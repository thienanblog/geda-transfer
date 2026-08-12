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
import Security

/// Trust-on-first-use pinning: the receiver has a self-signed certificate and
/// there is no CA anywhere in this system, so the pin recorded when the user
/// scanned the QR code *is* the trust relationship (docs/PROTOCOL.md §3.3).
///
/// The pin covers the SubjectPublicKeyInfo, not the certificate. Reissuing
/// from the same key pair produces a different certificate with an identical
/// pin, so the receiver can renew its certificate without every phone having
/// to pair again -- which pinning the whole certificate would force.
enum SPKIPin {

  /// Computes base64(SHA-256(SubjectPublicKeyInfo)) for a certificate.
  ///
  /// The SPKI is taken from the certificate's DER rather than rebuilt from the
  /// public key. `SecKeyCopyExternalRepresentation` hands back the bare key --
  /// for an EC key just `04 || X || Y` -- and reconstructing the algorithm
  /// header around it means hardcoding one prefix per key type and silently
  /// computing the wrong digest for anything else. The bytes in the
  /// certificate are the same bytes the receiver hashed.
  static func of(certificate: SecCertificate) -> String? {
    let der = SecCertificateCopyData(certificate) as Data
    guard let spki = subjectPublicKeyInfo(inCertificate: der) else {
      return nil
    }
    return Data(SHA256.hash(data: spki)).base64EncodedString()
  }

  /// Verifies a server trust against a pin, ignoring the CA chain entirely.
  ///
  /// The certificate is self-signed and the receiver is reached at whichever
  /// of its addresses works today, so neither a chain check nor a hostname
  /// check would prove anything here. Equality with the pinned key is
  /// strictly stronger (docs/DECISIONS.md).
  static func trust(_ trust: SecTrust, matches pin: String) -> Bool {
    guard !pin.isEmpty else { return false }
    guard let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate],
      let leaf = chain.first
    else {
      return false
    }
    guard let served = of(certificate: leaf) else { return false }

    // Constant-time comparison. The pin is not a secret, but comparing it
    // this way costs nothing and removes the question.
    let a = Array(served.utf8)
    let b = Array(pin.utf8)
    guard a.count == b.count else { return false }
    var difference: UInt8 = 0
    for i in 0..<a.count {
      difference |= a[i] ^ b[i]
    }
    return difference == 0
  }

  // MARK: - Minimal DER walking

  /// One ASN.1 element: where its contents start, how long they are, and
  /// where the next element begins.
  private struct Element {
    let tag: UInt8
    let range: Range<Int>  // the whole element, header included
    let contents: Range<Int>
  }

  /// Extracts the SubjectPublicKeyInfo from an X.509 certificate.
  ///
  ///     Certificate ::= SEQUENCE {
  ///       tbsCertificate ::= SEQUENCE {
  ///         [0] version, serialNumber, signature, issuer,
  ///         validity, subject, subjectPublicKeyInfo, ...
  ///       }, ...
  ///     }
  ///
  /// The version tag is optional, which is the only branch here: with it the
  /// SPKI is the seventh element of the TBS certificate, without it the sixth.
  private static func subjectPublicKeyInfo(inCertificate der: Data) -> Data? {
    let bytes = [UInt8](der)

    guard let certificate = element(in: bytes, at: bytes.startIndex),
      certificate.tag == 0x30,
      let tbs = element(in: bytes, at: certificate.contents.lowerBound),
      tbs.tag == 0x30
    else {
      return nil
    }

    var index = tbs.contents.lowerBound
    var fields: [Element] = []
    while index < tbs.contents.upperBound, fields.count < 8 {
      guard let field = element(in: bytes, at: index) else { return nil }
      fields.append(field)
      index = field.range.upperBound
    }

    // A context-specific constructed [0] in first position is the explicit
    // version tag; everything after it shifts by one.
    let spkiIndex = (fields.first?.tag == 0xA0) ? 6 : 5
    guard fields.count > spkiIndex else { return nil }

    let spki = fields[spkiIndex]
    guard spki.tag == 0x30 else { return nil }
    return der.subdata(in: spki.range)
  }

  /// Reads one tag-length-value at `start`.
  private static func element(in bytes: [UInt8], at start: Int) -> Element? {
    guard start + 1 < bytes.count else { return nil }

    let tag = bytes[start]
    var cursor = start + 1
    let first = bytes[cursor]
    cursor += 1

    var length = 0
    if first & 0x80 == 0 {
      length = Int(first)
    } else {
      // Long form: the low bits say how many bytes carry the length. More
      // than four would be a certificate over 4 GB, which is not a thing.
      let count = Int(first & 0x7F)
      guard count > 0, count <= 4, cursor + count <= bytes.count else { return nil }
      for _ in 0..<count {
        length = (length << 8) | Int(bytes[cursor])
        cursor += 1
      }
    }

    let end = cursor + length
    guard length >= 0, end <= bytes.count else { return nil }
    return Element(tag: tag, range: start..<end, contents: cursor..<end)
  }
}
