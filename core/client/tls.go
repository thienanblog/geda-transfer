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

// Package client connects to a receiver: certificate pinning, candidate
// racing, and the handful of control requests a peer makes before it starts
// moving bytes.
//
// Trust here is a pinned SubjectPublicKeyInfo, recorded when the user scanned
// the receiver's QR code. There is no CA, and a mismatch is fatal with no
// override -- an override users can click is a CA that always says yes
// (docs/PROTOCOL.md §3.5).
package client

import (
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"

	"github.com/geda/geda-transfer/core/identity"
)

// ErrPinMismatch reports a certificate that is not the pinned key.
//
// Callers must not offer a way past this. The only recovery is scanning a
// fresh QR code, which requires physical presence at the receiver.
var ErrPinMismatch = errors.New("client: certificate does not match the pinned key")

// PinnedTLS builds a client configuration that trusts exactly one public key.
//
// InsecureSkipVerify is set deliberately. It disables the CA and hostname
// checks, which are meaningless here: the certificate is self-signed, and the
// receiver is reached by whichever of its addresses happens to work today.
// Verification is not skipped -- it is replaced by an equality check against
// the pinned SPKI, which is strictly stronger than a name matching a CA-issued
// certificate.
func PinnedTLS(pin string) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{"h2", "http/1.1"},
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return verifyPin(rawCerts, pin)
		},
	}
}

func verifyPin(rawCerts [][]byte, pin string) error {
	if pin == "" {
		return errors.New("client: no pin configured")
	}
	if len(rawCerts) == 0 {
		return fmt.Errorf("%w: peer sent no certificate", ErrPinMismatch)
	}

	cert, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPinMismatch, err)
	}

	got, err := identity.PinOf(cert)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPinMismatch, err)
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(pin)) != 1 {
		return fmt.Errorf("%w: expected %s, got %s",
			ErrPinMismatch, identity.Fingerprint(pin), identity.Fingerprint(got))
	}
	return nil
}
