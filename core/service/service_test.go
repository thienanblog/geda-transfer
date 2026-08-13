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

package service_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geda/geda-transfer/core/naming"
	"github.com/geda/geda-transfer/core/receiver"
	"github.com/geda/geda-transfer/core/service"
)

func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

// open builds a service in a temp state directory, on a port the OS picks.
func open(t *testing.T, mutate ...func(*service.Config)) *service.Service {
	t.Helper()

	cfg := service.Config{
		StateDir: t.TempDir(),
		Listen:   "127.0.0.1:0",
		Logger:   quiet(),
	}
	for _, m := range mutate {
		m(&cfg)
	}

	svc, err := service.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	return svc
}

// The product has to work with nothing configured (AGENTS.md §4).
func TestZeroConfigurationProducesAWorkingReceiver(t *testing.T) {
	state := t.TempDir()
	svc := open(t, func(c *service.Config) { c.StateDir = state })

	if want := filepath.Join(state, "Photos"); svc.Dest() != want {
		t.Errorf("destination = %q, want %q", svc.Dest(), want)
	}
	if svc.Name() == "" {
		t.Error("the receiver has no name")
	}
	if svc.DeviceID() == "" {
		t.Error("the receiver has no device id")
	}
	if svc.Pin() == "" || svc.Fingerprint() == "" {
		t.Error("the receiver has no identity")
	}

	// The identity has to be on disk, or a reinstall makes every paired
	// device fail with a mismatch that has no override (AGENTS.md §3.5).
	if _, err := os.Stat(filepath.Join(state, "identity", "identity.key")); err != nil {
		t.Errorf("the identity key was not persisted: %v", err)
	}

	tmpl, err := svc.Template(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if tmpl != naming.Default {
		t.Errorf("template = %q, want the default %q", tmpl, naming.Default)
	}
}

func TestStateDirIsRequired(t *testing.T) {
	_, err := service.Open(t.Context(), service.Config{Logger: quiet()})
	if err == nil {
		t.Fatal("a service with no state directory opened")
	}
}

// Paired devices remember the id. A receiver that minted a new one on every
// start would look like a different machine to every phone.
func TestDeviceIDAndIdentitySurviveARestart(t *testing.T) {
	state := t.TempDir()

	first := open(t, func(c *service.Config) { c.StateDir = state })
	id, pin := first.DeviceID(), first.Pin()
	first.Close()

	second := open(t, func(c *service.Config) { c.StateDir = state })
	if second.DeviceID() != id {
		t.Errorf("device id changed across restart: %q then %q", id, second.DeviceID())
	}
	if second.Pin() != pin {
		t.Errorf("SPKI pin changed across restart: %q then %q", pin, second.Pin())
	}
}

// A listen port of 0 is resolved so that what is advertised is dialable.
func TestAdvertisedPortIsTheBoundPort(t *testing.T) {
	svc := open(t, func(c *service.Config) { c.Advertise = []string{"203.0.113.7"} })

	addrs, err := svc.CandidateAddrs()
	if err != nil {
		t.Fatal(err)
	}
	if len(addrs) != 1 {
		t.Fatalf("candidates = %v, want one configured address", addrs)
	}

	port := svc.Addr().(interface{ String() string }).String()
	port = port[strings.LastIndex(port, ":")+1:]
	if want := "203.0.113.7:" + port; addrs[0] != want {
		t.Errorf("candidate = %q, want %q", addrs[0], want)
	}
}

func TestStatusReportsWhatTheLedgerHolds(t *testing.T) {
	svc := open(t)
	ctx := t.Context()

	status, err := svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.PairedDevices != 0 || status.Files != 0 || status.Bytes != 0 {
		t.Errorf("a fresh receiver reports %+v", status)
	}
	if status.Fingerprint != svc.Fingerprint() || status.SPKI != svc.Pin() {
		t.Error("status does not report the receiver's own identity")
	}

	addDevice(t, svc, "phone-1", "An's iPhone")
	addFile(t, svc, "phone-1", "IMG_1.HEIC", 1000, "2026-07-04T10:00:00Z")
	addFile(t, svc, "phone-1", "IMG_2.HEIC", 2000, "2026-07-04T11:00:00Z")

	status, err = svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.PairedDevices != 1 {
		t.Errorf("paired devices = %d, want 1", status.PairedDevices)
	}
	if status.Files != 2 || status.Bytes != 3000 {
		t.Errorf("files = %d (%d bytes), want 2 (3000)", status.Files, status.Bytes)
	}
}

// A revoked device keeps its row: its files are still on disk and still
// attributed to it, so hiding it would leave a folder with no explanation.
func TestDevicesIncludesRevokedOnes(t *testing.T) {
	svc := open(t)
	ctx := t.Context()

	addDevice(t, svc, "phone-1", "An's iPhone")
	addFile(t, svc, "phone-1", "IMG_1.HEIC", 4096, "2026-07-04T10:00:00Z")

	if err := svc.Unpair(ctx, "phone-1"); err != nil {
		t.Fatal(err)
	}

	devices, err := svc.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 {
		t.Fatalf("devices = %d, want 1", len(devices))
	}
	if !devices[0].Revoked {
		t.Error("the revoked device is not marked as revoked")
	}
	if devices[0].Files != 1 || devices[0].Bytes != 4096 {
		t.Errorf("its files were forgotten: %d files, %d bytes", devices[0].Files, devices[0].Bytes)
	}

	status, err := svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.PairedDevices != 0 {
		t.Errorf("a revoked device still counts as paired: %d", status.PairedDevices)
	}
}

func TestUnpairingAnUnknownDeviceFails(t *testing.T) {
	svc := open(t)
	if err := svc.Unpair(t.Context(), "nobody"); err == nil {
		t.Fatal("unpairing a device that never paired succeeded")
	}
}

func TestHistoryIsNewestFirstAndPages(t *testing.T) {
	svc := open(t)
	ctx := t.Context()

	addDevice(t, svc, "phone-1", "An's iPhone")
	addDevice(t, svc, "phone-2", "The iPad")

	addFile(t, svc, "phone-1", "IMG_1.HEIC", 100, "2026-07-04T10:00:00Z")
	addFile(t, svc, "phone-1", "IMG_2.HEIC", 200, "2026-07-04T11:00:00Z")
	addFile(t, svc, "phone-2", "IMG_3.HEIC", 300, "2026-07-04T12:00:00Z")

	all, err := svc.History(ctx, service.HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("history has %d entries, want 3", len(all))
	}
	if all[0].Name != "IMG_3.HEIC" || all[2].Name != "IMG_1.HEIC" {
		t.Errorf("history is not newest first: %q ... %q", all[0].Name, all[2].Name)
	}
	if all[0].DeviceName != "The iPad" {
		t.Errorf("device name = %q, want The iPad", all[0].DeviceName)
	}

	// Paging is by timestamp, not offset, so a file arriving mid-scroll
	// cannot make a row appear twice or vanish.
	page, err := svc.History(ctx, service.HistoryQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 {
		t.Fatalf("first page has %d entries, want 2", len(page))
	}
	next, err := svc.History(ctx, service.HistoryQuery{Before: page[1].ReceivedAt})
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Name != "IMG_1.HEIC" {
		t.Fatalf("second page = %+v, want just IMG_1.HEIC", next)
	}

	scoped, err := svc.History(ctx, service.HistoryQuery{DeviceID: "phone-2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 1 || scoped[0].Name != "IMG_3.HEIC" {
		t.Fatalf("device-scoped history = %+v", scoped)
	}
}

func TestHistoryLimitIsBounded(t *testing.T) {
	svc := open(t)
	ctx := t.Context()

	addDevice(t, svc, "phone-1", "An's iPhone")
	addFile(t, svc, "phone-1", "IMG_1.HEIC", 100, "2026-07-04T10:00:00Z")

	// An unbounded limit turns a window refresh into a full table scan on a
	// NAS holding years of photos.
	got, err := svc.History(ctx, service.HistoryQuery{Limit: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("history = %d entries, want 1", len(got))
	}
}

// `{yyy}` renders literally, so a typo would name every future file after the
// typo and nobody would notice until the photos were filed under it.
func TestSetTemplateRejectsAnUnusableTemplate(t *testing.T) {
	svc := open(t)
	ctx := t.Context()

	before, err := svc.Template(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.SetTemplate(ctx, "{yyy}-{original_name}"); err == nil {
		t.Fatal("a template with an unknown variable was accepted")
	}

	after, err := svc.Template(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("a rejected template was stored anyway: %q", after)
	}
}

func TestSetTemplateStoresAValidTemplate(t *testing.T) {
	svc := open(t)
	ctx := t.Context()

	const tmpl = "{device}/{yyyy}/{yyyy}-{MM}-{dd}_{original_name}.{ext}"
	if err := svc.SetTemplate(ctx, tmpl); err != nil {
		t.Fatal(err)
	}

	got, err := svc.Template(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != tmpl {
		t.Errorf("template = %q, want %q", got, tmpl)
	}
}

// A configured template is validated at startup, not at the first upload.
func TestUnusableConfiguredTemplateFailsToOpen(t *testing.T) {
	_, err := service.Open(t.Context(), service.Config{
		StateDir:       t.TempDir(),
		Listen:         "127.0.0.1:0",
		Logger:         quiet(),
		NamingTemplate: "{nope}",
	})
	if err == nil {
		t.Fatal("a service opened with a template that cannot render")
	}
}

// The settings screen shows the effect of an edit before it is saved.
func TestTemplatePreview(t *testing.T) {
	got, err := service.TemplatePreview("{device}/{yyyy}-{MM}-{dd}_{original_name}.{ext}")
	if err != nil {
		t.Fatal(err)
	}
	if want := "An's iPhone/2026-07-04_IMG_4021.HEIC"; got != want {
		t.Errorf("preview = %q, want %q", got, want)
	}

	if _, err := service.TemplatePreview("{yyy}"); err == nil {
		t.Error("a preview of an invalid template did not report the problem")
	}
}

func TestRunStopsWhenTheContextIsCancelled(t *testing.T) {
	// Discovery off: the responder binds a fixed UDP port, and a test that
	// took it would fail on a machine already running a receiver.
	svc := open(t, func(c *service.Config) { c.Discovery = false; c.MDNS = false })

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	// Wait for the listener to actually be serving before pulling it down.
	deadline := time.After(5 * time.Second)
	for {
		if svc.Addr() != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the receiver never started")
		default:
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v on a clean shutdown", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not stop when the context was cancelled")
	}
}

// --- helpers ---------------------------------------------------------------

func addDevice(t *testing.T, svc *service.Service, id, name string) {
	t.Helper()
	_, err := svc.DB().SQL().ExecContext(t.Context(), `
		INSERT INTO devices (id, name, platform, spki_pin, token_hash, paired_at)
		VALUES (?, ?, 'ios', 'pin', ?, ?)`,
		id, name, receiver.HashToken("token-"+id), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
}

func addFile(t *testing.T, svc *service.Service, deviceID, name string, size int64, receivedAt string) {
	t.Helper()
	_, err := svc.DB().SQL().ExecContext(t.Context(), `
		INSERT INTO files (device_id, hash, head_hash, size, original_name,
		                   dir, basename, ext, stored_path, kind, received_at)
		VALUES (?, ?, 'head', ?, ?, '', ?, 'HEIC', ?, 'photo', ?)`,
		deviceID, "hash-"+name, size, name, name, deviceID+"/"+name, receivedAt)
	if err != nil {
		t.Fatal(err)
	}
}
