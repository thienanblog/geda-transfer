// Copyright 2026 Geda
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import ExpoModulesCore
import Foundation

/// The native half of the transfer engine.
///
/// Two things force it to exist. The receiver's certificate is self-signed and
/// pinned, which no JavaScript HTTP client on this platform can express; and
/// file bytes must never cross the JavaScript bridge (AGENTS.md §3.8), so the
/// upload has to be a URLSession task reading straight from disk.
///
/// Everything above this -- which asset goes next, how many at once, what the
/// progress bar says -- stays in TypeScript, where it is testable.
public class GedaTransferModule: Module {

  /// One client per pinned key. Reused across uploads so that six to eight
  /// parallel transfers multiplex over a single HTTP/2 connection instead of
  /// paying for a TLS handshake per file.
  private var clients: [String: PinnedClient] = [:]
  private let clientsLock = NSLock()

  /// Progress is throttled per upload. URLSession reports every few kilobytes;
  /// forwarding all of it for eight concurrent transfers would spend more time
  /// crossing the bridge than the UI can use.
  private var lastProgressAt: [String: TimeInterval] = [:]
  private let progressLock = NSLock()
  private let progressInterval: TimeInterval = 0.1

  public func definition() -> ModuleDefinition {
    Name("GedaTransfer")

    Events("onUploadProgress", "onUploadCreated")

    AsyncFunction("request") { (options: RequestOptions) -> [String: Any] in
      let client = self.client(for: options.pin)
      let response = try await client.send(
        url: options.url,
        method: options.method,
        headers: self.headers(token: options.token, extra: options.headers),
        body: options.body?.data(using: .utf8)
      )
      return Self.dictionary(from: response)
    }

    // Happy Eyeballs across the candidate set: a paired receiver advertises
    // every address it has, including VPN addresses, and only the phone can
    // find out which of them works from where it is standing (AGENTS.md §3.4).
    AsyncFunction("race") { (urls: [String], pin: String, timeoutMs: Int) -> String? in
      try await self.race(urls: urls, pin: pin, timeoutMs: timeoutMs)
    }

    AsyncFunction("upload") { (options: UploadOptions) -> [String: Any] in
      try await self.upload(options)
    }

    AsyncFunction("cancel") { (uploadId: String) in
      self.eachClient { $0.cancel(uploadID: uploadId) }
    }

    AsyncFunction("cancelAll") {
      self.eachClient { $0.cancelAll() }
    }

    OnDestroy {
      self.eachClient { $0.invalidate() }
      self.clientsLock.lock()
      self.clients.removeAll()
      self.clientsLock.unlock()
    }
  }

  // MARK: - Transfer

  /// Runs one file through the tus protocol: create or resume, then send.
  ///
  /// The whole exchange happens here rather than as three calls from
  /// JavaScript because small files are the case that hurts. Two extra bridge
  /// round trips per photo is a real cost when the library holds ten thousand
  /// of them (AGENTS.md §5).
  private func upload(_ options: UploadOptions) async throws -> [String: Any] {
    let client = self.client(for: options.pin)
    let authorization = self.headers(token: options.token, extra: [:])

    var location = options.location
    var offset: Int64 = 0

    if let existing = location {
      var head = authorization
      head["Tus-Resumable"] = "1.0.0"
      let response = try await client.send(url: existing, method: "HEAD", headers: head, body: nil)

      if response.status == 200 || response.status == 204 {
        offset = Int64(response.headers["upload-offset"] ?? "") ?? 0
      } else {
        // The receiver swept the partial upload, or it finished and was
        // committed. Either way there is nothing to resume; start again.
        location = nil
      }
    }

    if location == nil {
      var create = authorization
      create["Tus-Resumable"] = "1.0.0"
      create["Upload-Length"] = String(options.size)
      if let metadata = Self.encodeMetadata(options.metadata) {
        create["Upload-Metadata"] = metadata
      }

      let response = try await client.send(
        url: options.baseUrl + "/v1/files/", method: "POST", headers: create, body: nil)
      guard response.status == 201, let created = response.headers["location"] else {
        throw Exception(name: "ERR_UPLOAD_CREATE", description: Self.message(response))
      }
      location = Self.absolute(created, base: options.baseUrl)
      offset = 0
    }

    guard let target = location else {
      throw Exception(name: "ERR_UPLOAD_CREATE", description: "the receiver returned no location")
    }

    // Announced before a byte moves. If the transfer is paused halfway
    // through a 4 GB video, this is what lets the next attempt ask the
    // receiver for its offset and carry on, instead of sending it all again.
    sendEvent("onUploadCreated", ["uploadId": options.uploadId, "location": target])

    if offset >= Int64(options.size) {
      // Everything already arrived; committing is the receiver's business.
      return [
        "location": target, "status": 204, "bytesSent": 0,
        "storedPath": "", "deduplicated": false, "resumedFrom": offset,
      ]
    }

    var patch = authorization
    patch["Tus-Resumable"] = "1.0.0"
    patch["Upload-Offset"] = String(offset)
    patch["Content-Type"] = "application/offset+octet-stream"

    let response = try await client.upload(
      url: target,
      method: "PATCH",
      headers: patch,
      filePath: options.filePath,
      offset: offset,
      uploadID: options.uploadId
    )

    guard response.status == 204 || response.status == 200 else {
      throw Exception(name: "ERR_UPLOAD", description: Self.message(response))
    }

    // The receiver reports where the file landed once it is durable, which is
    // what makes a later "delete after transfer" safe to even consider.
    var storedPath = ""
    if let encoded = response.headers["geda-stored-path"],
      let raw = Data(base64Encoded: encoded),
      let decoded = String(data: raw, encoding: .utf8)
    {
      storedPath = decoded
    }

    return [
      "location": target,
      "status": response.status,
      "bytesSent": Int64(options.size) - offset,
      "storedPath": storedPath,
      "deduplicated": response.headers["geda-deduplicated"] == "1",
      "resumedFrom": offset,
    ]
  }

  private func race(urls: [String], pin: String, timeoutMs: Int) async throws -> String? {
    guard !urls.isEmpty else { return nil }
    let client = self.client(for: pin)

    return await withTaskGroup(of: String?.self) { group in
      for url in urls {
        group.addTask {
          do {
            // A small stagger keeps a long candidate list from opening
            // dozens of sockets in the same millisecond; the first address
            // is usually the one that works, and it is tried immediately.
            if let index = urls.firstIndex(of: url), index > 0 {
              try await Task.sleep(nanoseconds: UInt64(index) * 50_000_000)
            }
            let response = try await client.send(
              url: url + "/v1/info", method: "GET", headers: [:], body: nil)
            return response.status == 200 ? url : nil
          } catch {
            return nil
          }
        }
      }

      group.addTask {
        try? await Task.sleep(nanoseconds: UInt64(max(timeoutMs, 1)) * 1_000_000)
        return nil
      }

      for await result in group {
        if let result {
          // First responder wins; the rest are pointless work on a phone's
          // battery (AGENTS.md §3.4).
          group.cancelAll()
          return result
        }
      }
      return nil
    }
  }

  // MARK: - Clients

  private func client(for pin: String) -> PinnedClient {
    clientsLock.lock()
    defer { clientsLock.unlock() }

    if let existing = clients[pin] {
      return existing
    }

    let client = PinnedClient(pin: pin)
    client.onProgress = { [weak self] uploadID, sent, total in
      self?.reportProgress(uploadID: uploadID, sent: sent, total: total)
    }
    clients[pin] = client
    return client
  }

  private func eachClient(_ body: (PinnedClient) -> Void) {
    clientsLock.lock()
    let all = Array(clients.values)
    clientsLock.unlock()
    all.forEach(body)
  }

  private func reportProgress(uploadID: String, sent: Int64, total: Int64) {
    let now = Date.timeIntervalSinceReferenceDate

    progressLock.lock()
    let last = lastProgressAt[uploadID] ?? 0
    let complete = total > 0 && sent >= total
    if now - last < progressInterval && !complete {
      progressLock.unlock()
      return
    }
    lastProgressAt[uploadID] = now
    if complete {
      lastProgressAt.removeValue(forKey: uploadID)
    }
    progressLock.unlock()

    sendEvent(
      "onUploadProgress",
      [
        "uploadId": uploadID,
        "bytesSent": sent,
        "totalBytes": total,
      ])
  }

  // MARK: - Helpers

  private func headers(token: String?, extra: [String: String]) -> [String: String] {
    var headers = extra
    if let token, !token.isEmpty {
      headers["Authorization"] = "Bearer \(token)"
    }
    return headers
  }

  private static func dictionary(from response: PinnedClient.Response) -> [String: Any] {
    ["status": response.status, "headers": response.headers, "body": response.body]
  }

  private static func message(_ response: PinnedClient.Response) -> String {
    response.body.isEmpty ? "the receiver answered \(response.status)" : response.body
  }

  /// tus metadata is `key base64(value)` pairs, comma separated.
  private static func encodeMetadata(_ metadata: [String: String]) -> String? {
    guard !metadata.isEmpty else { return nil }
    // Sorted so that the header is stable, which makes a failing request
    // reproducible from a log.
    return
      metadata
      .sorted { $0.key < $1.key }
      .map { "\($0.key) \(Data($0.value.utf8).base64EncodedString())" }
      .joined(separator: ",")
  }

  private static func absolute(_ location: String, base: String) -> String {
    if location.hasPrefix("http://") || location.hasPrefix("https://") {
      return location
    }
    return base + (location.hasPrefix("/") ? location : "/" + location)
  }
}

struct RequestOptions: Record {
  @Field var url: String = ""
  @Field var method: String = "GET"
  @Field var pin: String = ""
  @Field var token: String? = nil
  @Field var headers: [String: String] = [:]
  @Field var body: String? = nil
}

struct UploadOptions: Record {
  @Field var uploadId: String = ""
  @Field var baseUrl: String = ""
  @Field var pin: String = ""
  @Field var token: String? = nil
  @Field var filePath: String = ""
  @Field var size: Int = 0
  @Field var metadata: [String: String] = [:]
  /// Set when resuming an upload the receiver already knows about.
  @Field var location: String? = nil
}
