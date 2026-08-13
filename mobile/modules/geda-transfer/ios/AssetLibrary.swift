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
import Photos

/// Reading the parts of the photo library that the cross-platform layer cannot.
///
/// Listing assets is `expo-media-library`'s job and stays there. What it has no
/// concept of is everything P8 turns on: that one asset holds several
/// resources, that a photo is a screenshot or one frame of a burst, that a
/// still has edits with the original still underneath. All of that is
/// `PHAsset` and `PHAssetResource`, and it is here.
///
/// Nothing in this file decides anything. Which resources to send is
/// `src/core/selection.ts`, which is plain TypeScript and therefore testable
/// without a device; this only reports what exists and copies out what it is
/// asked for.
enum AssetLibrary {

  enum Failure: Error, LocalizedError {
    case notFound(String)
    case noSuchResource(String)
    case exportFailed(String, Error)

    var errorDescription: String? {
      switch self {
      case .notFound(let id):
        return "\(id) is not in the photo library any more."
      case .noSuchResource(let type):
        return "This photo has no \(type) to send."
      case .exportFailed(let name, let error):
        return "\(name) could not be read from the library: \(error.localizedDescription)"
      }
    }
  }

  // MARK: flags

  /// The per-asset facts that decide whether an asset is sent at all.
  ///
  /// Cheap on purpose: every field is a property `PHAsset` already holds in
  /// memory, so asking about ten thousand assets is one fetch and no I/O.
  ///
  /// `hasAdjustments` is the exception and is deliberately left false here.
  /// The only way to know it is to enumerate the asset's resources, which
  /// does touch the library -- doing that per asset while listing would undo
  /// the whole reason listing is fast (AGENTS.md §5). It is filled in at
  /// resolve time instead, from the resource list that step already fetches.
  static func flags(for identifiers: [String]) -> [[String: Any]] {
    guard !identifiers.isEmpty else { return [] }

    let assets = PHAsset.fetchAssets(withLocalIdentifiers: identifiers, options: nil)
    var out: [[String: Any]] = []
    out.reserveCapacity(assets.count)

    assets.enumerateObjects { asset, _, _ in
      out.append(flagsDictionary(for: asset))
    }
    return out
  }

  /// - Parameter resources: already-fetched resources, when the caller has
  ///   them. Passing nil leaves `hasAdjustments` false rather than paying for
  ///   a lookup; see `flags(for:)`.
  private static func flagsDictionary(
    for asset: PHAsset, resources: [PHAssetResource]? = nil
  ) -> [String: Any] {
    let selection = asset.burstSelectionTypes
    return [
      "id": asset.localIdentifier,
      "isScreenshot": asset.mediaSubtypes.contains(.photoScreenshot),
      "isHidden": asset.isHidden,
      "burstId": asset.burstIdentifier ?? "",
      "representsBurst": asset.representsBurst,
      // `.userPick` is a frame the user tapped to keep, `.autoPick` one iOS
      // thought was good. Only the user's choice counts as a keeper: autoPick
      // is set on a large share of a burst and would defeat the point.
      "userPickedFromBurst": selection.contains(.userPick),
      // `PHAsset` has no such property; the presence of an adjustment-data
      // resource is how edits are actually known.
      "hasAdjustments": resources?.contains { $0.type == .adjustmentData } ?? false,
    ]
  }

  // MARK: hidden assets

  /// Lists hidden assets, which an ordinary fetch does not return.
  ///
  /// This exists so that "send hidden photos" can be a setting that actually
  /// does something. It is off by default: somebody hid those deliberately,
  /// and a backup that quietly un-hides them onto a shared computer is its own
  /// kind of data loss.
  static func hiddenAssets(limit: Int) -> [[String: Any]] {
    let options = PHFetchOptions()
    options.includeHiddenAssets = true
    options.sortDescriptors = [NSSortDescriptor(key: "creationDate", ascending: false)]
    if limit > 0 { options.fetchLimit = limit }
    options.predicate = NSPredicate(format: "isHidden == YES")

    let assets = PHAsset.fetchAssets(with: options)
    var out: [[String: Any]] = []
    out.reserveCapacity(assets.count)

    assets.enumerateObjects { asset, _, _ in
      // Hidden assets do pay for a resource lookup, because there is no other
      // way to learn a filename for them -- but the Hidden album is tens of
      // photos where the library is tens of thousands, and this listing is
      // off by default.
      let resources = PHAssetResource.assetResources(for: asset)

      var entry = flagsDictionary(for: asset, resources: resources)
      entry["filename"] = primaryFilename(from: resources, fallback: asset.localIdentifier)
      entry["kind"] = asset.mediaType == .video ? "video" : "photo"
      // Milliseconds, matching what the listing layer produces.
      entry["capturedAt"] = (asset.creationDate?.timeIntervalSince1970).map { $0 * 1000 } ?? 0
      out.append(entry)
    }
    return out
  }

  private static func primaryFilename(
    from resources: [PHAssetResource], fallback: String
  ) -> String {
    let primary =
      resources.first { $0.type == .photo || $0.type == .video } ?? resources.first
    return primary?.originalFilename ?? fallback
  }

  // MARK: resources

  /// Every resource of one asset.
  ///
  /// The size is `PHAssetResource`'s private `fileSize` value, which is not
  /// API. It is read defensively and reported as 0 when absent: a size is a
  /// progress bar, and a progress bar is not worth failing a transfer over.
  static func resources(of identifier: String) throws -> [[String: Any]] {
    guard let asset = fetch(identifier) else { throw Failure.notFound(identifier) }

    return PHAssetResource.assetResources(for: asset).map { resource in
      [
        "type": name(of: resource.type),
        "filename": resource.originalFilename,
        "uti": resource.uniformTypeIdentifier,
        "size": size(of: resource),
      ]
    }
  }

  private static func size(of resource: PHAssetResource) -> Int64 {
    (resource.value(forKey: "fileSize") as? NSNumber)?.int64Value ?? 0
  }

  /// Copies one resource out of the library, byte for byte.
  ///
  /// `requestOptions.isNetworkAccessAllowed` is deliberately false. An asset
  /// that lives only in iCloud would otherwise be pulled down over cellular so
  /// it could be pushed back up over Wi-Fi, which is not a transfer anybody
  /// asked for and is expensive on a metered plan.
  static func export(
    identifier: String, type: String, to destination: String,
    completion: @escaping (Result<Int64, Error>) -> Void
  ) {
    guard let asset = fetch(identifier) else {
      completion(.failure(Failure.notFound(identifier)))
      return
    }
    guard
      let resource = PHAssetResource.assetResources(for: asset).first(where: {
        name(of: $0.type) == type
      })
    else {
      completion(.failure(Failure.noSuchResource(type)))
      return
    }

    let url = URL(fileURLWithPath: destination)
    // writeData refuses to overwrite, and a leftover from an abandoned run
    // would otherwise fail every retry from here on.
    try? FileManager.default.removeItem(at: url)
    try? FileManager.default.createDirectory(
      at: url.deletingLastPathComponent(), withIntermediateDirectories: true)

    let options = PHAssetResourceRequestOptions()
    options.isNetworkAccessAllowed = false

    PHAssetResourceManager.default().writeData(for: resource, toFile: url, options: options) {
      error in
      if let error {
        try? FileManager.default.removeItem(at: url)
        completion(.failure(Failure.exportFailed(resource.originalFilename, error)))
        return
      }
      let size =
        (try? FileManager.default.attributesOfItem(atPath: destination)[.size] as? NSNumber)?
        .int64Value ?? 0
      completion(.success(size))
    }
  }

  private static func fetch(_ identifier: String) -> PHAsset? {
    PHAsset.fetchAssets(withLocalIdentifiers: [identifier], options: nil).firstObject
  }

  /// The wire name for a resource type.
  ///
  /// Spelled out rather than derived from the raw value: these strings are the
  /// contract with `src/core/selection.ts`, and a future SDK renumbering the
  /// enum must not silently change which resource is a Live Photo's video.
  static func name(of type: PHAssetResourceType) -> String {
    switch type {
    case .photo: return "photo"
    case .fullSizePhoto: return "fullSizePhoto"
    case .alternatePhoto: return "alternatePhoto"
    case .video: return "video"
    case .fullSizeVideo: return "fullSizeVideo"
    case .pairedVideo: return "pairedVideo"
    case .fullSizePairedVideo: return "fullSizePairedVideo"
    case .audio: return "audio"
    // A placeholder for an asset still being written. It is never the thing
    // to send, and selection.ts has no rule that would pick it.
    case .photoProxy: return "photoProxy"
    case .adjustmentData: return "adjustmentData"
    case .adjustmentBasePhoto: return "adjustmentBasePhoto"
    case .adjustmentBaseVideo: return "adjustmentBaseVideo"
    case .adjustmentBasePairedVideo: return "adjustmentBasePairedVideo"
    @unknown default: return "unknown"
    }
  }
}
