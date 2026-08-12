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

package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/geda/geda-transfer/core/client"
	"github.com/geda/geda-transfer/core/pairing"
	"github.com/geda/geda-transfer/core/storage"
	"github.com/geda/geda-transfer/core/store"

	"github.com/geda/geda-transfer/cli/internal/config"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stateDir is deliberately not t.TempDir(): the control socket lives in here,
// and sun_path is about a hundred characters, which a test name plus the
// system temp directory can exceed on macOS.
func stateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gedad")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Name = "Test NAS"
	cfg.StateDir = stateDir(t)
	cfg.Listen = "127.0.0.1:0"
	// Discovery binds fixed UDP ports and sprays the local network; neither
	// belongs in a unit test. It has its own tests in core/discovery.
	cfg.Discovery = false
	cfg.MDNS = false
	if err := cfg.Resolve(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// start runs a daemon and returns it once it is answering.
func start(t *testing.T, cfg config.Config) *Daemon {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	d, err := New(ctx, cfg, "test", quietLogger())
	if err != nil {
		cancel()
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("daemon did not stop")
		}
		d.Close()
	})

	waitReady(t, d)
	return d
}

func waitReady(t *testing.T, d *Daemon) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := client.New(client.Config{
			Pin:            d.ident.Pin,
			Addrs:          []string{d.Addr().String()},
			DialTimeout:    time.Second,
			RequestTimeout: 2 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Info(context.Background()); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon never answered")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The whole headless pairing path: the control socket issues an offer, a
// client redeems it over pinned TLS, and the device shows up in the ledger.
func TestPairThroughTheControlSocket(t *testing.T) {
	d := start(t, testConfig(t))
	ctx := context.Background()

	offer, err := d.Pair(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	payload, err := pairing.Decode(offer.URI)
	if err != nil {
		t.Fatal(err)
	}
	if payload.SPKI != d.ident.Pin {
		t.Errorf("offer pins %q, the receiver's key is %q", payload.SPKI, d.ident.Pin)
	}

	c, result, err := client.PairWith(ctx, payload, client.Device{
		ID: "phone-1", Name: "Test iPhone", Platform: "ios",
	}, client.Config{
		// The advertised addresses are every local interface, which in a test
		// means addresses this process cannot dial; the listener is what is
		// under test here, and cross-subnet reachability is P2's gate.
		Addrs:          []string{d.Addr().String()},
		DialTimeout:    2 * time.Second,
		RequestTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeviceID != d.DeviceID() {
		t.Errorf("paired with %q, want %q", result.DeviceID, d.DeviceID())
	}

	// The token works.
	if _, err := c.Have(ctx, []client.HaveItem{{ID: "a", Size: 10, HeadHash: "deadbeef"}}); err != nil {
		t.Fatalf("token issued by pairing does not work: %v", err)
	}

	devices, err := d.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].ID != "phone-1" || devices[0].Revoked {
		t.Fatalf("devices = %+v", devices)
	}

	status, err := d.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.PairedDevices != 1 {
		t.Errorf("paired devices = %d", status.PairedDevices)
	}

	// Unpairing revokes the credential immediately, without a restart.
	if err := d.Unpair(ctx, "phone-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Have(ctx, []client.HaveItem{{ID: "a", Size: 10, HeadHash: "deadbeef"}}); err == nil {
		t.Fatal("a revoked token still works")
	}
}

// A pairing code is spent when it is presented, so a second attempt with the
// same code must fail even while the offer's clock is still running.
func TestPairingCodeIsSingleUse(t *testing.T) {
	d := start(t, testConfig(t))
	ctx := context.Background()

	offer, err := d.Pair(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := pairing.Decode(offer.URI)
	if err != nil {
		t.Fatal(err)
	}

	cfg := client.Config{
		Addrs:          []string{d.Addr().String()},
		DialTimeout:    2 * time.Second,
		RequestTimeout: 5 * time.Second,
	}
	self := client.Device{ID: "phone-1", Name: "Test iPhone", Platform: "ios"}

	if _, _, err := client.PairWith(ctx, payload, self, cfg); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.PairWith(ctx, payload, self, cfg); err == nil {
		t.Fatal("the same pairing code paired a second device")
	}
}

// Paired phones remember which receiver they trust, so the identifier has to
// survive a restart -- a new one would look like a different machine.
func TestDeviceIDSurvivesRestart(t *testing.T) {
	cfg := testConfig(t)

	first := start(t, cfg)
	id := first.DeviceID()
	if id == "" {
		t.Fatal("no device id was minted")
	}

	second, err := New(context.Background(), cfg, "test", quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	if second.DeviceID() != id {
		t.Errorf("device id changed across restart: %q then %q", id, second.DeviceID())
	}
}

func TestNamingTemplateIsValidatedAndStored(t *testing.T) {
	cfg := testConfig(t)
	cfg.NamingTemplate = "{yyyy}/{yyyy}-{MM}-{dd}_{original_name}.{ext}"

	d := start(t, cfg)

	got, err := d.files.Template(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != cfg.NamingTemplate {
		t.Errorf("stored template = %q, want %q", got, cfg.NamingTemplate)
	}
}

// A template that cannot render must stop the daemon at startup. Discovering
// it when the first photo arrives is too late -- that upload fails, and the
// user is not watching.
func TestUnusableNamingTemplateFailsAtStartup(t *testing.T) {
	cfg := testConfig(t)
	cfg.NamingTemplate = "{yyyy"

	d, err := New(context.Background(), cfg, "test", quietLogger())
	if err == nil {
		d.Close()
		t.Fatal("expected startup to fail on an unusable template")
	}
}

func TestAdvertiseOverridesTheLocalAddresses(t *testing.T) {
	// The bridged-container case: the only address the daemon can see is a
	// private bridge address no phone can dial, so the host's address is
	// configured instead.
	cfg := testConfig(t)
	cfg.Advertise = []string{"203.0.113.9"}
	cfg.AdvertisePort = 47891

	d := start(t, cfg)

	status, err := d.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Addrs) != 1 || status.Addrs[0] != "203.0.113.9:47891" {
		t.Fatalf("addrs = %v, want the configured address with the advertised port", status.Addrs)
	}

	offer, err := d.Pair(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(offer.Addrs) != 1 || offer.Addrs[0] != "203.0.113.9:47891" {
		t.Errorf("QR payload advertises %v, which is what a phone will try to dial", offer.Addrs)
	}
}

// The destination is the user's data directory and the state directory holds
// the ledger and the key; conflating them would put a SQLite file in the
// middle of somebody's photo library.
func TestDefaultLayout(t *testing.T) {
	cfg := testConfig(t)
	d := start(t, cfg)

	if want := filepath.Join(cfg.StateDir, "Photos"); d.files.Root() != want {
		t.Errorf("destination = %q, want %q", d.files.Root(), want)
	}
	for _, path := range []string{
		filepath.Join(cfg.StateDir, "ledger.db"),
		filepath.Join(cfg.StateDir, "identity", "identity.key"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s: %v", path, err)
		}
	}
}

func TestStatusCountsWhatTheLedgerHolds(t *testing.T) {
	cfg := testConfig(t)
	d := start(t, cfg)
	ctx := context.Background()

	db, err := store.Open(ctx, filepath.Join(cfg.StateDir, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.SQL().ExecContext(ctx, `
		INSERT INTO devices (id, name, platform, spki_pin, token_hash, paired_at)
		VALUES ('phone-1', 'iPhone', 'ios', 'pin', 'hash', ?)`,
		time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
		INSERT INTO files (device_id, hash, head_hash, size, original_name,
			dir, basename, ext, stored_path, kind, received_at)
		VALUES ('phone-1', 'h', 'hh', 2048, 'IMG_0001.HEIC',
			'', 'IMG_0001', 'heic', 'IMG_0001.heic', ?, ?)`,
		storage.KindPhoto, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	status, err := d.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Files != 1 || status.Bytes != 2048 {
		t.Errorf("files = %d, bytes = %d; want 1 and 2048", status.Files, status.Bytes)
	}

	devices, err := d.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].Files != 1 || devices[0].Bytes != 2048 {
		t.Errorf("devices = %+v", devices)
	}
}
