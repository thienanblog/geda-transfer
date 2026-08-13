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

/// One file being collected from a receiver.
///
/// The same constraint as `BackgroundJob`: every field has to survive the app
/// being killed, because the system finishes the download in `nsurlsessiond`
/// and then relaunches the app to say so. At that moment there may be no
/// JavaScript runtime, so this record is the only thing that says what the
/// finished task *was*.
struct DownloadJob: Codable, Equatable {

  enum State: String, Codable {
    case pending
    case running
    /// The bytes are on disk and unverified. Saving needs the app -- a photo
    /// library cannot be written from a background session -- so this is
    /// where a download waits for somebody to open it.
    case ready
    case failed
  }

  /// The receiver's own identifier for the item, and the key everything is
  /// scoped by. It is what the acknowledgement quotes.
  var itemId: String
  var receiverId: String
  /// Shown in a notification-free UI, but also in an error the app may raise
  /// long after the receiver's name would otherwise be available.
  var receiverName: String
  /// The name on the sending computer. Untrusted: sanitised before it becomes
  /// a path, on the JavaScript side where that rule is tested.
  var filename: String
  /// photo, video, or file. Decides whether the Photo Library is even a
  /// candidate (AGENTS.md §3.7).
  var kind: String
  /// RFC3339, or empty. Becomes the asset's creation date where it can.
  var capturedAt: String
  /// `https://host:port`, no trailing slash.
  var baseUrl: String
  /// The path on the receiver, always under /v1/outbox.
  var path: String
  /// base64(SHA-256(SubjectPublicKeyInfo)). The delegate needs it to accept
  /// the certificate, in a process JavaScript never started.
  var pin: String
  var token: String
  /// Hex SHA-256 of the whole file, from the receiver's listing. Nothing is
  /// saved until the downloaded bytes reproduce it.
  var sha256: String
  var size: Int64
  var bytesReceived: Int64
  var state: State
  /// Where the finished download is waiting, inside the app container.
  var stagedPath: String?
  /// URLSession's own resume token, written out when a task is interrupted.
  /// It is what turns a dropped connection on a 2 GB archive into a Range
  /// request rather than starting again.
  var resumeDataPath: String?
  var error: String?
  var updatedAt: Double
  /// How many times this download has been handed back to the system. A file
  /// that fails forever must eventually be given up on, or it is retried on
  /// every wake-up for the life of the install.
  var attempts: Int

  var isFinished: Bool { state == .ready || state == .failed }

  static let maxAttempts = 5
}

/// The downloads, on disk.
///
/// Deliberately not the same file as `BackgroundStore`. The two directions
/// have different records and different cleanup rules -- an uploaded file's
/// staged copy is deleted, a downloaded one is moved into the photo library --
/// and Swift will not let a generic type hold the `static let shared` that
/// both of them need. A hundred lines of duplication is the cheaper of the two
/// mistakes available here.
final class DownloadStore {

  static let shared = DownloadStore()

  private let lock = NSLock()
  private let url: URL
  private var cache: [DownloadJob]?

  init(url: URL? = nil) {
    if let url {
      self.url = url
    } else {
      let support = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)
        .first ?? URL(fileURLWithPath: NSTemporaryDirectory())
      let directory = support.appendingPathComponent("geda", isDirectory: true)
      try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
      self.url = directory.appendingPathComponent("download-jobs.json")
    }
  }

  /// Where finished downloads wait to be verified and saved.
  ///
  /// Not `Caches`: the system may delete a cache under pressure, and it would
  /// be deleting a file the user is waiting for. Excluded from backup, because
  /// these are transient and can be collected again.
  static func stagingDirectory() -> URL {
    let support = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)
      .first ?? URL(fileURLWithPath: NSTemporaryDirectory())
    var directory = support.appendingPathComponent("geda/incoming", isDirectory: true)
    try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)

    var values = URLResourceValues()
    values.isExcludedFromBackup = true
    try? directory.setResourceValues(values)
    return directory
  }

  /// Where the app's Files container keeps what it has been sent.
  ///
  /// Visible in the Files app, and backed up: these are the user's documents,
  /// which is the whole reason they are not in the photo library.
  static func documentsDirectory() -> URL {
    let documents = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)
      .first ?? URL(fileURLWithPath: NSTemporaryDirectory())
    let directory = documents.appendingPathComponent("Received", isDirectory: true)
    try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    return directory
  }

  func all() -> [DownloadJob] {
    lock.lock()
    defer { lock.unlock() }
    return load()
  }

  func job(itemId: String) -> DownloadJob? {
    lock.lock()
    defer { lock.unlock() }
    return load().first { $0.itemId == itemId }
  }

  func upsert(_ job: DownloadJob) {
    lock.lock()
    defer { lock.unlock() }
    var jobs = load()
    if let index = jobs.firstIndex(where: { $0.itemId == job.itemId }) {
      jobs[index] = job
    } else {
      jobs.append(job)
    }
    save(jobs)
  }

  @discardableResult
  func update(itemId: String, _ body: (inout DownloadJob) -> Void) -> DownloadJob? {
    lock.lock()
    defer { lock.unlock() }
    var jobs = load()
    guard let index = jobs.firstIndex(where: { $0.itemId == itemId }) else { return nil }
    body(&jobs[index])
    jobs[index].updatedAt = Date().timeIntervalSince1970
    save(jobs)
    return jobs[index]
  }

  func remove(itemIds: [String]) {
    guard !itemIds.isEmpty else { return }
    let doomed = Set(itemIds)
    lock.lock()
    defer { lock.unlock() }
    let jobs = load()
    for job in jobs where doomed.contains(job.itemId) {
      Self.discardFiles(of: job)
    }
    save(jobs.filter { !doomed.contains($0.itemId) })
  }

  func removeAll() {
    lock.lock()
    defer { lock.unlock() }
    for job in load() {
      Self.discardFiles(of: job)
    }
    save([])
  }

  /// Deletes whatever a job left in the container.
  ///
  /// A staged download is a second copy of something the user is about to have
  /// in their photo library; leaving it behind would quietly eat the phone's
  /// storage where nobody can see it to clean it up.
  static func discardFiles(of job: DownloadJob) {
    for path in [job.stagedPath, job.resumeDataPath].compactMap({ $0 }) where !path.isEmpty {
      try? FileManager.default.removeItem(atPath: path)
    }
  }

  // MARK: - Disk

  private func load() -> [DownloadJob] {
    if let cache { return cache }
    guard let data = try? Data(contentsOf: url),
      let jobs = try? JSONDecoder().decode([DownloadJob].self, from: data)
    else {
      cache = []
      return []
    }
    cache = jobs
    return jobs
  }

  private func save(_ jobs: [DownloadJob]) {
    cache = jobs
    guard let data = try? JSONEncoder().encode(jobs) else { return }
    // Atomic: a half-written record is a download whose staged file can never
    // be found again and whose bytes can never be resumed.
    try? data.write(to: url, options: .atomic)
  }
}
