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

import Foundation

/// The parts of tus that both transfer paths need.
///
/// Creating an upload and asking it for its offset are identical whether the
/// bytes will then be sent by a foreground session or by a background one.
/// Only the `PATCH` differs, because a background session can send a file and
/// nothing else.
enum Tus {

  static let version = "1.0.0"

  /// The result of asking a receiver what it holds.
  enum Offset {
    /// The receiver still has the partial upload, at this offset.
    case at(Int64)
    /// The upload is gone -- swept, or already committed. Start again.
    case gone
  }

  struct Failure: LocalizedError {
    let summary: String
    var errorDescription: String? { summary }
  }

  static func headers(token: String?, extra: [String: String] = [:]) -> [String: String] {
    var headers = extra
    headers["Tus-Resumable"] = version
    if let token, !token.isEmpty {
      headers["Authorization"] = "Bearer \(token)"
    }
    return headers
  }

  /// `POST /v1/files/` -> the upload's URL.
  static func create(
    client: PinnedClient,
    baseUrl: String,
    token: String?,
    size: Int64,
    metadata: [String: String]
  ) async throws -> String {
    var request = headers(token: token)
    request["Upload-Length"] = String(size)
    if let encoded = encodeMetadata(metadata) {
      request["Upload-Metadata"] = encoded
    }

    let response = try await client.send(
      url: baseUrl + "/v1/files/", method: "POST", headers: request, body: nil)

    guard response.status == 201, let location = response.headers["location"] else {
      throw Failure(summary: message(response))
    }
    return absolute(location, base: baseUrl)
  }

  /// `HEAD` the upload to learn how much of it the receiver already has.
  static func offset(client: PinnedClient, location: String, token: String?) async throws -> Offset {
    let response = try await client.send(
      url: location, method: "HEAD", headers: headers(token: token), body: nil)

    guard response.status == 200 || response.status == 204 else { return .gone }
    return .at(Int64(response.headers["upload-offset"] ?? "") ?? 0)
  }

  static func patchHeaders(token: String?, offset: Int64) -> [String: String] {
    var request = headers(token: token)
    request["Upload-Offset"] = String(offset)
    request["Content-Type"] = "application/offset+octet-stream"
    return request
  }

  /// Where the receiver filed the upload, from the final `PATCH` response.
  ///
  /// The header is base64 of UTF-8 because HTTP headers are Latin-1 by
  /// specification and a path may hold any character at all
  /// (docs/PROTOCOL.md §5.3).
  static func storedPath(from headers: [String: String]) -> String {
    guard let encoded = headers["geda-stored-path"],
      let raw = Data(base64Encoded: encoded),
      let decoded = String(data: raw, encoding: .utf8)
    else { return "" }
    return decoded
  }

  static func deduplicated(from headers: [String: String]) -> Bool {
    headers["geda-deduplicated"] == "1"
  }

  /// tus metadata is `key base64(value)` pairs, comma separated.
  static func encodeMetadata(_ metadata: [String: String]) -> String? {
    guard !metadata.isEmpty else { return nil }
    // Sorted so the header is stable, which makes a failing request
    // reproducible from a log.
    return
      metadata
      .sorted { $0.key < $1.key }
      .map { "\($0.key) \(Data($0.value.utf8).base64EncodedString())" }
      .joined(separator: ",")
  }

  static func absolute(_ location: String, base: String) -> String {
    if location.hasPrefix("http://") || location.hasPrefix("https://") {
      return location
    }
    return base + (location.hasPrefix("/") ? location : "/" + location)
  }

  static func message(_ response: PinnedClient.Response) -> String {
    response.body.isEmpty ? "the receiver answered \(response.status)" : response.body
  }

  /// Lower-cased header names, as `PinnedClient` reports them, from the
  /// headers `URLSession` hands a delegate.
  static func normalize(_ response: URLResponse?) -> (status: Int, headers: [String: String]) {
    guard let http = response as? HTTPURLResponse else { return (0, [:]) }
    var headers: [String: String] = [:]
    for (key, value) in http.allHeaderFields {
      if let key = key as? String, let value = value as? String {
        headers[key.lowercased()] = value
      }
    }
    return (http.statusCode, headers)
  }
}
