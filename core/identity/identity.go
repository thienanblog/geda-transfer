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

// Package identity manages the receiver's TLS key pair and self-signed
// certificate.
//
// There is no CA. A mobile client records SHA-256 of this key's
// SubjectPublicKeyInfo when the user scans the pairing QR code, and refuses
// any later certificate that does not match (docs/PROTOCOL.md §3.3).
//
// Two consequences shape this package:
//
//   - The key pair must outlive reinstalls. A new key means every paired
//     device sees a mismatch, which is a hard failure with no override, so the
//     key is stored outside the application bundle and never regenerated while
//     it is readable.
//   - The certificate may be reissued freely. Renewal reuses the key, so the
//     pin is unchanged and nobody has to re-pair.
package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	keyFile  = "identity.key"
	certFile = "identity.crt"

	// certLifetime is deliberately short of the ~13 months that Apple and
	// others enforce for public certificates. Nothing here is public, but
	// staying inside the common limit avoids surprises from platform TLS
	// stacks that apply the rule regardless.
	certLifetime = 390 * 24 * time.Hour

	// renewBefore is how much slack is left before expiry. Renewal reuses the
	// key, so it is invisible to paired devices.
	renewBefore = 30 * 24 * time.Hour
)

// Identity is a receiver's long-lived cryptographic identity.
type Identity struct {
	// Certificate is ready to hand to a TLS server.
	Certificate tls.Certificate

	// Pin is base64(SHA-256(SubjectPublicKeyInfo)), the value a client stores
	// at pairing time and checks on every later connection.
	Pin string

	dir string
}

// Load returns the identity stored in dir, creating one on first use and
// renewing the certificate when it is close to expiry.
//
// dir should be outside the application bundle -- a reinstall that loses the
// key makes every paired device fail to connect.
func Load(dir string) (*Identity, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("prepare identity directory: %w", err)
	}

	key, err := loadOrCreateKey(dir)
	if err != nil {
		return nil, err
	}

	cert, err := loadCert(dir, key)
	if err != nil {
		return nil, err
	}
	if cert == nil {
		if cert, err = issue(dir, key); err != nil {
			return nil, err
		}
	}

	pin, err := PinOf(cert.Leaf)
	if err != nil {
		return nil, err
	}

	return &Identity{Certificate: *cert, Pin: pin, dir: dir}, nil
}

// Fingerprint renders the pin for a human to compare across two screens, which
// is the fallback when a receiver has no display of its own.
func (id *Identity) Fingerprint() string { return Fingerprint(id.Pin) }

// Fingerprint formats a pin as four readable groups. Eight bytes is far more
// than enough to make a collision infeasible while still being something a
// person will actually read off a screen.
func Fingerprint(pin string) string {
	raw, err := base64.StdEncoding.DecodeString(pin)
	if err != nil || len(raw) < 8 {
		return pin
	}

	groups := make([]string, 4)
	for i := range groups {
		groups[i] = strings.ToUpper(hex.EncodeToString(raw[i*2 : i*2+2]))
	}
	return strings.Join(groups, " · ")
}

// PinOf computes the SPKI pin of a certificate.
//
// The pin covers the public key only, not the certificate: reissuing from the
// same key produces a different certificate with an identical pin, so renewal
// never forces anyone to re-pair. Pinning the whole certificate would break on
// every renewal.
func PinOf(cert *x509.Certificate) (string, error) {
	if cert == nil {
		return "", errors.New("no certificate")
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:]), nil
}

// TLSConfig returns a server configuration for this identity.
func (id *Identity) TLSConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{id.Certificate},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"h2", "http/1.1"},
	}
}

func keyPath(dir string) string  { return filepath.Join(dir, keyFile) }
func certPath(dir string) string { return filepath.Join(dir, certFile) }

func loadOrCreateKey(dir string) (*ecdsa.PrivateKey, error) {
	raw, err := os.ReadFile(keyPath(dir))
	switch {
	case err == nil:
		block, _ := pem.Decode(raw)
		if block == nil || block.Type != "EC PRIVATE KEY" {
			// Refusing beats generating a replacement: a new key would look
			// exactly like an impersonation attempt to every paired device.
			return nil, fmt.Errorf("identity key at %s is corrupt; restore it from backup or re-pair every device", keyPath(dir))
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse identity key: %w", err)
		}
		return key, nil

	case os.IsNotExist(err):
		return createKey(dir)

	default:
		return nil, fmt.Errorf("read identity key: %w", err)
	}
}

func createKey(dir string) (*ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate identity key: %w", err)
	}

	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal identity key: %w", err)
	}

	encoded := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := writeFileExclusive(keyPath(dir), encoded, 0o600); err != nil {
		if errors.Is(err, os.ErrExist) {
			// Another process created it first. Its key is as good as ours.
			return loadOrCreateKey(dir)
		}
		return nil, fmt.Errorf("write identity key: %w", err)
	}
	return key, nil
}

// loadCert returns the stored certificate, or nil if it is absent, unreadable,
// expiring, or no longer matches the key.
func loadCert(dir string, key *ecdsa.PrivateKey) (*tls.Certificate, error) {
	raw, err := os.ReadFile(certPath(dir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read certificate: %w", err)
	}

	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, nil // reissue below
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil
	}

	if time.Now().Add(renewBefore).After(leaf.NotAfter) {
		return nil, nil
	}

	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		// The certificate belongs to a different key; reissuing from ours
		// keeps the pin that paired devices already trust.
		return nil, nil
	}

	return &tls.Certificate{
		Certificate: [][]byte{leaf.Raw},
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}

func issue(dir string, key *ecdsa.PrivateKey) (*tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}

	host, _ := os.Hostname()
	if host == "" {
		host = "geda"
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host, Organization: []string{"Geda Transfer"}},
		NotBefore:             now.Add(-time.Hour), // tolerate a little clock skew
		NotAfter:              now.Add(certLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{host, "localhost"},
		IPAddresses:           localAddresses(),
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}

	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath(dir), encoded, 0o600); err != nil {
		return nil, fmt.Errorf("write certificate: %w", err)
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse issued certificate: %w", err)
	}

	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// localAddresses collects the addresses to name in the certificate. Clients
// verify by pin rather than by name, so this is a convenience for tools like
// curl, not a security control.
func localAddresses() []net.IP {
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			ips = append(ips, ipnet.IP)
		}
	}
	return ips
}

// writeFileExclusive makes path appear, complete, in one step, and only if it
// does not already exist.
//
// Creating the file and then writing to it would not do. Another process
// starting at the same moment can open the file in the window between those
// two operations, read zero bytes, and conclude the identity key is corrupt --
// which this package deliberately treats as unrecoverable. The content is
// therefore written to a temporary file and linked into place, since a link
// both publishes the file atomically and fails if the name is taken.
func writeFileExclusive(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".identity-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := writeAndClose(tmp, data, perm); err != nil {
		return err
	}

	// Link, not rename: rename would silently replace a key another process
	// just wrote, and every device paired with that key would break.
	if err := os.Link(tmpName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return os.ErrExist
		}
		return err
	}
	return nil
}

func writeAndClose(f *os.File, data []byte, perm os.FileMode) error {
	defer f.Close()

	if err := f.Chmod(perm); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}
