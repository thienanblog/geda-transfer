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

// Prints the SPKI pin of a PEM certificate, using the same code the app uses
// to decide whether to trust a receiver.
//
// This exists so that the pin can be checked against the one the receiver
// itself reports. The two are computed by different implementations in
// different languages -- a DER walk in Swift, crypto/x509 in Go -- and if they
// ever disagree, every pairing fails with a mismatch and no override.
// scripts/verify-p4.sh runs it against a real gedad identity.
//
//   swiftc -O SPKIPin.swift checks/main.swift -o pincheck
//   ./pincheck /path/to/identity.crt

import Foundation
import Security

let arguments = CommandLine.arguments
guard arguments.count == 2 else {
  FileHandle.standardError.write(Data("usage: pincheck <certificate.pem>\n".utf8))
  exit(2)
}

guard let pem = try? String(contentsOfFile: arguments[1], encoding: .utf8) else {
  FileHandle.standardError.write(Data("cannot read \(arguments[1])\n".utf8))
  exit(1)
}

let base64 =
  pem
  .split(separator: "\n")
  .filter { !$0.hasPrefix("-----") }
  .joined()

guard let der = Data(base64Encoded: base64),
  let certificate = SecCertificateCreateWithData(nil, der as CFData),
  let pin = SPKIPin.of(certificate: certificate)
else {
  FileHandle.standardError.write(Data("not a usable certificate\n".utf8))
  exit(1)
}

print(pin)
