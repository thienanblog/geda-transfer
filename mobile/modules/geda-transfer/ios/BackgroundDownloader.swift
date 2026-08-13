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

import CryptoKit
import Foundation

/// Collecting files a computer has queued for this phone.
///
/// The desktop cannot push (AGENTS.md §3.7), so the app asks what is waiting
/// and hands the list to a background `URLSession`. From there the system owns
/// it: the user can put the phone in their pocket and the download continues,
/// and when it finishes the app is relaunched to be told.
///
/// Three things shape this, and they are the mirror image of the upload side:
///
///   * **A download task cannot be resumed from an offset the app chooses.**
///     What it can do is resume from URLSession's own resume token, which
///     turns a dropped connection on a 2 GB archive into a Range request
///     rather than a fresh start. The token is written to disk, because the
///     process holding it in memory may not survive.
///   * **The finished file has to be moved inside the delegate callback.** The
///     system deletes its temporary file the moment that method returns.
///   * **Nothing may be saved from here.** Writing to the photo library needs
///     PhotoKit and the app's own permission prompt, neither of which exists
///     in a process launched purely to deliver a completion. So a finished
///     download is parked, verified and saved by JavaScript later, and only
///     then acknowledged to the receiver.
final class BackgroundDownloader: NSObject {

  /// The system quotes this identifier when it relaunches the app, so it must
  /// never change between releases: a renamed session is a session whose
  /// in-flight downloads nobody claims.
  static let sessionIdentifier = "app.geda.transfer.download"

  static let shared = BackgroundDownloader(identifier: sessionIdentifier)

  /// What a caller must supply to collect one file.
  struct Request {
    let itemId: String
    let receiverId: String
    let receiverName: String
    let filename: String
    let kind: String
    let capturedAt: String
    let baseUrl: String
    let path: String
    let pin: String
    let token: String
    let sha256: String
    let size: Int64
  }

  enum Event {
    case progress(itemId: String, received: Int64, total: Int64)
    case finished(DownloadJob)
  }

  /// Set while JavaScript is alive; nil in a process the system launched only
  /// to deliver a completion. The download does not depend on it.
  var onEvent: ((Event) -> Void)?

  private var systemCompletion: (() -> Void)?

  private let store: DownloadStore
  private var session: URLSession!
  private let lock = NSLock()
  private var lastEventAt: [String: TimeInterval] = [:]
  private let progressInterval: TimeInterval = 0.25
  private var lastPersistAt: [String: TimeInterval] = [:]
  private let persistInterval: TimeInterval = 5
  /// How long a freshly enqueued job is left alone before it counts as lost.
  private static let settleSeconds: TimeInterval = 10

  init(identifier: String, store: DownloadStore = .shared) {
    self.store = store
    super.init()

    let config = URLSessionConfiguration.background(withIdentifier: identifier)
    // The OS decides when to spend the radio, which is what makes a background
    // transfer cheap enough for it to allow at all (AGENTS.md §3.2).
    config.isDiscretionary = true
    // Without this the app is never relaunched to hear that a file arrived,
    // and the download sits in the container unseen.
    config.sessionSendsLaunchEvents = true
    // Somebody's monthly allowance is not the place for a 2 GB archive.
    config.allowsCellularAccess = false
    config.allowsExpensiveNetworkAccess = false
    config.allowsConstrainedNetworkAccess = false
    config.timeoutIntervalForResource = 7 * 24 * 60 * 60
    config.httpMaximumConnectionsPerHost = 4

    let queue = OperationQueue()
    queue.maxConcurrentOperationCount = 1
    session = URLSession(configuration: config, delegate: self, delegateQueue: queue)
  }

  // MARK: - Starting work

  /// Hands a batch to the system and returns the ids it accepted.
  @discardableResult
  func start(_ requests: [Request]) async -> [String] {
    let live = await liveItemIds()
    var started: [String] = []

    for request in requests {
      // A check that runs while the same file is already coming down must not
      // start it twice: the app may have been relaunched in between, so the
      // only thing the two runs share is the item id.
      if live.contains(request.itemId) { continue }
      if let existing = store.job(itemId: request.itemId), !existing.isFinished { continue }

      let job = DownloadJob(
        itemId: request.itemId,
        receiverId: request.receiverId,
        receiverName: request.receiverName,
        filename: request.filename,
        kind: request.kind,
        capturedAt: request.capturedAt,
        baseUrl: request.baseUrl,
        path: request.path,
        pin: request.pin,
        token: request.token,
        sha256: request.sha256,
        size: request.size,
        bytesReceived: 0,
        state: .pending,
        stagedPath: nil,
        resumeDataPath: nil,
        error: nil,
        updatedAt: Date().timeIntervalSince1970,
        attempts: 0
      )
      store.upsert(job)

      guard enqueue(job) else { continue }
      started.append(request.itemId)
    }

    return started
  }

  /// Hands back to the system anything it stopped working on.
  ///
  /// Called on launch and whenever the app returns to the foreground. A task
  /// the system gave up on while the app was gone leaves a job that no
  /// callback will ever arrive for, and only a sweep from here notices.
  func reconcile() async {
    let live = await liveItemIds()
    let now = Date().timeIntervalSince1970

    for job in store.all() where !job.isFinished {
      if live.contains(job.itemId) { continue }
      // Freshly enqueued tasks have not appeared in the session's own list
      // yet; restarting those would be a loop that downloads nothing.
      if now - job.updatedAt < Self.settleSeconds { continue }

      restart(job)
    }
  }

  /// Retries what failed, up to the attempt limit.
  ///
  /// The app being open is decent evidence that the phone is somewhere with a
  /// network again, which is the most common reason a download failed.
  func retryFailed() async {
    for job in store.all() where job.state == .failed && job.attempts < DownloadJob.maxAttempts {
      restart(job)
    }
  }

  private func restart(_ job: DownloadJob) {
    guard
      let updated = store.update(itemId: job.itemId, { current in
        current.state = .pending
        current.attempts += 1
        current.error = nil
      })
    else { return }

    if !enqueue(updated) {
      fail(itemId: job.itemId, reason: "could not be started again")
    }
  }

  /// Creates the task. Resume data is used when there is any, so an
  /// interrupted download continues rather than starting from zero.
  private func enqueue(_ job: DownloadJob) -> Bool {
    let task: URLSessionDownloadTask

    if let path = job.resumeDataPath, let data = try? Data(contentsOf: URL(fileURLWithPath: path)),
      !data.isEmpty
    {
      task = session.downloadTask(withResumeData: data)
      // Spent: keeping it would resume from a token that no longer describes
      // what is on disk if this attempt makes progress.
      try? FileManager.default.removeItem(atPath: path)
      store.update(itemId: job.itemId) { $0.resumeDataPath = nil }
    } else {
      guard let url = URL(string: job.baseUrl + job.path) else { return false }
      var request = URLRequest(url: url)
      request.httpMethod = "GET"
      request.setValue("Bearer " + job.token, forHTTPHeaderField: "Authorization")
      task = session.downloadTask(with: request)
    }

    // The only thing that survives to the delegate, which may be running in a
    // process that has never seen this code path execute.
    task.taskDescription = job.itemId
    task.resume()
    return true
  }

  // MARK: - Inspecting

  func snapshot() -> [DownloadJob] {
    store.all().sorted { $0.updatedAt < $1.updatedAt }
  }

  /// Forgets a job that the app has verified, saved, and acknowledged.
  func finish(itemId: String) {
    store.remove(itemIds: [itemId])
  }

  /// Records that a downloaded file could not be used.
  ///
  /// The staged bytes go with it: a file whose digest did not match is not
  /// something to keep, and it is certainly not something to save.
  func fail(itemId: String, reason: String) {
    guard let job = store.job(itemId: itemId) else { return }
    if let staged = job.stagedPath {
      try? FileManager.default.removeItem(atPath: staged)
    }
    store.update(itemId: itemId) { current in
      current.state = .failed
      current.stagedPath = nil
      current.error = reason
    }
  }

  func cancelAll() {
    session.getAllTasks { tasks in
      for task in tasks { task.cancel() }
    }
    store.removeAll()
  }

  func adopt(systemCompletion handler: @escaping () -> Void) {
    lock.lock()
    systemCompletion = handler
    lock.unlock()
  }

  private func liveItemIds() async -> Set<String> {
    let tasks = await session.allTasks
    return Set(
      tasks
        .filter { $0.state == .running || $0.state == .suspended }
        .compactMap { $0.taskDescription })
  }

  // MARK: - Files

  /// Moves the system's temporary file somewhere it will still exist.
  ///
  /// Must happen inside the delegate callback: the file is deleted the moment
  /// that method returns, and a download the user waited an hour for would be
  /// gone with it.
  private func park(_ location: URL, for job: DownloadJob) throws -> String {
    let directory = DownloadStore.stagingDirectory()
    let destination = directory.appendingPathComponent(stagedName(for: job))

    // A leftover from an attempt that failed after parking. Deleting first is
    // what makes this safe to run twice.
    try? FileManager.default.removeItem(at: destination)
    try FileManager.default.moveItem(at: location, to: destination)
    return destination.path
  }

  /// A name built from the item id, never from the receiver's filename.
  ///
  /// The filename crossed a network. It is sanitised on the JavaScript side
  /// before it becomes a path in the photo library or in Documents, and this
  /// side does not need it at all: an id and an extension are enough, and an
  /// id cannot contain a separator.
  private func stagedName(for job: DownloadJob) -> String {
    let safeId = job.itemId.filter { $0.isLetter || $0.isNumber || $0 == "-" || $0 == "_" }
    let base = safeId.isEmpty ? UUID().uuidString : safeId

    let extension_ = (job.filename as NSString).pathExtension.filter {
      $0.isLetter || $0.isNumber
    }
    return extension_.isEmpty ? base : "\(base).\(extension_.lowercased())"
  }

  // MARK: - Progress

  private func report(job: DownloadJob, received: Int64) {
    let now = Date().timeIntervalSince1970

    lock.lock()
    let last = lastEventAt[job.itemId] ?? 0
    let due = now - last >= progressInterval
    if due { lastEventAt[job.itemId] = now }

    let lastPersist = lastPersistAt[job.itemId] ?? 0
    let persist = now - lastPersist >= persistInterval
    if persist { lastPersistAt[job.itemId] = now }
    lock.unlock()

    // Written to disk rarely on purpose: it is a hint for a progress bar, and
    // the resume token is what a restart is actually built from.
    if persist {
      store.update(itemId: job.itemId) { current in
        current.bytesReceived = received
        if current.state == .pending { current.state = .running }
      }
    }

    if due {
      onEvent?(.progress(itemId: job.itemId, received: received, total: job.size))
    }
  }
}

// MARK: - URLSessionDelegate

extension BackgroundDownloader: URLSessionDownloadDelegate {

  /// The pin check, per task rather than per session: every download carries
  /// the key of the receiver it came from, and this process may never have
  /// spoken to that receiver before.
  func urlSession(
    _ session: URLSession,
    task: URLSessionTask,
    didReceive challenge: URLAuthenticationChallenge,
    completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void
  ) {
    guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
      let trust = challenge.protectionSpace.serverTrust,
      let itemId = task.taskDescription,
      let job = store.job(itemId: itemId)
    else {
      completionHandler(.performDefaultHandling, nil)
      return
    }

    if SPKIPin.trust(trust, matches: job.pin) {
      completionHandler(.useCredential, URLCredential(trust: trust))
    } else {
      // No override, ever. Recovery is a fresh QR code, which needs physical
      // presence at the receiver (AGENTS.md §3.5).
      completionHandler(.cancelAuthenticationChallenge, nil)
    }
  }

  func urlSession(
    _ session: URLSession,
    downloadTask: URLSessionDownloadTask,
    didWriteData bytesWritten: Int64,
    totalBytesWritten: Int64,
    totalBytesExpectedToWrite: Int64
  ) {
    guard let itemId = downloadTask.taskDescription, let job = store.job(itemId: itemId) else {
      return
    }
    report(job: job, received: totalBytesWritten)
  }

  func urlSession(
    _ session: URLSession,
    downloadTask: URLSessionDownloadTask,
    didFinishDownloadingTo location: URL
  ) {
    guard let itemId = downloadTask.taskDescription, let job = store.job(itemId: itemId) else {
      return
    }

    // A 404 or a 401 still "finishes" a download -- of the error document.
    // Saving that as somebody's holiday video is the failure this catches.
    let status = (downloadTask.response as? HTTPURLResponse)?.statusCode ?? 0
    guard (200...299).contains(status) else {
      fail(itemId: itemId, reason: "the computer answered with HTTP \(status)")
      if let finished = store.job(itemId: itemId) { onEvent?(.finished(finished)) }
      return
    }

    do {
      let parked = try park(location, for: job)
      let updated = store.update(itemId: itemId) { current in
        current.state = .ready
        current.stagedPath = parked
        current.bytesReceived = current.size
        current.error = nil
        current.resumeDataPath = nil
      }
      if let updated { onEvent?(.finished(updated)) }
    } catch {
      fail(itemId: itemId, reason: "could not be saved to this phone: \(error.localizedDescription)")
      if let finished = store.job(itemId: itemId) { onEvent?(.finished(finished)) }
    }
  }

  func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
    guard let itemId = task.taskDescription else { return }

    lock.lock()
    lastEventAt[itemId] = nil
    lastPersistAt[itemId] = nil
    lock.unlock()

    guard let error else { return }

    // An interruption is not a failure. The system hands back a token that
    // resumes by Range, and the next sweep uses it.
    let nsError = error as NSError
    if let resume = nsError.userInfo[NSURLSessionDownloadTaskResumeData] as? Data, !resume.isEmpty {
      let path = DownloadStore.stagingDirectory()
        .appendingPathComponent("\(itemId).resume").path
      try? resume.write(to: URL(fileURLWithPath: path), options: .atomic)
      store.update(itemId: itemId) { current in
        current.state = .pending
        current.resumeDataPath = path
      }
      return
    }

    fail(itemId: itemId, reason: describe(error))
    if let job = store.job(itemId: itemId) { onEvent?(.finished(job)) }
  }

  func urlSessionDidFinishEvents(forBackgroundURLSession session: URLSession) {
    lock.lock()
    let handler = systemCompletion
    systemCompletion = nil
    lock.unlock()

    // On the main thread and promptly: an app that dawdles here is killed, and
    // one that is killed often enough stops being woken at all.
    DispatchQueue.main.async { handler?() }
  }

  private func describe(_ error: Error) -> String {
    let nsError = error as NSError
    if nsError.domain == NSURLErrorDomain {
      switch nsError.code {
      case NSURLErrorServerCertificateUntrusted, NSURLErrorSecureConnectionFailed:
        return
          "that computer's identity does not match the one you paired with; pair again by scanning its QR code"
      case NSURLErrorCannotConnectToHost, NSURLErrorTimedOut, NSURLErrorNetworkConnectionLost:
        return "that computer could not be reached"
      default:
        break
      }
    }
    return error.localizedDescription
  }
}

/// Streaming SHA-256 of a file.
///
/// The digest the receiver published is SHA-256 rather than the BLAKE3 used
/// everywhere else, for exactly this: CryptoKit computes it on the CPU's own
/// crypto instructions, and shipping a second hash implementation in Swift
/// would be a correctness risk in exchange for nothing (docs/DECISIONS.md).
///
/// Chunked, because a 2 GB archive read into memory to be hashed is a crash on
/// most of the phones this has to work on.
enum FileDigest {

  static func sha256(path: String) throws -> String {
    let handle = try FileHandle(forReadingFrom: URL(fileURLWithPath: path))
    defer { try? handle.close() }

    var hasher = SHA256()
    while true {
      let chunk = try handle.read(upToCount: 1 << 20) ?? Data()
      if chunk.isEmpty { break }
      hasher.update(data: chunk)
    }

    return hasher.finalize().map { String(format: "%02x", $0) }.joined()
  }
}
