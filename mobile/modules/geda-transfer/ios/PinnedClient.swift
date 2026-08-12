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

/// An HTTP client that trusts exactly one public key.
///
/// This exists because the receiver's certificate is self-signed and pinned:
/// `URLSession` with default trust would refuse it, and no amount of App
/// Transport Security configuration expresses "trust this one key". The
/// delegate below is the only place that decision is made.
///
/// It is also why file bytes never reach JavaScript. `URLSession` reads the
/// asset straight off disk and writes it to the socket; JavaScript orchestrates
/// and renders progress (AGENTS.md §3.8). A megabyte crossing the bridge per
/// photo would cost more than the network does.
final class PinnedClient: NSObject {

  struct Response {
    let status: Int
    let headers: [String: String]
    let body: String
  }

  enum ClientError: Error, LocalizedError {
    case pinMismatch
    case badURL(String)
    case noResponse
    case fileMissing(String)
    case cancelled

    var errorDescription: String? {
      switch self {
      case .pinMismatch:
        // Deliberately final. A pin mismatch is a hard failure with no
        // override: the recovery is to scan a fresh QR code, which requires
        // being in front of the receiver (AGENTS.md §3.5).
        return
          "This receiver's identity does not match the one you paired with. Pair again by scanning its QR code."
      case .badURL(let url): return "Not a usable address: \(url)"
      case .noResponse: return "The receiver closed the connection without answering"
      case .fileMissing(let path): return "The file is no longer there: \(path)"
      case .cancelled: return "Cancelled"
      }
    }
  }

  /// Per-task state. Held here rather than on the task because URLSession
  /// hands the delegate a task, not a context.
  private final class TaskState {
    var data = Data()
    var continuation: CheckedContinuation<Response, Error>?
    var uploadID: String?
    var bodyStream: (() -> InputStream)?
  }

  private let pin: String
  private var session: URLSession!
  private let queue = DispatchQueue(label: "app.geda.transfer.client")
  private var states: [Int: TaskState] = [:]
  private var tasksByUploadID: [String: URLSessionTask] = [:]

  /// Called on every progress update, off the main thread.
  var onProgress: ((_ uploadID: String, _ sent: Int64, _ total: Int64) -> Void)?

  init(pin: String) {
    self.pin = pin
    super.init()

    let config = URLSessionConfiguration.default
    // Six to eight streams saturate a Wi-Fi 6 link without making the
    // receiver's disk seek itself to death (docs/PROTOCOL.md, MaxConcurrency).
    // HTTP/2 multiplexes them over one connection; this cap is what keeps
    // URLSession from opening a second one.
    config.httpMaximumConnectionsPerHost = 8
    config.timeoutIntervalForRequest = 30
    // A 4K video on a slow link legitimately takes a long time; the request
    // timeout above still catches a receiver that has stopped answering.
    config.timeoutIntervalForResource = 24 * 60 * 60
    config.waitsForConnectivity = false
    config.requestCachePolicy = .reloadIgnoringLocalCacheData

    let delegateQueue = OperationQueue()
    delegateQueue.maxConcurrentOperationCount = 1
    session = URLSession(configuration: config, delegate: self, delegateQueue: delegateQueue)
  }

  func invalidate() {
    session.invalidateAndCancel()
  }

  // MARK: - Requests

  func send(
    url: String,
    method: String,
    headers: [String: String],
    body: Data?
  ) async throws -> Response {
    var request = try makeRequest(url: url, method: method, headers: headers)
    request.httpBody = body
    return try await perform(request, uploadID: nil, fromFile: nil, stream: nil)
  }

  /// Uploads a file's contents as the request body.
  ///
  /// `offset` resumes a partial upload. At offset zero the file is handed to
  /// URLSession directly, which is the fast path and the only one a background
  /// session will accept later; a resume streams from the same file starting
  /// at the offset, so no copy of a multi-gigabyte video is ever made.
  func upload(
    url: String,
    method: String,
    headers: [String: String],
    filePath: String,
    offset: Int64,
    uploadID: String
  ) async throws -> Response {
    // The library hands out `file://` URIs; percent-escapes and all. Treating
    // one as a plain path looks for a file called "file:" and fails with a
    // message about the wrong thing entirely.
    let fileURL =
      filePath.hasPrefix("file://")
      ? (URL(string: filePath) ?? URL(fileURLWithPath: filePath))
      : URL(fileURLWithPath: filePath)

    guard FileManager.default.fileExists(atPath: fileURL.path) else {
      throw ClientError.fileMissing(filePath)
    }

    let request = try makeRequest(url: url, method: method, headers: headers)

    if offset <= 0 {
      return try await perform(request, uploadID: uploadID, fromFile: fileURL, stream: nil)
    }

    let makeStream: () -> InputStream = {
      let stream = InputStream(url: fileURL) ?? InputStream(data: Data())
      stream.setProperty(NSNumber(value: offset), forKey: .fileCurrentOffsetKey)
      return stream
    }
    return try await perform(request, uploadID: uploadID, fromFile: nil, stream: makeStream)
  }

  func cancel(uploadID: String) {
    queue.sync {
      tasksByUploadID[uploadID]?.cancel()
    }
  }

  func cancelAll() {
    queue.sync {
      for task in tasksByUploadID.values {
        task.cancel()
      }
    }
  }

  // MARK: - Plumbing

  private func makeRequest(url: String, method: String, headers: [String: String]) throws
    -> URLRequest
  {
    guard let parsed = URL(string: url) else { throw ClientError.badURL(url) }

    var request = URLRequest(url: parsed)
    request.httpMethod = method
    for (key, value) in headers {
      request.setValue(value, forHTTPHeaderField: key)
    }
    return request
  }

  private func perform(
    _ request: URLRequest,
    uploadID: String?,
    fromFile: URL?,
    stream: (() -> InputStream)?
  ) async throws -> Response {
    try await withCheckedThrowingContinuation { continuation in
      let task: URLSessionTask
      if let fromFile {
        task = session.uploadTask(with: request, fromFile: fromFile)
      } else if stream != nil {
        task = session.uploadTask(withStreamedRequest: request)
      } else {
        task = session.dataTask(with: request)
      }

      let state = TaskState()
      state.continuation = continuation
      state.uploadID = uploadID
      state.bodyStream = stream

      queue.sync {
        states[task.taskIdentifier] = state
        if let uploadID {
          tasksByUploadID[uploadID] = task
        }
      }

      task.resume()
    }
  }

  private func state(for task: URLSessionTask) -> TaskState? {
    queue.sync { states[task.taskIdentifier] }
  }

  private func finish(_ task: URLSessionTask, with result: Result<Response, Error>) {
    let state: TaskState? = queue.sync {
      let state = states.removeValue(forKey: task.taskIdentifier)
      if let uploadID = state?.uploadID {
        tasksByUploadID.removeValue(forKey: uploadID)
      }
      return state
    }
    guard let continuation = state?.continuation else { return }
    state?.continuation = nil
    continuation.resume(with: result)
  }
}

// MARK: - URLSessionDelegate

extension PinnedClient: URLSessionDataDelegate {

  /// The pin check. Everything else in this file is plumbing around it.
  func urlSession(
    _ session: URLSession,
    didReceive challenge: URLAuthenticationChallenge,
    completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void
  ) {
    guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
      let trust = challenge.protectionSpace.serverTrust
    else {
      completionHandler(.performDefaultHandling, nil)
      return
    }

    if SPKIPin.trust(trust, matches: pin) {
      completionHandler(.useCredential, URLCredential(trust: trust))
    } else {
      completionHandler(.cancelAuthenticationChallenge, nil)
    }
  }

  func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
    queue.sync {
      states[dataTask.taskIdentifier]?.data.append(data)
    }
  }

  func urlSession(
    _ session: URLSession,
    task: URLSessionTask,
    needNewBodyStream completionHandler: @escaping (InputStream?) -> Void
  ) {
    completionHandler(state(for: task)?.bodyStream?())
  }

  func urlSession(
    _ session: URLSession,
    task: URLSessionTask,
    didSendBodyData bytesSent: Int64,
    totalBytesSent: Int64,
    totalBytesExpectedToSend: Int64
  ) {
    guard let uploadID = state(for: task)?.uploadID else { return }
    onProgress?(uploadID, totalBytesSent, totalBytesExpectedToSend)
  }

  func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
    if let error {
      let nsError = error as NSError
      if nsError.domain == NSURLErrorDomain, nsError.code == NSURLErrorCancelled {
        // A cancelled handshake is how the pin check refuses a connection,
        // and it is also what an explicit cancel looks like. The two are
        // distinguished by whether the trust evaluation failed.
        finish(task, with: .failure(ClientError.cancelled))
      } else if nsError.domain == NSURLErrorDomain,
        nsError.code == NSURLErrorServerCertificateUntrusted
          || nsError.code == NSURLErrorSecureConnectionFailed
      {
        finish(task, with: .failure(ClientError.pinMismatch))
      } else {
        finish(task, with: .failure(error))
      }
      return
    }

    guard let http = task.response as? HTTPURLResponse else {
      finish(task, with: .failure(ClientError.noResponse))
      return
    }

    var headers: [String: String] = [:]
    for (key, value) in http.allHeaderFields {
      if let key = key as? String, let value = value as? String {
        headers[key.lowercased()] = value
      }
    }

    let body = state(for: task)?.data ?? Data()
    finish(
      task,
      with: .success(
        Response(
          status: http.statusCode,
          headers: headers,
          body: String(data: body, encoding: .utf8) ?? ""
        )))
  }
}
