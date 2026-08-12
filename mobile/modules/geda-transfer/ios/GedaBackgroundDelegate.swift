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

import BackgroundTasks
import ExpoModulesCore
import Foundation

/// The app's half of the contract with the system.
///
/// Both of the things here have to happen before, and independently of,
/// JavaScript:
///
///   * The system launches the app when a background upload finishes. It hands
///     over a completion handler and expects it back promptly; an app that
///     dawdles is killed, and one that is killed often enough stops being
///     woken at all. Waiting for a React runtime to start would be dawdling.
///   * A `BGProcessingTask` handler must be registered before
///     `didFinishLaunchingWithOptions` returns, which is earlier than any
///     module method can be called from JavaScript.
///
/// The processing task does not transfer anything itself (AGENTS.md §3.2). It
/// wakes up on power and Wi-Fi, hands whatever is staged back to the background
/// session, and returns.
public final class GedaBackgroundDelegate: ExpoAppDelegateSubscriber {

  public static let kickoffIdentifier = "app.geda.transfer.kickoff"

  /// Long enough that the system does not see the app as greedy, short enough
  /// that a phone put on charge overnight has finished by morning.
  private static let kickoffDelay: TimeInterval = 15 * 60

  public func application(
    _ application: UIApplication,
    didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
  ) -> Bool {
    BGTaskScheduler.shared.register(
      forTaskWithIdentifier: Self.kickoffIdentifier, using: nil
    ) { task in
      Self.runKickoff(task)
    }

    // A launch is also the moment to notice that something was interrupted:
    // the phone may have been off the network for a day, and the tasks the
    // system gave up on are only visible from here.
    Task { await BackgroundUploader.shared.reconcile() }

    return true
  }

  public func application(
    _ application: UIApplication,
    handleEventsForBackgroundURLSession identifier: String,
    completionHandler: @escaping () -> Void
  ) {
    guard identifier == BackgroundUploader.sessionIdentifier else {
      completionHandler()
      return
    }
    BackgroundUploader.shared.adopt(systemCompletion: completionHandler)
  }

  public func applicationDidEnterBackground(_ application: UIApplication) {
    Self.scheduleKickoff()
  }

  /// Asks the system to wake the app when the phone is charging and on Wi-Fi.
  ///
  /// `requiresExternalPower` is what makes this acceptable to run at all: the
  /// transfer costs radio and disk, and doing that to a phone in someone's
  /// pocket at 12% battery is the behaviour that gets an app deleted.
  public static func scheduleKickoff() {
    guard !BackgroundStore.shared.all().filter({ !$0.isFinished }).isEmpty else { return }

    let request = BGProcessingTaskRequest(identifier: kickoffIdentifier)
    request.requiresNetworkConnectivity = true
    request.requiresExternalPower = true
    request.earliestBeginDate = Date(timeIntervalSinceNow: kickoffDelay)

    do {
      try BGTaskScheduler.shared.submit(request)
    } catch {
      // Submission fails on a simulator, and when a request with this
      // identifier is already pending. Neither is worth reporting: the
      // transfer itself is the background session's business, and it is
      // already running.
      NSLog("geda: could not schedule the background kickoff: %@", String(describing: error))
    }
  }

  private static func runKickoff(_ task: BGTask) {
    // Installed before the work starts: the system may expire a task
    // immediately, and an expiration with no handler in place is a
    // termination rather than a tidy stop.
    let work = Task { () -> Void in
      await BackgroundUploader.shared.reconcile()
      await BackgroundUploader.shared.retryFailed()
      task.setTaskCompleted(success: true)
    }
    task.expirationHandler = {
      // The window closed. The uploads themselves are the system's now and
      // carry on regardless; only this bookkeeping stops.
      work.cancel()
    }

    // The next one is asked for immediately, because a task is only ever
    // scheduled once and there may still be work when this one ends.
    scheduleKickoff()
  }
}
