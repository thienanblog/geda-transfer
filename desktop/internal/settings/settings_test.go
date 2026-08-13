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

package settings_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/geda/geda-transfer/core/naming"
	"github.com/geda/geda-transfer/core/store"

	"github.com/geda/geda-transfer/desktop/internal/settings"
)

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// Nothing configured must still be a working receiver (AGENTS.md §4).
func TestDefaultsAreUsable(t *testing.T) {
	set := settings.Default()

	if err := set.Validate(); err != nil {
		t.Fatalf("the defaults do not validate: %v", err)
	}
	if !filepath.IsAbs(set.Dest) {
		t.Errorf("the default destination %q is not a full path", set.Dest)
	}
	if set.Name == "" {
		t.Error("the default receiver has no name")
	}
	if !set.MDNS || !set.Discovery {
		t.Error("discovery is off by default, so nothing would find this machine")
	}
	if set.Onboarded {
		t.Error("a machine that has never run is marked as onboarded")
	}
	if set.Autostart {
		t.Error("autostart defaults to on, which changes the OS without being asked")
	}
}

// P6 asks for per-device folders. That is expressed as a template, so the
// template is what has to say so.
func TestDefaultTemplateFilesPerDeviceAndValidates(t *testing.T) {
	if err := naming.Validate(settings.DefaultTemplate); err != nil {
		t.Fatalf("the default template does not render: %v", err)
	}
	if !strings.HasPrefix(settings.DefaultTemplate, "{device}/") {
		t.Errorf("the default template %q does not file per device", settings.DefaultTemplate)
	}
}

func TestLoadReturnsWhatWasSaved(t *testing.T) {
	db := openDB(t)
	ctx := t.Context()

	saved := settings.Default()
	saved.Name = "Studio Mac"
	saved.Dest = filepath.Join(t.TempDir(), "Received")
	saved.Port = 51000
	saved.Advertise = []string{"203.0.113.7", "198.51.100.4"}
	saved.MDNS = false
	saved.Onboarded = true
	saved.Template = "{yyyy}/{original_name}.{ext}"

	if err := settings.Save(ctx, db, saved); err != nil {
		t.Fatal(err)
	}

	got, err := settings.Load(ctx, db)
	if err != nil {
		t.Fatal(err)
	}

	if got.Name != saved.Name || got.Dest != saved.Dest || got.Port != saved.Port {
		t.Errorf("round trip changed the basics: %+v", got)
	}
	if len(got.Advertise) != 2 || got.Advertise[0] != "203.0.113.7" {
		t.Errorf("Advertise = %v", got.Advertise)
	}
	if got.MDNS || !got.Discovery {
		t.Errorf("MDNS = %v, Discovery = %v", got.MDNS, got.Discovery)
	}
	if !got.Onboarded {
		t.Error("Onboarded was not kept")
	}
	if got.Template != saved.Template {
		t.Errorf("Template = %q", got.Template)
	}
}

// The template is stored under core's key so that gedad reading the same
// ledger sees the same value.
func TestTemplateIsStoredUnderCoresKey(t *testing.T) {
	db := openDB(t)
	ctx := t.Context()

	set := settings.Default()
	set.Dest = t.TempDir()
	set.Template = "{yyyy}/{original_name}.{ext}"
	if err := settings.Save(ctx, db, set); err != nil {
		t.Fatal(err)
	}

	got, err := db.Setting(ctx, "naming_template")
	if err != nil {
		t.Fatal(err)
	}
	if got != set.Template {
		t.Errorf("core's key holds %q, want %q", got, set.Template)
	}
}

func TestLoadOnAFreshLedgerReturnsTheDefaults(t *testing.T) {
	got, err := settings.Load(t.Context(), openDB(t))
	if err != nil {
		t.Fatal(err)
	}
	if got.Template != settings.DefaultTemplate {
		t.Errorf("Template = %q, want the default", got.Template)
	}
	if got.Port != settings.Default().Port {
		t.Errorf("Port = %d, want the default", got.Port)
	}
	if got.Onboarded {
		t.Error("a fresh ledger reports the welcome screen as already done")
	}
}

func TestValidateRefusesWhatCannotWork(t *testing.T) {
	base := settings.Default()
	base.Dest = filepath.Join(string(filepath.Separator), "tmp", "dest")

	for _, tc := range []struct {
		name   string
		mutate func(*settings.Settings)
	}{
		{"an empty name", func(s *settings.Settings) { s.Name = "  " }},
		{"an empty destination", func(s *settings.Settings) { s.Dest = "" }},
		{"a relative destination", func(s *settings.Settings) { s.Dest = "Received" }},
		{"port zero", func(s *settings.Settings) { s.Port = 0 }},
		{"a port above the range", func(s *settings.Settings) { s.Port = 70000 }},
		{"an unknown template variable", func(s *settings.Settings) { s.Template = "{yyy}" }},
		{"an unclosed brace", func(s *settings.Settings) { s.Template = "{yyyy" }},
	} {
		set := base
		tc.mutate(&set)
		if err := set.Validate(); err == nil {
			t.Errorf("%s was accepted", tc.name)
		}
	}
}

// A refused save must not be able to reach the ledger.
func TestSaveRejectsInvalidSettings(t *testing.T) {
	db := openDB(t)
	ctx := t.Context()

	set := settings.Default()
	set.Dest = t.TempDir()
	set.Template = "{nope}"

	if err := settings.Save(ctx, db, set); err == nil {
		t.Fatal("an invalid template was saved")
	}
	if _, err := db.Setting(ctx, "naming_template"); err == nil {
		t.Error("the rejected template reached the ledger anyway")
	}
}

// Only the template can be applied to a running receiver; everything else was
// handed to it when it was built.
func TestNeedsRestart(t *testing.T) {
	base := settings.Default()
	base.Dest = filepath.Join(string(filepath.Separator), "tmp", "dest")
	base.Advertise = []string{"203.0.113.7"}

	same := base
	if settings.NeedsRestart(base, same) {
		t.Error("an unchanged settings object asks for a restart")
	}

	template := base
	template.Template = "{original_name}.{ext}"
	if settings.NeedsRestart(base, template) {
		t.Error("a template change asks for a restart it does not need")
	}

	onboarded := base
	onboarded.Onboarded = !base.Onboarded
	if settings.NeedsRestart(base, onboarded) {
		t.Error("finishing the welcome screen restarts the receiver")
	}

	for _, tc := range []struct {
		name   string
		mutate func(*settings.Settings)
	}{
		{"name", func(s *settings.Settings) { s.Name = "Другой" }},
		{"destination", func(s *settings.Settings) { s.Dest = "/tmp/elsewhere" }},
		{"port", func(s *settings.Settings) { s.Port = 51000 }},
		{"mDNS", func(s *settings.Settings) { s.MDNS = !s.MDNS }},
		{"discovery", func(s *settings.Settings) { s.Discovery = !s.Discovery }},
		{"advertise", func(s *settings.Settings) { s.Advertise = []string{"198.51.100.4"} }},
		{"advertise length", func(s *settings.Settings) { s.Advertise = nil }},
	} {
		next := base
		tc.mutate(&next)
		if !settings.NeedsRestart(base, next) {
			t.Errorf("changing the %s does not restart the receiver, so it would not take effect", tc.name)
		}
	}
}

func TestServiceConfigCarriesTheSettings(t *testing.T) {
	set := settings.Default()
	set.Name = "  Studio Mac  "
	set.Dest = filepath.Join(string(filepath.Separator), "tmp", "dest")
	set.Port = 51000

	cfg := set.ServiceConfig("/tmp/state", "1.2.3")

	if cfg.Name != "Studio Mac" {
		t.Errorf("Name = %q; surrounding space was not trimmed", cfg.Name)
	}
	if cfg.Listen != ":51000" {
		t.Errorf("Listen = %q, want :51000", cfg.Listen)
	}
	if cfg.StateDir != "/tmp/state" || cfg.Version != "1.2.3" {
		t.Errorf("unexpected config: %+v", cfg)
	}
	if cfg.NamingTemplate != settings.DefaultTemplate {
		t.Errorf("NamingTemplate = %q", cfg.NamingTemplate)
	}
}

func TestStateDirHonoursTheOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(settings.EnvStateDir, dir)

	got, err := settings.StateDir()
	if err != nil {
		t.Fatal(err)
	}
	// t.TempDir on macOS is under /var, which is a symlink to /private/var;
	// comparing the resolved forms keeps the test honest without asserting
	// which one the platform hands back.
	if resolved, err := filepath.EvalSymlinks(got); err == nil {
		got = resolved
	}
	if want, err := filepath.EvalSymlinks(dir); err == nil {
		dir = want
	}
	if got != dir {
		t.Errorf("StateDir() = %q, want %q", got, dir)
	}
}

// Port 0 means "any free port". Only a harness that has asked for it may have
// it, because a real installation whose port moved on every restart would
// leave every paired phone dialling nothing.
func TestEphemeralPortIsRefusedUnlessAskedFor(t *testing.T) {
	set := settings.Default()
	set.Dest = filepath.Join(string(filepath.Separator), "tmp", "dest")
	set.Port = 0

	if err := set.Validate(); err == nil {
		t.Fatal("port 0 was accepted without AllowEphemeralPort")
	}

	set.AllowEphemeralPort = true
	if err := set.Validate(); err != nil {
		t.Fatalf("port 0 was refused even though it was asked for: %v", err)
	}
}

// A stored port must round trip exactly, including 0. Substituting the default
// for a value the ledger holds is how a receiver ends up on a different port
// than the one phones were paired against.
func TestStoredPortRoundTrips(t *testing.T) {
	for _, port := range []int{0, 1, 47891, 65535} {
		db := openDB(t)
		ctx := t.Context()

		set := settings.Default()
		set.Dest = t.TempDir()
		set.Port = port
		set.AllowEphemeralPort = true

		if err := settings.Save(ctx, db, set); err != nil {
			t.Fatalf("saving port %d: %v", port, err)
		}

		got, err := settings.Load(ctx, db)
		if err != nil {
			t.Fatalf("loading port %d: %v", port, err)
		}
		if got.Port != port {
			t.Errorf("port %d came back as %d", port, got.Port)
		}
	}
}

// A port that is not a port must be reported, not quietly replaced.
func TestUnreadableStoredPortIsAnError(t *testing.T) {
	for _, bad := range []string{"", "not-a-port", "-1", "70000"} {
		db := openDB(t)
		ctx := t.Context()

		if err := db.SetSetting(ctx, "desktop.port", bad); err != nil {
			t.Fatal(err)
		}
		if _, err := settings.Load(ctx, db); err == nil {
			t.Errorf("a stored port of %q was accepted", bad)
		}
	}
}
