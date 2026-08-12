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

package identity_test

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/geda/geda-transfer/core/identity"
)

func TestLoadCreatesAndPersists(t *testing.T) {
	dir := t.TempDir()

	first, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.Pin == "" {
		t.Fatal("no pin")
	}

	for _, name := range []string{"identity.key", "identity.crt"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s has mode %o, want 600", name, perm)
		}
	}

	second, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second.Pin != first.Pin {
		t.Errorf("pin changed across loads: %q then %q", first.Pin, second.Pin)
	}
}

// The pin covers the public key only. Reissuing the certificate from the same
// key must leave it unchanged, or every paired device would see a mismatch.
func TestPinSurvivesCertificateReissue(t *testing.T) {
	dir := t.TempDir()

	first, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	firstSerial := first.Certificate.Leaf.SerialNumber.String()

	// Drop the certificate but keep the key, which is what renewal does.
	if err := os.Remove(filepath.Join(dir, "identity.crt")); err != nil {
		t.Fatal(err)
	}

	second, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	if second.Certificate.Leaf.SerialNumber.String() == firstSerial {
		t.Fatal("certificate was not actually reissued")
	}
	if second.Pin != first.Pin {
		t.Errorf("reissue changed the pin: %q then %q", first.Pin, second.Pin)
	}
}

// A corrupt key must not be silently replaced: a fresh key is indistinguishable
// from an impersonation attempt to every device that already paired.
func TestCorruptKeyIsRefusedRatherThanReplaced(t *testing.T) {
	dir := t.TempDir()

	first, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "identity.key"), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}

	second, err := identity.Load(dir)
	if err == nil {
		if second.Pin != first.Pin {
			t.Fatal("silently generated a new identity, which breaks every paired device")
		}
		return
	}
	if !strings.Contains(err.Error(), "re-pair") {
		t.Errorf("error should tell the user what to do, got: %v", err)
	}
}

func TestPinMatchesSPKIDigest(t *testing.T) {
	id, err := identity.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(id.Certificate.Leaf.RawSubjectPublicKeyInfo)
	want := base64.StdEncoding.EncodeToString(sum[:])

	if id.Pin != want {
		t.Errorf("Pin = %q, want %q", id.Pin, want)
	}
}

func TestFingerprintIsReadable(t *testing.T) {
	id, err := identity.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	fp := id.Fingerprint()
	groups := strings.Split(fp, " · ")
	if len(groups) != 4 {
		t.Fatalf("Fingerprint() = %q, want four groups", fp)
	}
	for _, g := range groups {
		if len(g) != 4 {
			t.Errorf("group %q is not four hex digits", g)
		}
		if g != strings.ToUpper(g) {
			t.Errorf("group %q is not upper case", g)
		}
	}
}

func TestFingerprintToleratesGarbage(t *testing.T) {
	if got := identity.Fingerprint("not base64"); got != "not base64" {
		t.Errorf("got %q, want the input back unchanged", got)
	}
}

func TestTLSConfigRequiresTLS13(t *testing.T) {
	id, err := identity.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	cfg := id.TLSConfig()
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %x, want TLS 1.3", cfg.MinVersion)
	}
	// HTTP/2 is what makes many small files cheap, and iOS background
	// URLSession negotiates it by ALPN.
	if len(cfg.NextProtos) == 0 || cfg.NextProtos[0] != "h2" {
		t.Errorf("NextProtos = %v, want h2 first", cfg.NextProtos)
	}
}

func TestCertificateIsUsableForServerAuth(t *testing.T) {
	id, err := identity.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	leaf := id.Certificate.Leaf
	var serverAuth bool
	for _, u := range leaf.ExtKeyUsage {
		if u == x509.ExtKeyUsageServerAuth {
			serverAuth = true
		}
	}
	if !serverAuth {
		t.Error("certificate cannot be used for server authentication")
	}
	if !leaf.NotBefore.Before(leaf.NotAfter) {
		t.Error("certificate validity window is inverted")
	}
}

// Two processes sharing a directory must converge on one identity rather than
// each writing its own key.
func TestConcurrentLoadConvergesOnOneIdentity(t *testing.T) {
	dir := t.TempDir()

	const n = 8
	var wg sync.WaitGroup
	pins := make([]string, n)
	errs := make([]error, n)

	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := identity.Load(dir)
			if err != nil {
				errs[i] = err
				return
			}
			pins[i] = id.Pin
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("load %d: %v", i, err)
		}
	}
	for i := range pins {
		if pins[i] != pins[0] {
			t.Fatalf("loaders disagree on identity: %q and %q", pins[0], pins[i])
		}
	}
}

func TestPinOfRejectsNil(t *testing.T) {
	if _, err := identity.PinOf(nil); err == nil {
		t.Fatal("expected an error")
	}
}
