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

/// Uploads that outlive the app.
///
/// A background `URLSession` is not a faster session or a quieter one: it is a
/// session whose tasks are owned by `nsurlsessiond`, a system process. When the
/// user swipes the app away, that process keeps sending the file, and when the
/// task finishes the system relaunches the app in the background to tell it so.
/// That is the whole point of P5, and it constrains the design in three ways
/// that are easy to get wrong:
///
///   * **The body must be a file, and only a file.** No data body, no streamed
///     request. So resuming from an offset means writing the remainder out as
///     its own file first (`slice`), because there is no other way to say
///     "start at byte 40,000,000".
///   * **The file must be readable by another process.** The photo library's
///     originals are not; the app reads them through an entitlement that
///     `nsurlsessiond` does not have. Every background upload is therefore
///     staged into the app container first (see DECISIONS).
///   * **Nothing may be looked up in JavaScript.** The delegate can run in a
///     process launched purely to deliver a completion, with no React runtime
///     and no chance of starting one. Everything the delegate needs -- the
///     pin, the token, the upload URL -- is on disk in `BackgroundStore`.
final class BackgroundUploader: NSObject {

  /// The system identifies the session by this string when it relaunches the
  /// app, so it must never change between releases: a renamed session is a
  /// session whose in-flight uploads nobody claims.
  static let sessionIdentifier = "app.geda.transfer.upload"

  /// Exactly one session per identifier, for the lifetime of the process:
  /// creating a second one with the same identifier is a crash on some
  /// releases and undefined behaviour on the rest.
  static let shared = BackgroundUploader(identifier: sessionIdentifier)

  /// What a caller must supply to send one file in the background.
  struct Request {
    let uploadId: String
    let receiverId: String
    let receiverName: String
    let assetId: String
    let filename: String
    let baseUrl: String
    let pin: String
    let token: String
    /// A copy inside the app container. Deleted when the job finishes.
    let stagedPath: String
    let size: Int64
    let metadata: [String: String]
  }

  enum Event {
    case progress(uploadId: String, sent: Int64, total: Int64)
    case finished(BackgroundJob)
  }

  /// Set while JavaScript is alive; nil in a process the system launched only
  /// to deliver a completion. The transfer does not depend on it.
  var onEvent: ((Event) -> Void)?

  /// Handed over by the app delegate when the system wakes the app for this
  /// session. Must be called, on the main thread, once the delegate has
  /// finished handling every queued completion -- otherwise the app is
  /// terminated for taking too long, and the system stops being generous
  /// about waking it at all.
  private var systemCompletion: (() -> Void)?

  private let identifier: String
  private let store: BackgroundStore
  private var session: URLSession!
  private let lock = NSLock()
  private var clients: [String: PinnedClient] = [:]
  /// Live progress, which is deliberately not written to disk on every
  /// callback: it is a hint for the UI, and the receiver's offset is the
  /// truth that a resume is built from.
  private var liveProgress: [String: Int64] = [:]
  private var lastPersistAt: [String: TimeInterval] = [:]
  private let persistInterval: TimeInterval = 5
  private let progressInterval: TimeInterval = 0.25
  /// How long a freshly enqueued job is left alone before it counts as lost.
  private static let settleSeconds: TimeInterval = 10
  private var lastEventAt: [String: TimeInterval] = [:]

  init(identifier: String, store: BackgroundStore = .shared) {
    self.identifier = identifier
    self.store = store
    super.init()

    let config = URLSessionConfiguration.background(withIdentifier: identifier)
    // Discretionary: the OS decides when to spend the radio, which is what
    // makes a background transfer cheap enough for the system to allow at all
    // (AGENTS.md §3.2). It is honoured only for tasks started while the app is
    // in the background; the system ignores it for a session kicked off in the
    // foreground, which is the behaviour we want in both cases.
    config.isDiscretionary = true
    // Without this the app is not relaunched when the transfer finishes, and
    // the ledger never learns that the file arrived.
    config.sessionSendsLaunchEvents = true
    // A photo library over cellular is somebody's monthly allowance.
    config.allowsCellularAccess = false
    config.allowsExpensiveNetworkAccess = false
    config.allowsConstrainedNetworkAccess = false
    // Background tasks are scheduled by the system and may sit for a long
    // time before they run; a week is the system's own maximum and anything
    // shorter throws away work the user asked for.
    config.timeoutIntervalForResource = 7 * 24 * 60 * 60
    config.httpMaximumConnectionsPerHost = 4

    let queue = OperationQueue()
    queue.maxConcurrentOperationCount = 1
    session = URLSession(configuration: config, delegate: self, delegateQueue: queue)
  }

  // MARK: - Starting work

  /// Creates the uploads and hands the files to the system.
  ///
  /// Creation happens here, while the app is running, rather than from the
  /// background session: a background session can send a file, and cannot do
  /// the `POST` that produces somewhere to send it to. Pre-creating every URL
  /// is what lets the whole batch survive the app being killed a second later.
  @discardableResult
  func start(_ requests: [Request]) async -> [String] {
    var started: [String] = []
    LiveActivity.enable()

    for request in requests {
      guard FileManager.default.fileExists(atPath: request.stagedPath) else { continue }

      do {
        let location = try await Tus.create(
          client: client(for: request.pin),
          baseUrl: request.baseUrl,
          token: request.token,
          size: request.size,
          metadata: request.metadata
        )

        let job = BackgroundJob(
          uploadId: request.uploadId,
          receiverId: request.receiverId,
          receiverName: request.receiverName,
          assetId: request.assetId,
          filename: request.filename,
          baseUrl: request.baseUrl,
          location: location,
          pin: request.pin,
          token: request.token,
          stagedPath: request.stagedPath,
          slicePath: nil,
          offset: 0,
          size: request.size,
          state: .pending,
          bytesSent: 0,
          storedPath: nil,
          deduplicated: false,
          error: nil,
          updatedAt: Date().timeIntervalSince1970,
          attempts: 1,
          metadata: request.metadata
        )
        store.upsert(job)
        enqueue(job)
        started.append(job.uploadId)
      } catch {
        // One receiver being unreachable must not lose the staged copy: the
        // job is recorded as failed so the next kickoff can retry it, and so
        // the staged bytes have an owner that will eventually delete them.
        store.upsert(
          BackgroundJob(
            uploadId: request.uploadId, receiverId: request.receiverId,
            receiverName: request.receiverName, assetId: request.assetId,
            filename: request.filename, baseUrl: request.baseUrl, location: "", pin: request.pin,
            token: request.token, stagedPath: request.stagedPath, slicePath: nil, offset: 0,
            size: request.size, state: .failed, bytesSent: 0, storedPath: nil,
            deduplicated: false, error: describe(error),
            updatedAt: Date().timeIntervalSince1970, attempts: 1,
            metadata: request.metadata))
      }
    }

    LiveActivity.refresh()
    return started
  }

  /// Picks up everything the system is no longer working on.
  ///
  /// Called on launch and from the `BGProcessingTask`. A job in the store with
  /// no task behind it was interrupted -- the app was killed while the network
  /// was down, or a previous attempt errored -- so the receiver is asked what
  /// it already holds and only the remainder is sent again.
  func reconcile() async {
    let live = await liveUploadIds()

    for job in store.all() where !job.isFinished && !live.contains(job.uploadId) {
      // A job enqueued a moment ago may not have appeared in `getAllTasks`
      // yet. Restarting it would create a second task against the same
      // upload URL, and two writers at one offset is a corrupt file.
      guard Date().timeIntervalSince1970 - job.updatedAt > Self.settleSeconds else { continue }
      await restart(job)
    }
  }

  /// Retries the failed jobs too, offset-aware. Used by the periodic kickoff.
  func retryFailed() async {
    let live = await liveUploadIds()

    for job in store.all() where job.state == .failed && !live.contains(job.uploadId) {
      await restart(job)
    }
  }

  private func restart(_ job: BackgroundJob) async {
    guard FileManager.default.fileExists(atPath: job.stagedPath) else {
      // Nothing left to send. Drop the record rather than retrying forever.
      store.remove(uploadIds: [job.uploadId])
      return
    }

    guard job.attempts < BackgroundJob.maxAttempts else {
      // Given up on. Dropping the record also deletes the staged copy, and
      // the asset is still in the library for a later batch to stage again --
      // it never reached the ledger, so nothing thinks it was sent.
      store.remove(uploadIds: [job.uploadId])
      return
    }

    var current = job
    var offset: Int64 = 0

    if !current.location.isEmpty {
      do {
        switch try await Tus.offset(
          client: client(for: current.pin), location: current.location, token: current.token)
        {
        case .at(let value):
          offset = min(value, current.size)
        case .gone:
          current.location = ""
        }
      } catch {
        // The receiver is not reachable from here right now. Leave the job
        // where it is; the next kickoff tries again.
        store.update(uploadId: current.uploadId) { $0.error = describe(error) }
        return
      }
    }

    if current.location.isEmpty {
      do {
        current.location = try await Tus.create(
          client: client(for: current.pin), baseUrl: current.baseUrl, token: current.token,
          size: current.size, metadata: current.metadata)
        offset = 0
      } catch {
        store.update(uploadId: current.uploadId) { $0.error = describe(error) }
        return
      }
    }

    if offset >= current.size {
      // Everything arrived; the receiver committed it and swept the record.
      // There is no stored path to report from here, which the app reconciles
      // against its own ledger.
      finish(uploadId: current.uploadId, state: .done, status: 204, headers: [:], error: nil)
      return
    }

    // A background task sends a file, so the remainder has to be one.
    var slicePath: String? = nil
    if offset > 0 {
      do {
        slicePath = try slice(path: current.stagedPath, from: offset, uploadId: current.uploadId)
      } catch {
        store.update(uploadId: current.uploadId) { $0.error = describe(error) }
        return
      }
    }

    if let previous = current.slicePath, previous != slicePath {
      try? FileManager.default.removeItem(atPath: previous)
    }

    current.slicePath = slicePath
    current.offset = offset
    current.bytesSent = 0
    current.state = .pending
    current.error = nil
    current.attempts += 1
    current.updatedAt = Date().timeIntervalSince1970
    store.upsert(current)

    enqueue(current)
  }

  private func enqueue(_ job: BackgroundJob) {
    guard let url = URL(string: job.location), url.scheme == "https" else {
      store.update(uploadId: job.uploadId) {
        $0.state = .failed
        $0.error = "the receiver gave an address that cannot be used: \(job.location)"
      }
      return
    }

    var request = URLRequest(url: url)
    request.httpMethod = "PATCH"
    for (key, value) in Tus.patchHeaders(token: job.token, offset: job.offset) {
      request.setValue(value, forHTTPHeaderField: key)
    }

    let task = session.uploadTask(
      with: request, fromFile: URL(fileURLWithPath: job.bodyPath))
    // The identifier the delegate uses to find the job again. `taskDescription`
    // is the only field that survives the app being killed and the task being
    // handed back by `getAllTasks` in a new process.
    task.taskDescription = job.uploadId

    // Before `resume`, not after. A small photo on a fast link can finish
    // between the two, and writing `.running` over the `.done` the delegate
    // just recorded would strand the job: never written to the ledger, its
    // staged copy already deleted, retried on every wake-up for ever.
    store.update(uploadId: job.uploadId) { $0.state = .running }
    task.resume()
  }

  // MARK: - Inspecting

  /// Every job, with live progress folded in.
  func snapshot() -> [BackgroundJob] {
    lock.lock()
    let progress = liveProgress
    lock.unlock()

    return store.all().map { job in
      guard let sent = progress[job.uploadId], !job.isFinished else { return job }
      var merged = job
      merged.bytesSent = max(job.bytesSent, sent)
      return merged
    }
  }

  func cancelAll() {
    LiveActivity.disable()
    lock.lock()
    liveProgress.removeAll()
    lastPersistAt.removeAll()
    lastEventAt.removeAll()
    lock.unlock()

    // The staged files are deleted only after the tasks holding them are
    // cancelled: removing the body out from under a task in flight is a
    // different, more confusing failure than the cancel the user asked for.
    session.getAllTasks { [store] tasks in
      for task in tasks { task.cancel() }
      store.removeAll()
    }
  }

  /// Forgets the uploads that arrived, once the app has written them down.
  ///
  /// Failures are deliberately kept. They are what the next wake-up retries,
  /// and what the app shows someone wondering where four of their photos
  /// went; `restart` is where a job is finally given up on.
  func clearDelivered() {
    let delivered = store.all().filter { $0.state == .done }.map(\.uploadId)
    store.remove(uploadIds: delivered)
  }

  func adopt(systemCompletion handler: @escaping () -> Void) {
    lock.lock()
    let previous = systemCompletion
    systemCompletion = handler
    lock.unlock()
    // Two wake-ups without a finish in between should not lose the first
    // handler; leaving one uncalled is what gets an app terminated.
    previous?()
  }

  private func liveUploadIds() async -> Set<String> {
    await withCheckedContinuation { continuation in
      session.getAllTasks { tasks in
        let ids = tasks.compactMap { task -> String? in
          guard task.state == .running || task.state == .suspended else { return nil }
          return task.taskDescription
        }
        continuation.resume(returning: Set(ids))
      }
    }
  }

  // MARK: - Files

  /// Writes `path` from `offset` to the end into its own file.
  ///
  /// Copied in blocks rather than read whole: the remainder of a 4K video is
  /// gigabytes, and reading it into memory on a phone is a termination.
  private func slice(path: String, from offset: Int64, uploadId: String) throws -> String {
    let destination = BackgroundStore.stagingDirectory()
      .appendingPathComponent("\(uploadId).slice")
    try? FileManager.default.removeItem(at: destination)

    let input = try FileHandle(forReadingFrom: URL(fileURLWithPath: path))
    defer { try? input.close() }
    try input.seek(toOffset: UInt64(offset))

    guard FileManager.default.createFile(atPath: destination.path, contents: nil) else {
      throw Tus.Failure(summary: "could not stage the remainder of \(uploadId)")
    }
    let output = try FileHandle(forWritingTo: destination)
    defer { try? output.close() }

    let block = 4 * 1024 * 1024
    while true {
      let chunk = try input.read(upToCount: block) ?? Data()
      if chunk.isEmpty { break }
      try output.write(contentsOf: chunk)
    }
    try output.synchronize()

    return destination.path
  }

  // MARK: - Clients

  private func client(for pin: String) -> PinnedClient {
    lock.lock()
    defer { lock.unlock() }
    if let existing = clients[pin] { return existing }
    let client = PinnedClient(pin: pin)
    clients[pin] = client
    return client
  }

  private func describe(_ error: Error) -> String {
    (error as? LocalizedError)?.errorDescription ?? error.localizedDescription
  }

  // MARK: - Completion

  fileprivate func finish(
    uploadId: String, state: BackgroundJob.State, status: Int, headers: [String: String],
    error: String?
  ) {
    let updated = store.update(uploadId: uploadId) { job in
      job.state = state
      job.error = error
      if state == .done {
        job.bytesSent = max(job.size - job.offset, 0)
        job.storedPath = Tus.storedPath(from: headers)
        job.deduplicated = Tus.deduplicated(from: headers)
        // The bytes are on the receiver now. Keeping a second copy of every
        // photo in the app container is how a transfer app quietly fills a
        // phone.
        BackgroundStore.discardFiles(of: job)
        job.slicePath = nil
      }
      if status != 0 && state == .failed && error == nil {
        job.error = "the receiver answered \(status)"
      }
    }

    lock.lock()
    liveProgress.removeValue(forKey: uploadId)
    lastPersistAt.removeValue(forKey: uploadId)
    lastEventAt.removeValue(forKey: uploadId)
    let notify = onEvent
    lock.unlock()

    LiveActivity.refresh()

    if let updated {
      notify?(.finished(updated))
    }
  }
}

// MARK: - URLSessionDelegate

extension BackgroundUploader: URLSessionDataDelegate {

  /// The pin check, in the one place it can be made.
  ///
  /// Deliberately the *task*-level challenge rather than the session-level
  /// one. `URLSession` calls whichever is implemented, and only the task
  /// knows which upload it is, which is what lets each job be checked against
  /// the pin it was created with. A session-wide answer would have to guess
  /// from the address, and one stale job left over from a receiver that has
  /// since been re-paired would then refuse every upload to it.
  ///
  /// This runs for a background session too, including in a process the
  /// system launched by itself, which is exactly why the pin is on disk
  /// rather than in the JavaScript that owns it everywhere else. Refusing here
  /// is final: there is no override anywhere in this app (AGENTS.md §3.5).
  func urlSession(
    _ session: URLSession,
    task: URLSessionTask,
    didReceive challenge: URLAuthenticationChallenge,
    completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void
  ) {
    guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
      let trust = challenge.protectionSpace.serverTrust
    else {
      completionHandler(.performDefaultHandling, nil)
      return
    }

    guard let uploadId = task.taskDescription, let job = store.job(uploadId: uploadId),
      SPKIPin.trust(trust, matches: job.pin)
    else {
      completionHandler(.cancelAuthenticationChallenge, nil)
      return
    }
    completionHandler(.useCredential, URLCredential(trust: trust))
  }

  func urlSession(
    _ session: URLSession,
    task: URLSessionTask,
    didSendBodyData bytesSent: Int64,
    totalBytesSent: Int64,
    totalBytesExpectedToSend: Int64
  ) {
    guard let uploadId = task.taskDescription else { return }
    let now = Date.timeIntervalSinceReferenceDate

    lock.lock()
    liveProgress[uploadId] = totalBytesSent
    let persistDue = now - (lastPersistAt[uploadId] ?? 0) >= persistInterval
    if persistDue { lastPersistAt[uploadId] = now }
    let eventDue = now - (lastEventAt[uploadId] ?? 0) >= progressInterval
    if eventDue { lastEventAt[uploadId] = now }
    let notify = onEvent
    lock.unlock()

    if persistDue {
      // Rarely, and only so a relaunch has something to show before the first
      // callback arrives. The receiver's offset is what a resume is built on.
      store.update(uploadId: uploadId) { $0.bytesSent = totalBytesSent }
      // The same cadence suits ActivityKit, which rate-limits updates and
      // would drop a faster stream on the floor anyway.
      LiveActivity.refresh()
    }
    if eventDue {
      notify?(.progress(uploadId: uploadId, sent: totalBytesSent, total: totalBytesExpectedToSend))
    }
  }

  func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
    guard let uploadId = task.taskDescription else { return }
    let (status, headers) = Tus.normalize(task.response)

    if let error {
      let nsError = error as NSError
      let cancelled = nsError.domain == NSURLErrorDomain && nsError.code == NSURLErrorCancelled
      finish(
        uploadId: uploadId, state: .failed, status: status, headers: headers,
        error: cancelled ? "cancelled" : error.localizedDescription)
      return
    }

    if status == 204 || status == 200 {
      finish(uploadId: uploadId, state: .done, status: status, headers: headers, error: nil)
    } else {
      finish(
        uploadId: uploadId, state: .failed, status: status, headers: headers,
        error: "the receiver answered \(status)")
    }
  }

  /// Every completion queued while the app was away has now been delivered.
  func urlSessionDidFinishEvents(forBackgroundURLSession session: URLSession) {
    lock.lock()
    let handler = systemCompletion
    systemCompletion = nil
    lock.unlock()

    // Documented to be called on the main thread, and the system watches.
    DispatchQueue.main.async { handler?() }
  }
}
