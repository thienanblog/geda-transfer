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

// Package settings stores the desktop app's preferences in the ledger.
//
// There is deliberately no configuration file. gedad has one because a NAS is
// administered over SSH, and its format is a decision already taken
// (docs/DECISIONS.md). A desktop app is administered through its own window,
// and a second file that the window and a text editor could both write would
// only create a question about which of them wins.
//
// The ledger is already the thing that must survive updates, so the settings
// live there, next to the device id which is stored for the same reason.
package settings

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/geda/geda-transfer/core/discovery"
	"github.com/geda/geda-transfer/core/naming"
	"github.com/geda/geda-transfer/core/service"
	"github.com/geda/geda-transfer/core/store"
)

// Keys are prefixed so they cannot collide with anything core stores in the
// same table.
const (
	keyName      = "desktop.name"
	keyDest      = "desktop.dest"
	keyPort      = "desktop.port"
	keyAdvertise = "desktop.advertise"
	keyMDNS      = "desktop.mdns"
	keyDiscovery = "desktop.discovery"
	keyAutostart = "desktop.autostart"
	keyOnboarded = "desktop.onboarded"
)

// Settings is everything the user can change from the app's own window.
//
// The naming template is not here: core owns it, validates it, and stores it
// under its own key, so that gedad and the desktop cannot disagree about what
// a template means.
type Settings struct {
	// Name is what phones see when choosing a destination.
	Name string `json:"name"`

	// Dest is the folder received files are written to.
	Dest string `json:"dest"`

	// Port is the TCP port transfers are accepted on.
	Port int `json:"port"`

	// Advertise overrides the address set phones are told to try. Empty means
	// every local interface address, which is what a desktop wants and why
	// this is behind the advanced section.
	Advertise []string `json:"advertise"`

	// MDNS and Discovery run the L1 and L2-L5 responders.
	MDNS      bool `json:"mdns"`
	Discovery bool `json:"discovery"`

	// Autostart asks the OS to launch the app at login.
	Autostart bool `json:"autostart"`

	// Onboarded records that the user has finished the first-run screen. It
	// is not a preference; it is why the app does not show the welcome again.
	Onboarded bool `json:"onboarded"`

	// Template is the filename template. It is carried here so the settings
	// screen is one object, but it is read from and written to core's key.
	Template string `json:"template"`

	// AllowEphemeralPort permits Port to be 0, meaning "any free port".
	//
	// It exists for the phase gate, which has to run on a machine that may
	// already have a receiver on the product's port -- and in CI, twice at
	// once. It is not serialised, so no window can offer it and no ledger can
	// hold it: a real installation that took an ephemeral port would leave
	// every paired phone dialling a port nothing is listening on.
	AllowEphemeralPort bool `json:"-"`
}

// Default is what a machine that has never been configured uses.
//
// Every field has a working value: the app has to transfer files the moment
// it is installed, with nothing filled in (AGENTS.md §4).
func Default() Settings {
	return Settings{
		Name:      service.DefaultName(),
		Dest:      DefaultDest(),
		Port:      discovery.DefaultTransferPort,
		MDNS:      true,
		Discovery: true,
		Template:  DefaultTemplate,
	}
}

// DefaultTemplate files each device's photos in its own folder, by date.
//
// It differs from core's default, which has no directory part, because a
// desktop receives from a household's worth of phones into a folder the user
// browses in Finder. "Per-device folders" is the phase's requirement
// (docs/PLAN.md P6) and this is where it is expressed: one template, applied
// by the same engine everything else uses.
const DefaultTemplate = "{device}/{yyyy}/{yyyy}-{MM}-{dd}_{HH}{mm}{ss}_{original_name}.{ext}"

// DefaultDest is the folder a person would look in without being told.
func DefaultDest() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "Geda Transfer"
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		// Both platforms have a Pictures folder that the file manager gives a
		// place of its own in the sidebar.
		if pictures := filepath.Join(home, "Pictures"); isDir(pictures) {
			return filepath.Join(pictures, "Geda Transfer")
		}
	}
	return filepath.Join(home, "Geda Transfer")
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// EnvStateDir overrides where the ledger and the identity live.
//
// It exists so the phase gate can drive a real receiver without touching the
// state of the app the developer actually uses, and it is the same variable
// gedad honours. It is deliberately not a setting: the app has to know where
// its state is before it can read any setting out of it.
const EnvStateDir = "GEDA_STATE_DIR"

// StateDir is where the ledger and the TLS identity live.
//
// Losing it makes every paired phone fail with a pin mismatch that has no
// override (AGENTS.md §3.5), so it is under the user's config directory rather
// than anywhere an uninstall or an update would sweep.
func StateDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv(EnvStateDir)); override != "" {
		abs, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", EnvStateDir, err)
		}
		return abs, nil
	}

	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find the configuration directory: %w", err)
	}
	// A development build lands beside the installed app's state, never in it.
	// An explicit override above is honoured exactly as given: somebody who
	// named a directory meant that directory.
	return filepath.Join(dir, "geda"+variant), nil
}

// Variant distinguishes a development build from a shipped one.
//
// It is empty for a release and "-dev" under `wails dev`, and it qualifies
// everything that must not be shared between the two: the state directory and
// the single-instance lock.
func Variant() string { return variant }

// IsDev reports whether this is a development build.
func IsDev() bool { return variant != "" }

// Load reads the settings, falling back to Default for anything unset.
func Load(ctx context.Context, db *store.DB) (Settings, error) {
	s := Default()

	get := func(key string) (string, bool, error) {
		v, err := db.Setting(ctx, key)
		if errors.Is(err, store.ErrNotFound) {
			return "", false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("read setting %s: %w", key, err)
		}
		return v, true, nil
	}

	for key, apply := range map[string]func(string){
		keyName:      func(v string) { s.Name = v },
		keyDest:      func(v string) { s.Dest = v },
		keyAdvertise: func(v string) { s.Advertise = splitList(v) },
	} {
		v, ok, err := get(key)
		if err != nil {
			return Settings{}, err
		}
		if ok && strings.TrimSpace(v) != "" {
			apply(v)
		}
	}

	if v, ok, err := get(keyPort); err != nil {
		return Settings{}, err
	} else if ok {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 0 || n > 65535 {
			// Refused rather than quietly replaced with the default. A
			// receiver listening on a port other than the one the ledger
			// records is the kind of mismatch that is discovered days later,
			// by which time phones have been paired against the wrong one.
			return Settings{}, fmt.Errorf("the stored port %q is not a port number", v)
		}
		s.Port = n
	}

	for key, apply := range map[string]func(bool){
		keyMDNS:      func(v bool) { s.MDNS = v },
		keyDiscovery: func(v bool) { s.Discovery = v },
		keyAutostart: func(v bool) { s.Autostart = v },
		keyOnboarded: func(v bool) { s.Onboarded = v },
	} {
		v, ok, err := get(key)
		if err != nil {
			return Settings{}, err
		}
		if ok {
			apply(v == "1" || strings.EqualFold(v, "true"))
		}
	}

	// The template is core's, under core's key, so that gedad reading the same
	// ledger sees the same value.
	tmpl, err := db.Setting(ctx, templateKey)
	switch {
	case errors.Is(err, store.ErrNotFound), err == nil && strings.TrimSpace(tmpl) == "":
		s.Template = DefaultTemplate
	case err != nil:
		return Settings{}, fmt.Errorf("read the filename template: %w", err)
	default:
		s.Template = tmpl
	}

	return s, nil
}

// Save validates and stores the settings.
//
// Validation is here rather than at the point of use because these values are
// applied by restarting the receiver: a port that cannot be bound or a
// template that cannot render would take the app down on save and leave the
// user with a window and no receiver behind it.
func Save(ctx context.Context, db *store.DB, s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}

	values := map[string]string{
		keyName:      strings.TrimSpace(s.Name),
		keyDest:      s.Dest,
		keyPort:      strconv.Itoa(s.Port),
		keyAdvertise: strings.Join(s.Advertise, ","),
		keyMDNS:      boolValue(s.MDNS),
		keyDiscovery: boolValue(s.Discovery),
		keyAutostart: boolValue(s.Autostart),
		keyOnboarded: boolValue(s.Onboarded),
		templateKey:  strings.TrimSpace(s.Template),
	}
	for key, value := range values {
		if err := db.SetSetting(ctx, key, value); err != nil {
			return fmt.Errorf("save setting %s: %w", key, err)
		}
	}
	return nil
}

// templateKey is core's, not this package's.
const templateKey = "naming_template"

// Validate rejects settings that cannot work, with a message a person can act
// on rather than a wrapped system error.
func (s Settings) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("the receiver needs a name; phones show it when choosing where to send")
	}
	if strings.TrimSpace(s.Dest) == "" {
		return errors.New("choose a folder for received files")
	}
	if !filepath.IsAbs(s.Dest) {
		return fmt.Errorf("%s is not a full path to a folder", s.Dest)
	}
	if s.Port < 0 || s.Port > 65535 {
		return fmt.Errorf("%d is not a port number", s.Port)
	}
	if s.Port == 0 && !s.AllowEphemeralPort {
		// Port 0 means "any free port", which works but changes on every
		// restart -- and paired phones hold the port they were given, so it
		// would silently make every one of them unable to connect. Only a
		// test harness wants it, and it has to ask.
		return errors.New("choose a port; 0 would change every time the app starts")
	}
	if err := naming.Validate(strings.TrimSpace(s.Template)); err != nil {
		return err
	}
	return nil
}

// ServiceConfig turns settings into what core needs to open a receiver.
func (s Settings) ServiceConfig(stateDir, version string) service.Config {
	return service.Config{
		Name:           strings.TrimSpace(s.Name),
		Version:        version,
		StateDir:       stateDir,
		Dest:           s.Dest,
		Listen:         ":" + strconv.Itoa(s.Port),
		Advertise:      s.Advertise,
		MDNS:           s.MDNS,
		Discovery:      s.Discovery,
		NamingTemplate: strings.TrimSpace(s.Template),
	}
}

// NeedsRestart reports whether moving from a to b requires the receiver to be
// rebuilt, as opposed to being a change the running one can absorb.
//
// Only the template can be changed in place: storage reads it from the ledger
// on every commit. Everything else was handed to the receiver when it was
// built.
func NeedsRestart(a, b Settings) bool {
	if a.Name != b.Name || a.Dest != b.Dest || a.Port != b.Port {
		return true
	}
	if a.MDNS != b.MDNS || a.Discovery != b.Discovery {
		return true
	}
	if len(a.Advertise) != len(b.Advertise) {
		return true
	}
	for i := range a.Advertise {
		if a.Advertise[i] != b.Advertise[i] {
			return true
		}
	}
	return false
}

func boolValue(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
