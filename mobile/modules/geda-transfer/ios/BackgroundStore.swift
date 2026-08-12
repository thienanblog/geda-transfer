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

/// One file handed to the background session.
///
/// Every field here has to survive the app being killed, because that is the
/// case the whole phase exists for: `nsurlsessiond` keeps uploading after the
/// process is gone, and when the system relaunches the app to tell it a task
/// finished, this record is the only thing that says what that task *was*.
/// Nothing may be looked up in JavaScript at that moment -- there may not be a
/// JavaScript runtime at all.
struct BackgroundJob: Codable, Equatable {

  enum State: String, Codable {
    case pending
    case running
    case done
    case failed
  }

  var uploadId: String
  var receiverId: String
  /// Shown on the Lock Screen, which is drawn by a process that cannot ask
  /// JavaScript what the receiver is called.
  var receiverName: String
  var assetId: String
  var filename: String
  /// `https://host:port`, no trailing slash.
  var baseUrl: String
  /// The tus upload URL, created before the task was enqueued.
  var location: String
  /// base64(SHA-256(SubjectPublicKeyInfo)). The delegate needs it to accept
  /// the certificate, and it may run in a process JavaScript never started.
  var pin: String
  var token: String
  /// A copy of the asset inside the app container. The photo library's own
  /// files are not readable by `nsurlsessiond` (see DECISIONS).
  var stagedPath: String
  /// The remainder of `stagedPath` from `offset` on, written when an
  /// interrupted job is resumed. Absent while the whole file is being sent.
  var slicePath: String?
  /// How much of the asset the receiver already holds.
  var offset: Int64
  /// The asset's full size.
  var size: Int64
  var state: State
  /// Bytes of the *body* sent so far, which is `size - offset` at completion.
  var bytesSent: Int64
  var storedPath: String?
  var deduplicated: Bool
  var error: String?
  var updatedAt: Double
  /// How many times this job has been handed back to the system.
  ///
  /// A file that fails forever -- a photo the receiver rejects, a staged copy
  /// that went bad -- must eventually be given up on, or it is retried on
  /// every wake-up for the life of the install and its copy is never deleted.
  var attempts: Int
  /// The tus metadata this upload was created with, kept so that an upload
  /// the receiver has swept can be created again without the app having to
  /// remember anything.
  var metadata: [String: String]

  /// What the upload task actually reads from.
  var bodyPath: String { slicePath ?? stagedPath }

  /// Progress against the whole asset, not against this attempt.
  var totalSent: Int64 { min(offset + bytesSent, size) }

  var isFinished: Bool { state == .done || state == .failed }

  /// Retried on wake-ups until this many attempts have failed.
  static let maxAttempts = 5
}

/// The jobs, on disk.
///
/// A plain JSON file, rewritten atomically. It is read on a cold launch that
/// the system started only to deliver a completion, so it must not depend on
/// anything else in the app having been initialised -- no SQLite handle, no
/// React context, no keychain unlock. A file the size of a few hundred short
/// records is read in a millisecond and is the right tool.
final class BackgroundStore {

  static let shared = BackgroundStore()

  private let lock = NSLock()
  private let url: URL
  private var cache: [BackgroundJob]?

  init(url: URL? = nil) {
    if let url {
      self.url = url
    } else {
      let support = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)
        .first ?? URL(fileURLWithPath: NSTemporaryDirectory())
      let directory = support.appendingPathComponent("geda", isDirectory: true)
      try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
      self.url = directory.appendingPathComponent("background-jobs.json")
    }
  }

  /// Where staged copies live. Not `Caches`: the system may delete a cache
  /// under pressure, and it would be deleting the body of an upload in
  /// flight. Excluded from backup, because these are copies of photos that
  /// are already in the library.
  static func stagingDirectory() -> URL {
    let support = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)
      .first ?? URL(fileURLWithPath: NSTemporaryDirectory())
    var directory = support.appendingPathComponent("geda/staging", isDirectory: true)
    try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)

    var values = URLResourceValues()
    values.isExcludedFromBackup = true
    try? directory.setResourceValues(values)
    return directory
  }

  func all() -> [BackgroundJob] {
    lock.lock()
    defer { lock.unlock() }
    return load()
  }

  func job(uploadId: String) -> BackgroundJob? {
    lock.lock()
    defer { lock.unlock() }
    return load().first { $0.uploadId == uploadId }
  }

  func upsert(_ job: BackgroundJob) {
    lock.lock()
    defer { lock.unlock() }
    var jobs = load()
    if let index = jobs.firstIndex(where: { $0.uploadId == job.uploadId }) {
      jobs[index] = job
    } else {
      jobs.append(job)
    }
    save(jobs)
  }

  /// Mutates one job in place and returns what it became.
  @discardableResult
  func update(uploadId: String, _ body: (inout BackgroundJob) -> Void) -> BackgroundJob? {
    lock.lock()
    defer { lock.unlock() }
    var jobs = load()
    guard let index = jobs.firstIndex(where: { $0.uploadId == uploadId }) else { return nil }
    body(&jobs[index])
    jobs[index].updatedAt = Date().timeIntervalSince1970
    save(jobs)
    return jobs[index]
  }

  func remove(uploadIds: [String]) {
    guard !uploadIds.isEmpty else { return }
    let doomed = Set(uploadIds)
    lock.lock()
    defer { lock.unlock() }
    let jobs = load()
    for job in jobs where doomed.contains(job.uploadId) {
      Self.discardFiles(of: job)
    }
    save(jobs.filter { !doomed.contains($0.uploadId) })
  }

  func removeAll() {
    lock.lock()
    defer { lock.unlock() }
    for job in load() {
      Self.discardFiles(of: job)
    }
    save([])
  }

  /// Deletes the staged copy and any resume slice.
  ///
  /// Staged bytes are a duplicate of a photo the user already has; leaving
  /// them behind after a transfer would quietly eat the phone's storage, and
  /// a user who cannot see them cannot clean them up either.
  static func discardFiles(of job: BackgroundJob) {
    for path in [job.slicePath, job.stagedPath].compactMap({ $0 }) where !path.isEmpty {
      try? FileManager.default.removeItem(atPath: path)
    }
  }

  // MARK: - Disk

  private func load() -> [BackgroundJob] {
    if let cache { return cache }
    guard let data = try? Data(contentsOf: url),
      let jobs = try? JSONDecoder().decode([BackgroundJob].self, from: data)
    else {
      cache = []
      return []
    }
    cache = jobs
    return jobs
  }

  private func save(_ jobs: [BackgroundJob]) {
    cache = jobs
    guard let data = try? JSONEncoder().encode(jobs) else { return }
    // Atomic: a half-written record is a job whose staged file can never be
    // cleaned up and whose upload can never be resumed.
    try? data.write(to: url, options: .atomic)
  }
}
