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

/// What the Lock Screen shows while a background transfer runs.
///
/// This file is compiled into *both* the app and the widget extension. They
/// are separate processes with separate binaries and no shared framework, and
/// ActivityKit pairs them up by the type's name -- so the declaration has to be
/// identical on both sides, which is best guaranteed by it being one file.
struct GedaTransferAttributes: ActivityAttributes {

  /// The parts that change as the transfer proceeds.
  ///
  /// Deliberately small. Every update crosses to a system process and is rate
  /// limited; sending a filename per file would spend the budget on text
  /// nobody reads and leave the progress bar stale.
  public struct ContentState: Codable, Hashable {
    var filesDone: Int
    var filesTotal: Int
    var bytesSent: Int64
    var bytesTotal: Int64
    /// Seconds remaining, when there is enough history to say.
    var eta: Double?
    var finished: Bool
    var failed: Int

    var fraction: Double {
      guard bytesTotal > 0 else { return finished ? 1 : 0 }
      return min(max(Double(bytesSent) / Double(bytesTotal), 0), 1)
    }
  }

  /// Fixed for the life of the activity.
  var receiverName: String
}
