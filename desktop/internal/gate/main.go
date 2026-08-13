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

// Command gate drives the desktop app without a window.
//
// P6's gate is a person, not a number: "a person who has never seen the app
// can pair and transfer without instructions" (docs/PLAN.md). No script can
// assert that. What a script *can* assert is everything that person depends
// on, and that is what this program does -- against the same bindings the
// window calls, with a real pinned TLS client standing in for the phone:
//
//   - a machine with nothing configured comes up ready to receive;
//   - the first screen offers a pairing code that a client can actually redeem;
//   - a file sent against that code arrives, is verified, and is findable;
//   - the live view saw it happen;
//   - the settings a first-time user is most likely to change apply.
//
// It is not a test of the window. It is a test that there is nothing behind
// the window for the window to get wrong.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/geda/geda-transfer/core/client"
	"github.com/geda/geda-transfer/core/hash"
	"github.com/geda/geda-transfer/core/pairing"
	"github.com/geda/geda-transfer/core/receiver"
	"github.com/geda/geda-transfer/core/storage"
	"github.com/geda/geda-transfer/core/store"

	"github.com/geda/geda-transfer/desktop/internal/app"
	"github.com/geda/geda-transfer/desktop/internal/settings"
)

func main() {
	var (
		stateDir  = flag.String("state", "", "state directory to use (required)")
		dest      = flag.String("dest", "", "destination directory (required)")
		seedOnly  = flag.Bool("seed", false, "only prepare the state directory, then exit")
		seedPortN = flag.Int("port", 0, "with -seed, the port to listen on (0 asks the OS)")
		onboarded = flag.Bool("onboarded", false, "with -seed, mark the welcome screen as already done")
		verbose   = flag.Bool("v", false, "log what the app logs")
	)
	flag.Parse()

	if *stateDir == "" || *dest == "" {
		fmt.Fprintln(os.Stderr, "gate: -state and -dest are required")
		os.Exit(2)
	}

	if err := run(*stateDir, *dest, *seedOnly, *seedPortN, *onboarded, *verbose); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func run(stateDir, dest string, seedOnly bool, seedPortN int, onboarded, verbose bool) error {
	ctx := context.Background()

	level := slog.LevelError
	if verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if seedOnly {
		return seedState(ctx, stateDir, dest, seedPortN, onboarded)
	}

	// A port of 0 keeps the gate off the product's fixed port, so it can run
	// on a machine that already has a receiver on it -- including in CI, twice
	// at once.
	if err := seedState(ctx, stateDir, dest, 0, false); err != nil {
		return err
	}

	a := app.New(app.Config{
		Version:  "gate",
		StateDir: stateDir,
		Logger:   log,
		// So the gate can run on a machine that already has a receiver on the
		// product's port, and in CI twice at once.
		AllowEphemeralPort: true,
	})
	a.UseEmitter(collector)
	if err := a.Start(ctx); err != nil {
		return fmt.Errorf("the app did not start: %w", err)
	}
	defer a.Shutdown()

	checks := []struct {
		name string
		fn   func(context.Context, *app.App) error
	}{
		{"a fresh machine is ready to receive", checkReady},
		{"the first screen offers a code a phone can redeem", checkPairing},
		{"a file sent against that code arrives and is verified", checkTransfer},
		{"the live view saw the transfer", checkLiveView},
		{"the history lists it and can locate it on disk", checkHistory},
		{"the settings a first-time user changes are applied", checkSettings},
		{"a setting that cannot work is refused with a reason", checkBadSettings},
	}

	for _, check := range checks {
		if err := check.fn(ctx, a); err != nil {
			return fmt.Errorf("%s: %w", check.name, err)
		}
		fmt.Printf("  ok: %s\n", check.name)
	}
	return nil
}

// seedState writes the settings a state directory needs, starting nothing.
func seedState(ctx context.Context, stateDir, dest string, port int, onboarded bool) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}

	// Written through the app's own settings package, so the gate cannot pass
	// by preparing a state the app would never produce.
	db, err := openLedger(ctx, stateDir)
	if err != nil {
		return err
	}
	defer db.Close()

	set := settings.Default()
	set.Dest = dest
	set.Port = port
	set.Onboarded = onboarded
	// The responders bind fixed, shared UDP ports. A gate that took them would
	// fail on any machine already running a receiver, and would be testing the
	// port rather than the product.
	set.MDNS = false
	set.Discovery = false

	// Port 0 means "any free port", so the gate can run on a machine that
	// already has a receiver on the product's port -- and in CI, twice at
	// once. Validate refuses it for anybody who has not asked, because a real
	// installation whose port moved on every restart would leave every paired
	// phone dialling nothing.
	set.AllowEphemeralPort = true

	return settings.Save(ctx, db, set)
}

// --- the checks ------------------------------------------------------------

// checkReady is the zero-configuration promise: install it, and it receives.
func checkReady(_ context.Context, a *app.App) error {
	status, err := a.Status()
	if err != nil {
		return err
	}
	if !status.Running {
		return fmt.Errorf("the receiver is not running: %s", status.Error)
	}
	if status.Name == "" {
		return errors.New("the receiver has no name, so a phone would show it as blank")
	}
	if status.Fingerprint == "" {
		return errors.New("the receiver has no identity")
	}
	if len(status.Addrs) == 0 {
		return errors.New("the receiver advertises no address, so nothing can reach it")
	}
	if status.Dest == "" {
		return errors.New("there is nowhere for files to go")
	}
	if _, err := os.Stat(status.Dest); err != nil {
		return fmt.Errorf("the destination does not exist: %w", err)
	}
	if status.Onboarded {
		return errors.New("a machine that has never been set up is already marked as onboarded")
	}
	return nil
}

var scanned pairing.Payload

// checkPairing takes the code off the first screen and decodes it exactly as
// a camera would.
func checkPairing(_ context.Context, a *app.App) error {
	view, err := a.Pair()
	if err != nil {
		return err
	}

	if !strings.HasPrefix(view.SVG, "<svg") || !strings.Contains(view.SVG, "</svg>") {
		return errors.New("the pairing code is not a drawable image")
	}
	if view.Fingerprint == "" {
		return errors.New("the code carries no fingerprint for the user to compare")
	}
	if time.Until(view.ExpiresAt) <= 0 {
		return errors.New("the code is already expired")
	}
	if len(view.Addrs) == 0 {
		return errors.New("the code carries no address, so a phone could not connect")
	}

	payload, err := pairing.Decode(view.URI)
	if err != nil {
		return fmt.Errorf("the code does not decode as a pairing payload: %w", err)
	}
	if payload.SPKI == "" || payload.PSK == "" {
		return errors.New("the payload is missing the key or the secret")
	}
	scanned = payload
	return nil
}

var (
	phone      *client.Client
	phoneToken string
)

// checkTransfer is a real upload over real TLS, pinned to the key the code
// carried. Nothing here is mocked.
func checkTransfer(ctx context.Context, a *app.App) error {
	c, result, err := client.PairWith(ctx, scanned, client.Device{
		ID:       "gate-phone",
		Name:     "Gate Phone",
		Platform: "ios",
	}, client.Config{})
	if err != nil {
		return fmt.Errorf("pairing failed: %w", err)
	}
	phone = c
	phoneToken = result.Token

	if result.Token == "" {
		return errors.New("pairing produced no token")
	}

	// A code is single-use and spent on presentation. Redeeming it twice must
	// fail, or a photograph of the screen would be a working credential.
	if _, _, err := client.PairWith(ctx, scanned, client.Device{
		ID: "second-phone", Name: "Second", Platform: "ios",
	}, client.Config{}); err == nil {
		return errors.New("the same pairing code was accepted twice")
	}

	devices, err := a.Devices()
	if err != nil {
		return err
	}
	if len(devices) != 1 || devices[0].Name != "Gate Phone" {
		return fmt.Errorf("the app lists %d devices after pairing", len(devices))
	}

	body := make([]byte, 3<<20)
	if _, err := rand.Read(body); err != nil {
		return err
	}
	digest, err := hash.Reader(ctx, bytes.NewReader(body))
	if err != nil {
		return err
	}

	stored, err := upload(ctx, c, result.Token, body, digest.Full)
	if err != nil {
		return err
	}

	status, err := a.Status()
	if err != nil {
		return err
	}

	// The receiver's own verification is the authority, but the gate checks
	// the bytes on disk too: a stored path with the wrong content is exactly
	// the failure that a hash check inside one process cannot catch.
	onDisk := filepath.Join(status.Dest, filepath.FromSlash(stored))
	written, err := os.ReadFile(onDisk)
	if err != nil {
		return fmt.Errorf("the file is not where the receiver said: %w", err)
	}
	if !bytes.Equal(written, body) {
		return errors.New("the stored file differs from what was sent")
	}

	// The default template files each device in its own folder (P6's
	// "per-device folders"), so the stored path must be under one.
	if !strings.Contains(stored, "/") {
		return fmt.Errorf("%q is not filed under a device folder", stored)
	}
	if !strings.HasPrefix(stored, "Gate Phone/") {
		return fmt.Errorf("%q is not under the sending device's folder", stored)
	}

	if status.Files != 1 || status.Bytes != int64(len(body)) {
		return fmt.Errorf("the app reports %d files and %d bytes", status.Files, status.Bytes)
	}
	return nil
}

// checkLiveView asserts the window would have shown the transfer happening.
func checkLiveView(_ context.Context, a *app.App) error {
	snapshot := a.Transfers()
	if len(snapshot.Recent) == 0 {
		return errors.New("the live view has no record of the transfer")
	}

	latest := snapshot.Recent[0]
	if latest.Outcome != app.OutcomeStored {
		return fmt.Errorf("the transfer ended as %q, not stored: %s", latest.Outcome, latest.Error)
	}
	if latest.DeviceName != "Gate Phone" {
		return fmt.Errorf("the transfer is attributed to %q", latest.DeviceName)
	}
	if latest.Offset != latest.Size || latest.Size == 0 {
		return fmt.Errorf("the transfer finished at %d of %d bytes", latest.Offset, latest.Size)
	}
	if len(snapshot.Active) != 0 {
		return errors.New("a finished transfer is still shown as arriving")
	}

	// The window is pushed snapshots rather than polling. If nothing was
	// emitted, a user watching the screen would have seen a static list.
	if collector.count() == 0 {
		return errors.New("the window was never told anything was happening")
	}
	return nil
}

// historyPage is the page size the window uses.
const historyPage = 100

// checkHistory covers the other half of a first-time user's question: not just
// "did it send", but "where did it go".
func checkHistory(_ context.Context, a *app.App) error {
	entries, err := a.History("", "", historyPage)
	if err != nil {
		return err
	}
	if len(entries) != 1 {
		return fmt.Errorf("the history holds %d entries", len(entries))
	}
	if entries[0].DeviceName != "Gate Phone" {
		return fmt.Errorf("the entry is attributed to %q", entries[0].DeviceName)
	}
	if entries[0].Hash == "" {
		return errors.New("the entry records no hash, so nothing could ever be verified against it")
	}

	// Scoping by a device that sent nothing must return nothing rather than
	// everything, or the filter is worse than no filter.
	empty, err := a.History("nobody", "", historyPage)
	if err != nil {
		return err
	}
	if len(empty) != 0 {
		return fmt.Errorf("filtering by an unknown device returned %d entries", len(empty))
	}

	// "Show in Finder" must refuse a path that climbs out of the destination.
	if err := a.RevealFile("../../../etc/passwd"); err == nil {
		return errors.New("a path outside the destination was accepted")
	}
	return nil
}

// checkSettings changes the two things a first-time user is most likely to
// change, and proves both took effect.
func checkSettings(ctx context.Context, a *app.App) error {
	view, err := a.Settings()
	if err != nil {
		return err
	}
	if view.TemplatePreview == "" {
		return errors.New("the settings screen cannot show what a filename will look like")
	}
	if len(view.TemplateVariables) == 0 {
		return errors.New("the settings screen lists no template variables")
	}

	// A new destination, which requires the receiver to be rebuilt.
	moved := filepath.Join(filepath.Dir(view.Dest), "Moved")
	next := view.Settings
	next.Dest = moved
	next.Name = "Renamed Receiver"
	next.Template = "{yyyy}-{MM}-{dd}_{original_name}.{ext}"

	if _, err := a.SaveSettings(next); err != nil {
		return err
	}

	status, err := a.Status()
	if err != nil {
		return err
	}
	if !status.Running {
		return fmt.Errorf("the receiver did not come back after the change: %s", status.Error)
	}
	if status.Dest != moved {
		return fmt.Errorf("the destination is %q, not the one that was saved", status.Dest)
	}
	if status.Name != "Renamed Receiver" {
		return fmt.Errorf("the name is %q, not the one that was saved", status.Name)
	}

	// The phone paired before the restart must still work: the identity and
	// the ledger are on disk, so nothing should have to be paired again.
	phone.SetAddrs(status.Addrs)

	body := []byte("a second file, sent after the settings changed")
	digest, err := hash.Reader(ctx, bytes.NewReader(body))
	if err != nil {
		return err
	}
	stored, err := upload(ctx, phone, phoneToken, body, digest.Full)
	if err != nil {
		return fmt.Errorf("the phone could not send after the restart: %w", err)
	}

	// The new template has no device folder, so this one lands at the root.
	if strings.Contains(stored, "/") {
		return fmt.Errorf("%q ignored the new template", stored)
	}
	if _, err := os.Stat(filepath.Join(moved, filepath.FromSlash(stored))); err != nil {
		return fmt.Errorf("the file did not land in the new destination: %w", err)
	}
	return nil
}

// checkBadSettings is the other half of a settings screen: refusing.
func checkBadSettings(_ context.Context, a *app.App) error {
	view, err := a.Settings()
	if err != nil {
		return err
	}

	for _, bad := range []struct {
		what   string
		mutate func(*settings.Settings)
	}{
		{"a template with a variable that does not exist", func(s *settings.Settings) { s.Template = "{yyy}-{original_name}" }},
		{"an empty destination", func(s *settings.Settings) { s.Dest = "" }},
		{"a relative destination", func(s *settings.Settings) { s.Dest = "somewhere" }},
		{"an empty name", func(s *settings.Settings) { s.Name = "  " }},
		{"a port outside the range", func(s *settings.Settings) { s.Port = 70000 }},
	} {
		next := view.Settings
		bad.mutate(&next)
		if _, err := a.SaveSettings(next); err == nil {
			return fmt.Errorf("%s was accepted", bad.what)
		}
	}

	// Every refusal must leave the receiver as it was.
	status, err := a.Status()
	if err != nil {
		return err
	}
	if !status.Running {
		return errors.New("a refused setting took the receiver down")
	}
	return nil
}

// --- helpers ---------------------------------------------------------------

// upload sends one file over tus and returns where the receiver stored it.
//
// The request is built here rather than through the client's own helpers
// because those cover the JSON control endpoints; tus is raw HTTP, which is
// also exactly what the phone's URLSession sends. The bearer token has to be
// attached by hand for the same reason.
func upload(ctx context.Context, c *client.Client, token string, body []byte, digest string) (string, error) {
	meta := encodeMetadata(map[string]string{
		"filename":    "IMG_4021.HEIC",
		"captured_at": "2026-07-04T15:09:03Z",
		"kind":        storage.KindPhoto,
		"hash":        digest,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL(receiver.UploadPath), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", fmt.Sprint(len(body)))
	req.Header.Set("Upload-Metadata", meta)

	resp, err := c.HTTP().Do(req)
	if err != nil {
		return "", err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	location := resp.Header.Get("Location")
	if location == "" {
		return "", fmt.Errorf("the receiver refused to create the upload: status %d", resp.StatusCode)
	}

	patch, err := http.NewRequestWithContext(ctx, http.MethodPatch, location, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	patch.Header.Set("Authorization", "Bearer "+token)
	patch.Header.Set("Tus-Resumable", "1.0.0")
	patch.Header.Set("Upload-Offset", "0")
	patch.Header.Set("Content-Type", "application/offset+octet-stream")

	done, err := c.HTTP().Do(patch)
	if err != nil {
		return "", err
	}
	defer done.Body.Close()
	io.Copy(io.Discard, done.Body)

	if done.StatusCode != http.StatusNoContent {
		return "", fmt.Errorf("the upload was rejected: status %d", done.StatusCode)
	}

	stored, err := decodeStoredPath(done.Header.Get("Geda-Stored-Path"))
	if err != nil {
		return "", err
	}
	if stored == "" {
		return "", errors.New("the receiver did not say where it put the file")
	}
	return stored, nil
}

func encodeMetadata(pairs map[string]string) string {
	parts := make([]string, 0, len(pairs))
	for k, v := range pairs {
		parts = append(parts, k+" "+base64.StdEncoding.EncodeToString([]byte(v)))
	}
	return strings.Join(parts, ",")
}

func decodeStoredPath(header string) (string, error) {
	if header == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		return "", fmt.Errorf("the stored path is not readable: %w", err)
	}
	return string(raw), nil
}

func openLedger(ctx context.Context, stateDir string) (*store.DB, error) {
	return store.Open(ctx, filepath.Join(stateDir, "ledger.db"))
}

// emitted counts what the window would have been sent.
type emitted struct {
	mu sync.Mutex
	n  int
}

var collector = &emitted{}

func (e *emitted) Emit(string, ...any) {
	e.mu.Lock()
	e.n++
	e.mu.Unlock()
}

func (e *emitted) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.n
}
