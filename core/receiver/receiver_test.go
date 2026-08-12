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

package receiver_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/geda/geda-transfer/core/hash"
	"github.com/geda/geda-transfer/core/identity"
	"github.com/geda/geda-transfer/core/receiver"
	"github.com/geda/geda-transfer/core/storage"
	"github.com/geda/geda-transfer/core/store"
)

const testToken = "test-device-token"

type harness struct {
	*httptest.Server
	db    *store.DB
	files *storage.Store
	root  string
	t     *testing.T
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	dir := t.TempDir()

	db, err := store.Open(t.Context(), filepath.Join(dir, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	root := filepath.Join(dir, "Photos")
	files, err := storage.New(db, root)
	if err != nil {
		t.Fatal(err)
	}

	id, err := identity.Load(filepath.Join(dir, "identity"))
	if err != nil {
		t.Fatal(err)
	}

	srv, err := receiver.New(receiver.Config{
		DeviceID: "receiver-1",
		Name:     "Studio Mac",
		DB:       db,
		Files:    files,
		Identity: id,
		Logger:   slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}

	h := &harness{Server: httptest.NewServer(srv.Handler()), db: db, files: files, root: root, t: t}
	t.Cleanup(h.Close)

	h.addDevice("dev-1", "An's iPhone", testToken)
	return h
}

func (h *harness) addDevice(id, name, token string) {
	h.t.Helper()
	_, err := h.db.SQL().ExecContext(h.t.Context(), `
		INSERT INTO devices (id, name, platform, spki_pin, token_hash, paired_at)
		VALUES (?, ?, 'ios', 'pin', ?, ?)`,
		id, name, receiver.HashToken(token), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) do(req *http.Request) *http.Response {
	h.t.Helper()
	resp, err := h.Client().Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	return resp
}

func meta(pairs map[string]string) string {
	parts := make([]string, 0, len(pairs))
	for k, v := range pairs {
		parts = append(parts, k+" "+base64.StdEncoding.EncodeToString([]byte(v)))
	}
	return strings.Join(parts, ",")
}

// create starts a tus upload and returns its location.
func (h *harness) create(size int, metadata map[string]string) string {
	h.t.Helper()

	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodPost, h.URL+receiver.UploadPath, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", strconv.Itoa(size))
	req.Header.Set("Upload-Metadata", meta(metadata))

	resp := h.do(req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		h.t.Fatalf("create: status %d, body %s", resp.StatusCode, body)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		h.t.Fatal("create: no Location header")
	}
	return loc
}

// patch sends body at offset and returns the response.
func (h *harness) patch(location string, offset int, body []byte) *http.Response {
	h.t.Helper()

	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodPatch, location, bytes.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Offset", strconv.Itoa(offset))
	req.Header.Set("Content-Type", "application/offset+octet-stream")

	return h.do(req)
}

func (h *harness) offsetOf(location string) int {
	h.t.Helper()

	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodHead, location, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Tus-Resumable", "1.0.0")

	resp := h.do(req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("HEAD: status %d", resp.StatusCode)
	}
	n, err := strconv.Atoi(resp.Header.Get("Upload-Offset"))
	if err != nil {
		h.t.Fatalf("HEAD: bad Upload-Offset %q", resp.Header.Get("Upload-Offset"))
	}
	return n
}

func (h *harness) storedPath(resp *http.Response) string {
	h.t.Helper()
	raw := resp.Header.Get("Geda-Stored-Path")
	if raw == "" {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		h.t.Fatalf("bad Geda-Stored-Path: %v", err)
	}
	return string(decoded)
}

func payload(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func digestOf(t *testing.T, b []byte) hash.Digest {
	t.Helper()
	d, err := hash.Reader(context.Background(), bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestInfoIsUnauthenticated(t *testing.T) {
	h := newHarness(t)

	resp, err := h.Client().Get(h.URL + "/v1/info")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}

	var info struct {
		Versions []int  `json:"versions"`
		DeviceID string `json:"device_id"`
		Name     string `json:"name"`
		Pin      string `json:"spki"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}

	if len(info.Versions) == 0 || info.Versions[0] != 1 {
		t.Errorf("versions = %v", info.Versions)
	}
	if info.DeviceID != "receiver-1" || info.Name != "Studio Mac" {
		t.Errorf("identity = %q / %q", info.DeviceID, info.Name)
	}
	// A client compares this against the pin it stored at pairing time before
	// trusting the connection.
	if info.Pin == "" {
		t.Error("no pin advertised")
	}
}

func TestUploadRequiresAToken(t *testing.T) {
	h := newHarness(t)

	for _, header := range []string{"", "Bearer wrong-token", "Basic abc", "bearer"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.URL+receiver.UploadPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		req.Header.Set("Tus-Resumable", "1.0.0")
		req.Header.Set("Upload-Length", "4")

		resp := h.do(req)
		resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status %d, want 401", header, resp.StatusCode)
		}
	}
}

func TestRevokedTokenIsRejected(t *testing.T) {
	h := newHarness(t)

	_, err := h.db.SQL().ExecContext(t.Context(),
		`UPDATE devices SET revoked_at = ? WHERE id = 'dev-1'`,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, h.URL+receiver.UploadPath, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "4")

	resp := h.do(req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status %d, want 401 after revocation", resp.StatusCode)
	}
}

func TestUploadStoresAndVerifies(t *testing.T) {
	h := newHarness(t)

	body := payload(64<<10, 1)
	d := digestOf(t, body)

	loc := h.create(len(body), map[string]string{
		"filename":    "IMG_4021.HEIC",
		"captured_at": "2026-07-04T15:09:03Z",
		"hash":        d.Full,
		"kind":        storage.KindPhoto,
	})

	resp := h.patch(loc, 0, body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("patch: status %d, body %s", resp.StatusCode, out)
	}

	stored := h.storedPath(resp)
	if want := "2026-07-04_150903_IMG_4021.HEIC"; stored != want {
		t.Fatalf("stored at %q, want %q", stored, want)
	}

	got, err := os.ReadFile(filepath.Join(h.root, stored))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Error("stored bytes differ from what was sent")
	}
}

// The gate for this phase: an upload interrupted partway through resumes from
// the offset the receiver reports, without resending what already arrived.
func TestInterruptedUploadResumes(t *testing.T) {
	h := newHarness(t)

	body := payload(256<<10, 2)
	d := digestOf(t, body)
	const cut = 100 << 10

	loc := h.create(len(body), map[string]string{
		"filename":    "VID_0007.MOV",
		"captured_at": "2026-07-04T15:09:03Z",
		"hash":        d.Full,
		"kind":        storage.KindVideo,
	})

	first := h.patch(loc, 0, body[:cut])
	first.Body.Close()
	if first.StatusCode != http.StatusNoContent {
		t.Fatalf("first patch: status %d", first.StatusCode)
	}

	if got := h.offsetOf(loc); got != cut {
		t.Fatalf("resume offset = %d, want %d", got, cut)
	}

	second := h.patch(loc, cut, body[cut:])
	defer second.Body.Close()
	if second.StatusCode != http.StatusNoContent {
		t.Fatalf("resume patch: status %d", second.StatusCode)
	}

	stored := h.storedPath(second)
	if stored == "" {
		t.Fatal("resumed upload did not report a stored path")
	}

	got, err := os.ReadFile(filepath.Join(h.root, stored))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Error("resumed file does not match the original")
	}
}

// Resuming across a restart loses the in-memory hasher, so the receiver must
// fall back to hashing the finished file rather than accepting it unverified.
func TestResumeAfterRestartStillVerifies(t *testing.T) {
	h := newHarness(t)

	body := payload(128<<10, 3)
	d := digestOf(t, body)
	const cut = 64 << 10

	loc := h.create(len(body), map[string]string{
		"filename": "a.bin",
		"hash":     d.Full,
		"kind":     storage.KindFile,
	})

	h.patch(loc, 0, body[:cut]).Body.Close()

	// Rebuild the server against the same directories, discarding all
	// in-memory state, and finish the upload through it.
	id, err := identity.Load(filepath.Join(filepath.Dir(h.root), "identity"))
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := receiver.New(receiver.Config{
		DeviceID: "receiver-1", Name: "Studio Mac",
		DB: h.db, Files: h.files, Identity: id,
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	fresh := httptest.NewServer(restarted.Handler())
	defer fresh.Close()

	resumed := strings.Replace(loc, h.URL, fresh.URL, 1)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, resumed, bytes.NewReader(body[cut:]))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Offset", strconv.Itoa(cut))
	req.Header.Set("Content-Type", "application/offset+octet-stream")

	resp, err := fresh.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d, body %s", resp.StatusCode, out)
	}

	raw := resp.Header.Get("Geda-Stored-Path")
	decoded, _ := base64.StdEncoding.DecodeString(raw)
	got, err := os.ReadFile(filepath.Join(h.root, string(decoded)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Error("file resumed across a restart does not match")
	}
}

// A declared hash that does not match the bytes must never be stored. This is
// what makes delete-after-transfer safe in a later phase.
func TestChecksumMismatchIsRejected(t *testing.T) {
	h := newHarness(t)

	body := payload(8<<10, 4)
	wrong := digestOf(t, payload(8<<10, 99))

	loc := h.create(len(body), map[string]string{
		"filename": "bad.bin",
		"hash":     wrong.Full,
		"kind":     storage.KindFile,
	})

	resp := h.patch(loc, 0, body)
	defer resp.Body.Close()

	if resp.StatusCode != 460 {
		t.Errorf("status %d, want 460", resp.StatusCode)
	}

	var n int
	if err := h.db.SQL().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM files`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("a file failing verification was recorded (%d rows)", n)
	}

	entries, _ := os.ReadDir(h.root)
	for _, e := range entries {
		if !e.IsDir() {
			t.Errorf("a file failing verification was stored: %s", e.Name())
		}
	}
}

func TestUploadWithoutFilenameIsRejected(t *testing.T) {
	h := newHarness(t)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, h.URL+receiver.UploadPath, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "4")
	req.Header.Set("Upload-Metadata", meta(map[string]string{"kind": storage.KindFile}))

	resp := h.do(req)
	defer resp.Body.Close()

	// Creation may succeed; what matters is that the file never lands.
	if resp.StatusCode == http.StatusCreated {
		loc := resp.Header.Get("Location")
		patched := h.patch(loc, 0, []byte("abcd"))
		patched.Body.Close()
		if patched.StatusCode == http.StatusNoContent {
			t.Error("an upload with no filename was accepted")
		}
	}
}

// A device must not be able to file its uploads under another device's id.
func TestDeviceIdComesFromTheTokenNotTheClient(t *testing.T) {
	h := newHarness(t)
	h.addDevice("dev-2", "Someone else", "other-token")

	body := payload(4<<10, 5)
	loc := h.create(len(body), map[string]string{
		"filename":  "spoof.bin",
		"kind":      storage.KindFile,
		"device_id": "dev-2", // the lie
	})

	resp := h.patch(loc, 0, body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", resp.StatusCode)
	}

	var owner string
	if err := h.db.SQL().QueryRowContext(t.Context(),
		`SELECT device_id FROM files`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != "dev-1" {
		t.Errorf("file filed under %q, want dev-1 — the token's device", owner)
	}
}

func TestDedupProbe(t *testing.T) {
	h := newHarness(t)

	body := payload(32<<10, 6)
	d := digestOf(t, body)
	const capturedAt = "2026-07-04T15:09:03Z"

	probe := func() []struct {
		ID   string `json:"id"`
		Have bool   `json:"have"`
		Path string `json:"path"`
	} {
		t.Helper()

		reqBody, _ := json.Marshal(map[string]any{
			"items": []map[string]any{{
				"id":          "asset-1",
				"size":        len(body),
				"captured_at": capturedAt,
				"head_hash":   d.Head,
			}},
		})

		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, h.URL+"/v1/have", bytes.NewReader(reqBody))
		req.Header.Set("Authorization", "Bearer "+testToken)

		resp := h.do(req)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("probe: status %d", resp.StatusCode)
		}

		var out struct {
			Results []struct {
				ID   string `json:"id"`
				Have bool   `json:"have"`
				Path string `json:"path"`
			} `json:"results"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out.Results
	}

	before := probe()
	if len(before) != 1 || before[0].Have {
		t.Fatalf("probe before upload = %+v, want have=false", before)
	}

	loc := h.create(len(body), map[string]string{
		"filename": "IMG_1.HEIC", "captured_at": capturedAt,
		"hash": d.Full, "kind": storage.KindPhoto,
	})
	h.patch(loc, 0, body).Body.Close()

	after := probe()
	if len(after) != 1 || !after[0].Have {
		t.Fatalf("probe after upload = %+v, want have=true", after)
	}
	if after[0].Path == "" {
		t.Error("probe reported have without a path")
	}
}

// The dedup probe is per device: one phone's files must not make another
// phone's upload disappear.
func TestDedupProbeIsScopedToTheDevice(t *testing.T) {
	h := newHarness(t)
	h.addDevice("dev-2", "Other phone", "other-token")

	body := payload(16<<10, 7)
	d := digestOf(t, body)

	loc := h.create(len(body), map[string]string{
		"filename": "IMG_1.HEIC", "hash": d.Full, "kind": storage.KindPhoto,
	})
	h.patch(loc, 0, body).Body.Close()

	reqBody, _ := json.Marshal(map[string]any{
		"items": []map[string]any{{"id": "a", "size": len(body), "head_hash": d.Head}},
	})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, h.URL+"/v1/have", bytes.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer other-token")

	resp := h.do(req)
	defer resp.Body.Close()

	var out struct {
		Results []struct {
			Have bool `json:"have"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 1 || out.Results[0].Have {
		t.Errorf("another device's file answered the probe: %+v", out.Results)
	}
}

func TestDedupProbeRejectsOversizedBatch(t *testing.T) {
	h := newHarness(t)

	items := make([]map[string]any, 1001)
	for i := range items {
		items[i] = map[string]any{"id": strconv.Itoa(i), "size": 1, "head_hash": "x"}
	}
	reqBody, _ := json.Marshal(map[string]any{"items": items})

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, h.URL+"/v1/have", bytes.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+testToken)

	resp := h.do(req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400", resp.StatusCode)
	}
}

// Re-uploading identical content must be skipped rather than stored twice.
func TestRepeatUploadIsDeduplicated(t *testing.T) {
	h := newHarness(t)

	body := payload(32<<10, 8)
	d := digestOf(t, body)
	md := map[string]string{
		"filename": "IMG_1.HEIC", "captured_at": "2026-07-04T15:09:03Z",
		"hash": d.Full, "kind": storage.KindPhoto,
	}

	first := h.patch(h.create(len(body), md), 0, body)
	first.Body.Close()
	firstPath := h.storedPath(first)

	second := h.patch(h.create(len(body), md), 0, body)
	second.Body.Close()

	if second.Header.Get("Geda-Deduplicated") != "1" {
		t.Error("repeat upload was not reported as deduplicated")
	}
	if got := h.storedPath(second); got != firstPath {
		t.Errorf("dedup pointed at %q, want %q", got, firstPath)
	}

	var n int
	if err := h.db.SQL().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM files`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("ledger has %d rows after a repeat upload, want 1", n)
	}
}

// An upload id from a URL must never reach the filesystem as a path.
func TestUploadIdIsValidatedBeforeTouchingDisk(t *testing.T) {
	h := newHarness(t)

	for _, id := range []string{"../../etc/passwd", "..", "short", strings.Repeat("z", 32)} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodHead,
			h.URL+receiver.UploadPath+id, nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Tus-Resumable", "1.0.0")

		resp := h.do(req)
		resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("id %q: status %d, want 404", id, resp.StatusCode)
		}
	}
}

// The server must present a certificate matching the pin it advertises, and
// refuse anything below TLS 1.3.
func TestServeUsesPinnedTLS13(t *testing.T) {
	dir := t.TempDir()

	db, err := store.Open(t.Context(), filepath.Join(dir, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	files, err := storage.New(db, filepath.Join(dir, "Photos"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := identity.Load(filepath.Join(dir, "identity"))
	if err != nil {
		t.Fatal(err)
	}

	srv, err := receiver.New(receiver.Config{
		DeviceID: "receiver-1", Name: "Studio Mac",
		DB: db, Files: files, Identity: id,
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx, ln) }()

	addr := ln.Addr().String()

	// A client that pins the SPKI accepts this connection without any CA.
	var seenPin string
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // pin verification replaces name checking
			MinVersion:         tls.VersionTLS13,
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				leaf, err := x509.ParseCertificate(rawCerts[0])
				if err != nil {
					return err
				}
				pin, err := identity.PinOf(leaf)
				if err != nil {
					return err
				}
				seenPin = pin
				if pin != id.Pin {
					return fmt.Errorf("pin mismatch")
				}
				return nil
			},
		},
	}}

	resp, err := client.Get("https://" + addr + "/v1/info")
	if err != nil {
		t.Fatalf("pinned client could not connect: %v", err)
	}
	resp.Body.Close()

	if seenPin != id.Pin {
		t.Errorf("served pin %q, advertised %q", seenPin, id.Pin)
	}
	if resp.TLS == nil || resp.TLS.Version != tls.VersionTLS13 {
		t.Errorf("negotiated TLS version %x, want 1.3", resp.TLS.Version)
	}

	// TLS 1.2 must not be accepted at all.
	old := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true,
		MaxVersion:         tls.VersionTLS12,
	}}}
	if _, err := old.Get("https://" + addr + "/v1/info"); err == nil {
		t.Error("a TLS 1.2 client was accepted")
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Errorf("Serve returned %v", err)
	}
}

// tusd validates offsets, but the upload file is opened for append: if the two
// ever disagreed, bytes would land at the wrong place and only surface as a
// hash mismatch after the whole file had been sent.
func TestChunkAtTheWrongOffsetIsRejected(t *testing.T) {
	h := newHarness(t)

	body := payload(16<<10, 11)
	loc := h.create(len(body), map[string]string{
		"filename": "a.bin", "kind": storage.KindFile,
	})

	h.patch(loc, 0, body[:4<<10]).Body.Close()

	// Claim an offset the server has not reached.
	resp := h.patch(loc, 12<<10, body[12<<10:])
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		t.Fatal("a chunk at the wrong offset was accepted")
	}
	if got := h.offsetOf(loc); got != 4<<10 {
		t.Errorf("offset moved to %d after a rejected chunk, want %d", got, 4<<10)
	}
}

// last_seen_at is display-only. Writing it on every authenticated request
// would make it the busiest write in the system during a large transfer.
func TestLastSeenIsThrottled(t *testing.T) {
	h := newHarness(t)

	var before string
	if err := h.db.SQL().QueryRowContext(t.Context(),
		`SELECT COALESCE(last_seen_at, '') FROM devices WHERE id = 'dev-1'`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	body := payload(1<<10, 12)
	for i := range 5 {
		loc := h.create(len(body), map[string]string{
			"filename": fmt.Sprintf("f%d.bin", i), "kind": storage.KindFile,
		})
		h.patch(loc, 0, body).Body.Close()
	}

	var after string
	if err := h.db.SQL().QueryRowContext(t.Context(),
		`SELECT COALESCE(last_seen_at, '') FROM devices WHERE id = 'dev-1'`).Scan(&after); err != nil {
		t.Fatal(err)
	}

	if after == "" {
		t.Fatal("last_seen_at was never recorded")
	}
	if before != "" && after != before {
		t.Error("last_seen_at was written more than once inside the throttle window")
	}
}
