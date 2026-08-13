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

import ActivityKit
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

    Events(
      "onUploadProgress", "onUploadCreated", "onBackgroundProgress", "onBackgroundFinished",
      "onDownloadProgress", "onDownloadFinished")

    OnCreate {
      // Progress and completions from the background session, forwarded only
      // while there is a JavaScript runtime to forward them to. The transfer
      // does not depend on anyone listening.
      BackgroundUploader.shared.onEvent = { [weak self] event in
        guard let self else { return }
        switch event {
        case .progress(let uploadId, let sent, let total):
          self.sendEvent(
            "onBackgroundProgress",
            ["uploadId": uploadId, "bytesSent": sent, "totalBytes": total])
        case .finished(let job):
          self.sendEvent("onBackgroundFinished", Self.dictionary(from: job))
        }
      }

      BackgroundDownloader.shared.onEvent = { [weak self] event in
        guard let self else { return }
        switch event {
        case .progress(let itemId, let received, let total):
          self.sendEvent(
            "onDownloadProgress",
            ["itemId": itemId, "bytesReceived": received, "totalBytes": total])
        case .finished(let job):
          self.sendEvent("onDownloadFinished", Self.dictionary(from: job))
        }
      }
    }

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

    // MARK: Background transfers

    /// Where a caller must put the copy it wants uploaded in the background.
    Function("backgroundStagingDirectory") { () -> String in
      BackgroundStore.stagingDirectory().path
    }

    AsyncFunction("startBackground") { (requests: [BackgroundRequestOptions]) -> [String] in
      await BackgroundUploader.shared.start(requests.map { $0.asRequest() })
    }

    /// Hands back to the system anything it stopped working on, offset-aware.
    AsyncFunction("reconcileBackground") { () -> [[String: Any]] in
      await BackgroundUploader.shared.reconcile()
      return BackgroundUploader.shared.snapshot().map(Self.dictionary(from:))
    }

    AsyncFunction("retryBackground") { () -> [[String: Any]] in
      await BackgroundUploader.shared.retryFailed()
      return BackgroundUploader.shared.snapshot().map(Self.dictionary(from:))
    }

    Function("backgroundJobs") { () -> [[String: Any]] in
      BackgroundUploader.shared.snapshot().map(Self.dictionary(from:))
    }

    /// Forgets the delivered jobs, once the app has written them to its
    /// ledger. Failures stay, to be retried and to be shown.
    Function("clearDeliveredBackground") {
      BackgroundUploader.shared.clearDelivered()
    }

    Function("cancelBackground") {
      BackgroundUploader.shared.cancelAll()
    }

    /// Asks the system for a wake-up on power and Wi-Fi.
    Function("scheduleBackgroundKickoff") {
      GedaBackgroundDelegate.scheduleKickoff()
    }

    Function("liveActivitiesAvailable") { () -> Bool in
      ActivityAuthorizationInfo().areActivitiesEnabled
    }

    // MARK: Collecting from a receiver

    /// Where files that arrive as documents are kept, visible in the Files
    /// app. Media goes to the photo library instead, unless the user has
    /// turned that off (AGENTS.md §3.7).
    Function("receivedDirectory") { () -> String in
      DownloadStore.documentsDirectory().path
    }

    AsyncFunction("startDownloads") { (requests: [DownloadRequestOptions]) -> [String] in
      await BackgroundDownloader.shared.start(requests.map { $0.asRequest() })
    }

    /// Hands back to the system anything it stopped working on, resuming from
    /// its own token rather than from the beginning.
    AsyncFunction("reconcileDownloads") { () -> [[String: Any]] in
      await BackgroundDownloader.shared.reconcile()
      return BackgroundDownloader.shared.snapshot().map(Self.dictionary(from:))
    }

    AsyncFunction("retryDownloads") { () -> [[String: Any]] in
      await BackgroundDownloader.shared.retryFailed()
      return BackgroundDownloader.shared.snapshot().map(Self.dictionary(from:))
    }

    Function("downloadJobs") { () -> [[String: Any]] in
      BackgroundDownloader.shared.snapshot().map(Self.dictionary(from:))
    }

    /// Forgets a download the app has verified, saved, and acknowledged, and
    /// deletes the copy it was holding.
    Function("finishDownload") { (itemId: String) in
      BackgroundDownloader.shared.finish(itemId: itemId)
    }

    /// Records that a downloaded file could not be used -- a digest that did
    /// not match, a photo library that refused it. The bytes go with it.
    Function("failDownload") { (itemId: String, reason: String) in
      BackgroundDownloader.shared.fail(itemId: itemId, reason: reason)
    }

    Function("cancelDownloads") {
      BackgroundDownloader.shared.cancelAll()
    }

    /// The digest of a file on disk, computed natively.
    ///
    /// The bytes must not cross the bridge to be hashed any more than they may
    /// to be sent (AGENTS.md §3.8): a 2 GB archive read into JavaScript is a
    /// crash, not a slow path.
    AsyncFunction("sha256OfFile") { (path: String) -> String in
      try FileDigest.sha256(path: path)
    }

    // MARK: the photo library

    /// The flags that decide whether an asset is sent at all: screenshot,
    /// hidden, one frame of a burst, edited.
    ///
    /// `expo-media-library` has no concept of any of them, and every one is a
    /// send option in P8. Cheap enough to ask about a whole library at once --
    /// these are properties PHAsset already holds.
    AsyncFunction("assetFlags") { (assetIds: [String]) -> [[String: Any]] in
      AssetLibrary.flags(for: assetIds)
    }

    /// Hidden assets, which an ordinary library query does not return at all.
    AsyncFunction("hiddenAssets") { (limit: Int) -> [[String: Any]] in
      AssetLibrary.hiddenAssets(limit: limit)
    }

    /// Every resource one asset holds.
    ///
    /// A Live Photo is a still and a video; a ProRAW shot is a negative and
    /// often a JPEG; an edited photo is the capture and the render. Which of
    /// them to send is decided in TypeScript (src/core/selection.ts); this
    /// only reports what is there.
    AsyncFunction("assetResources") { (assetId: String) -> [[String: Any]] in
      try AssetLibrary.resources(of: assetId)
    }

    /// Copies one resource out of the library and returns its size.
    ///
    /// Only used for resources that cannot be reached as a plain file URL --
    /// a Live Photo's video, a raw negative's JPEG, the untouched original of
    /// an edited photo. The ordinary case still sends straight from the
    /// library with no copy at all, which is what keeps the P4 baseline.
    AsyncFunction("exportResource") { (assetId: String, type: String, destination: String, promise: Promise) in
      AssetLibrary.export(identifier: assetId, type: type, to: destination) { result in
        switch result {
        case .success(let size):
          promise.resolve(size)
        case .failure(let error):
          promise.reject(error)
        }
      }
    }

    OnDestroy {
      BackgroundUploader.shared.onEvent = nil
      BackgroundDownloader.shared.onEvent = nil
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

    var location = options.location
    var offset: Int64 = 0

    if let existing = location {
      switch try await Tus.offset(client: client, location: existing, token: options.token) {
      case .at(let value):
        offset = value
      case .gone:
        // The receiver swept the partial upload, or it finished and was
        // committed. Either way there is nothing to resume; start again.
        location = nil
      }
    }

    let target: String
    if let existing = location {
      target = existing
    } else {
      do {
        target = try await Tus.create(
          client: client, baseUrl: options.baseUrl, token: options.token,
          size: Int64(options.size), metadata: options.metadata)
      } catch {
        throw Exception(
          name: "ERR_UPLOAD_CREATE",
          description: (error as? LocalizedError)?.errorDescription ?? error.localizedDescription)
      }
      offset = 0
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

    let response = try await client.upload(
      url: target,
      method: "PATCH",
      headers: Tus.patchHeaders(token: options.token, offset: offset),
      filePath: options.filePath,
      offset: offset,
      uploadID: options.uploadId
    )

    guard response.status == 204 || response.status == 200 else {
      throw Exception(name: "ERR_UPLOAD", description: Tus.message(response))
    }

    return [
      "location": target,
      "status": response.status,
      // The receiver reports where the file landed once it is durable, which
      // is what makes a later "delete after transfer" safe to even consider.
      "storedPath": Tus.storedPath(from: response.headers),
      "bytesSent": Int64(options.size) - offset,
      "deduplicated": Tus.deduplicated(from: response.headers),
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

  private static func dictionary(from job: BackgroundJob) -> [String: Any] {
    [
      "uploadId": job.uploadId,
      "receiverId": job.receiverId,
      "assetId": job.assetId,
      "filename": job.filename,
      "state": job.state.rawValue,
      "size": job.size,
      // Against the whole asset, not against the current attempt: a resumed
      // upload starts its task at zero and the person watching does not care.
      "bytesSent": job.totalSent,
      "storedPath": job.storedPath ?? "",
      "deduplicated": job.deduplicated,
      "error": job.error ?? "",
    ]
  }

  private static func dictionary(from job: DownloadJob) -> [String: Any] {
    [
      "itemId": job.itemId,
      "receiverId": job.receiverId,
      "receiverName": job.receiverName,
      "filename": job.filename,
      "kind": job.kind,
      "capturedAt": job.capturedAt,
      "sha256": job.sha256,
      "size": job.size,
      "bytesReceived": min(job.bytesReceived, job.size),
      // "saved" is never reported from here: a job the app has saved is
      // forgotten, staged copy and all, rather than kept as a tombstone.
      "state": job.state.rawValue,
      "stagedPath": job.stagedPath ?? "",
      "error": job.error ?? "",
    ]
  }

}

/// One file to collect from a receiver.
///
/// There is no staging path to supply, unlike an upload: the system writes the
/// file itself and the app is told where it landed.
struct DownloadRequestOptions: Record {
  @Field var itemId: String = ""
  @Field var receiverId: String = ""
  @Field var receiverName: String = ""
  @Field var filename: String = ""
  @Field var kind: String = "file"
  @Field var capturedAt: String = ""
  @Field var baseUrl: String = ""
  @Field var path: String = ""
  @Field var pin: String = ""
  @Field var token: String = ""
  @Field var sha256: String = ""
  @Field var size: Int = 0

  func asRequest() -> BackgroundDownloader.Request {
    BackgroundDownloader.Request(
      itemId: itemId,
      receiverId: receiverId,
      receiverName: receiverName,
      filename: filename,
      kind: kind,
      capturedAt: capturedAt,
      baseUrl: baseUrl,
      path: path,
      pin: pin,
      token: token,
      sha256: sha256,
      size: Int64(size)
    )
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


/// One file to send in the background.
///
/// `stagedPath` is a copy the caller has already made inside the app
/// container. It cannot be a photo library path: the system process that does
/// the sending has no access to the library (see DECISIONS).
struct BackgroundRequestOptions: Record {
  @Field var uploadId: String = ""
  @Field var receiverId: String = ""
  @Field var receiverName: String = ""
  @Field var assetId: String = ""
  @Field var filename: String = ""
  @Field var baseUrl: String = ""
  @Field var pin: String = ""
  @Field var token: String = ""
  @Field var stagedPath: String = ""
  @Field var size: Int = 0
  @Field var metadata: [String: String] = [:]

  func asRequest() -> BackgroundUploader.Request {
    BackgroundUploader.Request(
      uploadId: uploadId,
      receiverId: receiverId,
      receiverName: receiverName,
      assetId: assetId,
      filename: filename,
      baseUrl: baseUrl,
      pin: pin,
      token: token,
      stagedPath: stagedPath,
      size: Int64(size),
      metadata: metadata
    )
  }
}
