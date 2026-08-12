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
import SwiftUI
import WidgetKit

/// The Lock Screen and Dynamic Island faces of a background transfer.
///
/// A background upload is deliberately invisible: the app is not running, the
/// system is sending the files when it feels like it, and there is nothing to
/// tap. That is also its worst property -- a person who swiped the app away
/// mid-transfer has no way of knowing whether their photos are still going.
/// This is the answer, and it is why P5 lists it alongside the session itself.
@main
struct GedaWidgetBundle: WidgetBundle {
  var body: some Widget {
    GedaTransferLiveActivity()
  }
}

struct GedaTransferLiveActivity: Widget {
  var body: some WidgetConfiguration {
    ActivityConfiguration(for: GedaTransferAttributes.self) { context in
      LockScreenView(context: context)
        .activityBackgroundTint(Color.black.opacity(0.65))
        .activitySystemActionForegroundColor(.white)
    } dynamicIsland: { context in
      DynamicIsland {
        DynamicIslandExpandedRegion(.leading) {
          Label(context.attributes.receiverName, systemImage: "externaldrive.connected.to.line.below")
            .font(.caption)
            .lineLimit(1)
        }
        DynamicIslandExpandedRegion(.trailing) {
          Text(counts(context.state))
            .font(.caption.monospacedDigit())
        }
        DynamicIslandExpandedRegion(.bottom) {
          VStack(alignment: .leading, spacing: 4) {
            ProgressView(value: context.state.fraction)
              .tint(.green)
            Text(subtitle(context.state))
              .font(.caption2)
              .foregroundStyle(.secondary)
          }
        }
      } compactLeading: {
        Image(systemName: "arrow.up.circle")
      } compactTrailing: {
        Text(percent(context.state))
          .font(.caption2.monospacedDigit())
      } minimal: {
        Image(systemName: "arrow.up.circle")
      }
    }
  }
}

private struct LockScreenView: View {
  let context: ActivityViewContext<GedaTransferAttributes>

  var body: some View {
    VStack(alignment: .leading, spacing: 8) {
      HStack {
        Text(context.state.finished ? "Sent to \(context.attributes.receiverName)"
                                    : "Sending to \(context.attributes.receiverName)")
          .font(.headline)
        Spacer()
        Text(counts(context.state))
          .font(.subheadline.monospacedDigit())
          .foregroundStyle(.secondary)
      }

      ProgressView(value: context.state.fraction)
        .tint(context.state.failed > 0 ? .orange : .green)

      Text(subtitle(context.state))
        .font(.caption)
        .foregroundStyle(.secondary)
    }
    .padding()
  }
}

private func counts(_ state: GedaTransferAttributes.ContentState) -> String {
  "\(state.filesDone)/\(state.filesTotal)"
}

private func percent(_ state: GedaTransferAttributes.ContentState) -> String {
  "\(Int(state.fraction * 100))%"
}

/// One line that says the true thing.
///
/// While the app is closed the system decides when to spend the radio, so a
/// countdown that keeps stalling would be a lie told every few seconds. When
/// there is no estimate, the text says what is actually happening instead.
private func subtitle(_ state: GedaTransferAttributes.ContentState) -> String {
  if state.finished {
    let failed = state.failed > 0 ? " · \(state.failed) failed" : ""
    return "\(bytes(state.bytesTotal)) transferred\(failed)"
  }
  let sent = "\(bytes(state.bytesSent)) of \(bytes(state.bytesTotal))"
  guard let eta = state.eta, eta.isFinite, eta > 0 else {
    return "\(sent) · continues in the background"
  }
  return "\(sent) · about \(duration(eta)) left"
}

private func bytes(_ value: Int64) -> String {
  let formatter = ByteCountFormatter()
  formatter.countStyle = .file
  return formatter.string(fromByteCount: value)
}

private func duration(_ seconds: Double) -> String {
  if seconds < 60 { return "\(Int(seconds))s" }
  if seconds < 3600 { return "\(Int(seconds / 60))m" }
  return String(format: "%.1fh", seconds / 3600)
}
