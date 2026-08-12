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
import Foundation

/// Driving the Lock Screen activity from whatever process happens to be alive.
///
/// The state is derived from `BackgroundStore` rather than passed in, and that
/// is the whole trick: the app is usually not running while a background
/// transfer proceeds, and the process the system launches to deliver a
/// completion has no JavaScript, no React, and no memory of what was being
/// sent. It does have the store, so it can work out what to display and end
/// the activity when the last file lands.
///
/// Without a push token an activity can only be updated by the app, so it goes
/// stale between wake-ups. `staleDate` is what makes that honest: the system
/// dims the activity rather than presenting a number that stopped being true
/// an hour ago (see DECISIONS).
enum LiveActivity {

  /// How long a displayed figure is worth believing.
  private static let staleAfter: TimeInterval = 20 * 60

  /// Left up briefly after the last file so the person who swiped the app
  /// away can see that it finished.
  private static let lingerAfterFinish: TimeInterval = 60

  private static let lock = NSLock()
  private static var startedAt: Date?

  /// Notes when this batch began, for the estimate.
  ///
  /// Nothing else is remembered in memory. The process that ends the activity
  /// is usually not the process that started it -- the system launches the
  /// app to deliver a completion, and that launch has no idea a transfer was
  /// ever running -- so anything `refresh` needs is either on disk or on the
  /// activity itself.
  static func enable() {
    lock.lock()
    if startedAt == nil { startedAt = Date() }
    lock.unlock()
  }

  static func disable() {
    markStopped()
    Task { await endAll(final: nil) }
  }

  /// Taking the lock has to happen in a synchronous function: holding one
  /// across an `await` is a deadlock waiting for the right scheduling.
  private static func markStopped() {
    lock.lock()
    startedAt = nil
    lock.unlock()
  }

  /// Recomputes the activity from what is on disk, and starts, updates, or
  /// ends it accordingly.
  ///
  /// An activity is only *started* when there is unfinished work, so a
  /// relaunch that has nothing to say puts nothing on the Lock Screen; an
  /// existing one is always updated and ended, whichever process is running.
  static func refresh(store: BackgroundStore = .shared) {
    lock.lock()
    let since = startedAt
    lock.unlock()

    let jobs = store.all()
    guard !jobs.isEmpty else {
      Task { await endAll(final: nil) }
      return
    }

    let state = self.state(for: jobs, since: since)
    let name = jobs.first?.receiverName ?? "your computer"

    Task {
      if let activity = Activity<GedaTransferAttributes>.activities.first {
        await activity.update(content(state), alertConfiguration: nil)
        if state.finished {
          await activity.end(
            content(state), dismissalPolicy: .after(Date(timeIntervalSinceNow: lingerAfterFinish)))
          markStopped()
        }
        return
      }

      guard !state.finished, ActivityAuthorizationInfo().areActivitiesEnabled else { return }
      _ = try? Activity.request(
        attributes: GedaTransferAttributes(receiverName: name),
        content: content(state),
        pushType: nil
      )
    }
  }

  static func state(for jobs: [BackgroundJob], since: Date?) -> GedaTransferAttributes.ContentState {
    let bytesSent = jobs.reduce(Int64(0)) { $0 + $1.totalSent }
    let bytesTotal = jobs.reduce(Int64(0)) { $0 + $1.size }
    let done = jobs.filter { $0.state == .done }.count
    let failed = jobs.filter { $0.state == .failed }.count

    var eta: Double?
    if let since {
      let elapsed = Date().timeIntervalSince(since)
      let remaining = bytesTotal - bytesSent
      if elapsed > 5, bytesSent > 0, remaining > 0 {
        eta = Double(remaining) / (Double(bytesSent) / elapsed)
      }
    }

    return GedaTransferAttributes.ContentState(
      filesDone: done,
      filesTotal: jobs.count,
      bytesSent: bytesSent,
      bytesTotal: bytesTotal,
      eta: eta,
      finished: jobs.allSatisfy(\.isFinished),
      failed: failed
    )
  }

  private static func content(_ state: GedaTransferAttributes.ContentState)
    -> ActivityContent<GedaTransferAttributes.ContentState>
  {
    ActivityContent(state: state, staleDate: Date(timeIntervalSinceNow: staleAfter))
  }

  private static func endAll(final: GedaTransferAttributes.ContentState?) async {
    for activity in Activity<GedaTransferAttributes>.activities {
      await activity.end(final.map { content($0) }, dismissalPolicy: .immediate)
    }
  }
}
