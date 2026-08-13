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

// Package app is everything the desktop window can ask for.
//
// It holds no behaviour of its own. Receiving, pairing, naming, and storage
// are core's (AGENTS.md §2); what is here is the running receiver's lifecycle
// and the shape the window wants its answers in.
//
// The package deliberately does not import Wails. Every method on App can be
// called from a test with no window, which is what makes the phase gate
// something a script can drive rather than a screenshot somebody looked at.
// The three places that genuinely need the toolkit -- a folder picker, an
// event channel to the page, and the tray -- are interfaces, satisfied by the
// shell in main.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/geda/geda-transfer/core/events"
	"github.com/geda/geda-transfer/core/naming"
	"github.com/geda/geda-transfer/core/pairing"
	"github.com/geda/geda-transfer/core/service"
	"github.com/geda/geda-transfer/core/store"

	"github.com/geda/geda-transfer/desktop/internal/autostart"
	"github.com/geda/geda-transfer/desktop/internal/qrsvg"
	"github.com/geda/geda-transfer/desktop/internal/reveal"
	"github.com/geda/geda-transfer/desktop/internal/settings"
)

// EventTransfers is the name the window listens on for live snapshots.
const EventTransfers = "transfers"

// EventReceiver is emitted when the receiver starts, stops, or fails.
const EventReceiver = "receiver"

// Emitter delivers an event to the window. Implemented by the Wails runtime.
type Emitter interface {
	Emit(name string, data ...any)
}

// Chooser asks the user to pick a folder or some files. Implemented by the
// Wails dialog.
//
// It takes no context: the dialog needs the toolkit's own context, which the
// shell holds, and passing a second one would only invite the mistake of
// using the wrong one.
type Chooser interface {
	ChooseFolder(title, defaultPath string) (string, error)

	// ChooseFiles returns absolute paths, or nothing at all when the user
	// cancels. Cancelling is not an error and must not raise anything on
	// screen.
	ChooseFiles(title string) ([]string, error)
}

// nopEmitter is used before the window exists and in tests.
type nopEmitter struct{}

func (nopEmitter) Emit(string, ...any) {}

// Config is what main knows and this package does not.
type Config struct {
	// Version is stamped at build time and reported in Status.
	Version string

	// StateDir holds the ledger and the TLS identity.
	StateDir string

	// Logger receives operational messages.
	Logger *slog.Logger

	// AllowEphemeralPort lets the stored port be 0, meaning "any free port".
	//
	// Only the phase gate sets it, so that it can run on a machine that
	// already has a receiver on the product's port. A real installation must
	// not: a port that moved on every restart would leave every paired phone
	// dialling nothing.
	AllowEphemeralPort bool
}

// App is the window's view of the product.
type App struct {
	cfg Config
	log *slog.Logger

	emitter Emitter
	chooser Chooser

	live *live
	bus  *events.Bus

	// mu guards the running receiver and the settings it was built from.
	// Every exported method that touches either takes it, because a settings
	// save restarts the receiver and must not race a status query.
	mu      sync.Mutex
	svc     *service.Service
	set     settings.Settings
	cancel  context.CancelFunc
	stopped chan struct{}
	lastErr error

	// pumpCancel stops the live view's subscription when the app shuts down.
	pumpCancel context.CancelFunc
}

// New prepares an app. Nothing is started until Start is called.
func New(cfg Config) *App {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &App{
		cfg:     cfg,
		log:     cfg.Logger,
		emitter: nopEmitter{},
		live:    newLive(),
		bus:     events.NewBus(),
	}
}

// UseEmitter attaches the window's event channel.
func (a *App) UseEmitter(e Emitter) {
	if e != nil {
		a.emitter = e
	}
}

// UseChooser attaches the folder picker.
func (a *App) UseChooser(c Chooser) { a.chooser = c }

// Start reads the stored settings and brings the receiver up.
//
// A failure to serve is not a failure to start: the window has to open even
// when the port is taken or the destination has been unplugged, because the
// window is the only place the user can fix either. The error is kept and
// reported through Status.
func (a *App) Start(ctx context.Context) error {
	set, err := a.loadSettings(ctx)
	if err != nil {
		return err
	}

	pumpCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	a.pumpCancel = cancel
	go a.live.pump(pumpCtx, a.bus, func(s Snapshot) {
		a.emitter.Emit(EventTransfers, s)
	})

	a.mu.Lock()
	a.set = set
	a.mu.Unlock()

	if err := a.startReceiver(ctx, set); err != nil {
		a.log.Error("the receiver could not start", "error", err)
	}
	return nil
}

// loadSettings opens the ledger on its own to read the settings the receiver
// is then built from.
//
// The handle is closed before the service opens the same file: two handles on
// one SQLite database in one process is a lock conflict waiting for the first
// upload that arrives while the settings screen is open.
func (a *App) loadSettings(ctx context.Context) (settings.Settings, error) {
	db, err := store.Open(ctx, filepath.Join(a.cfg.StateDir, "ledger.db"))
	if err != nil {
		return settings.Settings{}, err
	}
	defer db.Close()

	set, err := settings.Load(ctx, db)
	if err != nil {
		return settings.Settings{}, err
	}
	// Not stored, so it has to be reapplied every time the settings are read.
	set.AllowEphemeralPort = a.cfg.AllowEphemeralPort

	// First run: write the defaults down, so that what the settings screen
	// shows is what is stored, and the destination folder exists before a
	// phone tries to send anything to it.
	if !set.Onboarded {
		if err := settings.Save(ctx, db, set); err != nil {
			return settings.Settings{}, err
		}
	}
	return set, nil
}

// startReceiver opens and runs a service. The caller must not hold mu.
func (a *App) startReceiver(ctx context.Context, set settings.Settings) error {
	cfg := set.ServiceConfig(a.cfg.StateDir, a.cfg.Version)
	cfg.Logger = a.log
	cfg.Events = a.bus

	svc, err := service.Open(context.WithoutCancel(ctx), cfg)
	if err != nil {
		a.mu.Lock()
		a.lastErr = err
		a.mu.Unlock()
		a.emitter.Emit(EventReceiver, map[string]any{"running": false, "error": err.Error()})
		return err
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stopped := make(chan struct{})

	a.mu.Lock()
	a.svc = svc
	a.cancel = cancel
	a.stopped = stopped
	a.lastErr = nil
	a.mu.Unlock()

	go func() {
		defer close(stopped)
		err := svc.Run(runCtx)
		if err != nil && runCtx.Err() == nil {
			a.log.Error("the receiver stopped", "error", err)
			a.mu.Lock()
			a.lastErr = err
			a.mu.Unlock()
			a.emitter.Emit(EventReceiver, map[string]any{"running": false, "error": err.Error()})
		}
	}()

	a.emitter.Emit(EventReceiver, map[string]any{"running": true})
	return nil
}

// stopReceiver brings the running service down and waits for it. Caller must
// not hold mu.
func (a *App) stopReceiver() {
	a.mu.Lock()
	svc, cancel, stopped := a.svc, a.cancel, a.stopped
	a.svc, a.cancel, a.stopped = nil, nil, nil
	a.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if stopped != nil {
		// Waited on rather than abandoned: the ledger must be closed before
		// the next service opens it, or the two overlap on the same file.
		<-stopped
	}
	if svc != nil {
		_ = svc.Close()
	}
}

// Shutdown stops everything. Safe to call more than once.
func (a *App) Shutdown() {
	if a.pumpCancel != nil {
		a.pumpCancel()
		a.pumpCancel = nil
	}
	a.stopReceiver()
}

// service returns the running receiver, or an error a person can act on.
func (a *App) service() (*service.Service, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.svc == nil {
		if a.lastErr != nil {
			return nil, a.lastErr
		}
		return nil, errors.New("the receiver is not running")
	}
	return a.svc, nil
}

// ---------------------------------------------------------------------------
// Bindings. Everything below is called from the window.
// ---------------------------------------------------------------------------

// StatusView is what the main screen shows about this machine.
type StatusView struct {
	// Running is false when the receiver could not start. Error says why, in
	// words the user can act on.
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`

	Name        string `json:"name"`
	DeviceID    string `json:"device_id"`
	Fingerprint string `json:"fingerprint"`
	Dest        string `json:"dest"`
	StateDir    string `json:"state_dir"`
	Version     string `json:"version"`

	Addrs []string `json:"addrs"`
	Port  int      `json:"port"`

	PairedDevices int   `json:"paired_devices"`
	Files         int   `json:"files"`
	Bytes         int64 `json:"bytes"`

	StartedAt time.Time `json:"started_at,omitzero"`

	// Onboarded is false until the user has finished the welcome screen.
	Onboarded bool `json:"onboarded"`
}

// Status reports what this machine is and what it holds.
func (a *App) Status() (StatusView, error) {
	a.mu.Lock()
	set, svc, lastErr := a.set, a.svc, a.lastErr
	a.mu.Unlock()

	view := StatusView{
		Name:      set.Name,
		Dest:      set.Dest,
		Port:      set.Port,
		StateDir:  a.cfg.StateDir,
		Version:   a.cfg.Version,
		Onboarded: set.Onboarded,
	}

	if svc == nil {
		if lastErr != nil {
			view.Error = lastErr.Error()
		}
		return view, nil
	}

	status, err := svc.Status(context.Background())
	if err != nil {
		view.Error = err.Error()
		return view, nil
	}

	view.Running = true
	view.Name = status.Name
	view.DeviceID = status.DeviceID
	view.Fingerprint = status.Fingerprint
	view.Dest = status.Dest
	view.Addrs = status.Addrs
	view.PairedDevices = status.PairedDevices
	view.Files = status.Files
	view.Bytes = status.Bytes
	view.StartedAt = status.StartedAt
	if lastErr != nil {
		view.Error = lastErr.Error()
	}
	return view, nil
}

// PairView is a pairing invitation, ready to put on screen.
type PairView struct {
	// SVG is the code itself. The URI is shown underneath it so a phone that
	// cannot scan -- a cracked camera, a bad angle -- still has a way in.
	SVG string `json:"svg"`
	URI string `json:"uri"`

	// Fingerprint is the short form of the key, for the user to compare
	// against what the phone shows after scanning.
	Fingerprint string `json:"fingerprint"`

	Addrs     []string  `json:"addrs"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Pair issues a single-use pairing code.
//
// The code expires and is spent on first use, so the window asks for a fresh
// one every time the pairing screen is opened rather than caching it.
func (a *App) Pair() (PairView, error) {
	svc, err := a.service()
	if err != nil {
		return PairView{}, err
	}

	offer, err := svc.Pair(context.Background(), pairing.DefaultOfferTTL)
	if err != nil {
		return PairView{}, err
	}

	svg, err := qrsvg.Encode(offer.URI)
	if err != nil {
		return PairView{}, err
	}

	return PairView{
		SVG:         svg,
		URI:         offer.URI,
		Fingerprint: offer.Fingerprint,
		Addrs:       offer.Addrs,
		ExpiresAt:   offer.ExpiresAt,
	}, nil
}

// CancelPairing withdraws the outstanding code.
//
// Called when the pairing screen closes: a code left live is a credential the
// user believes they have put away.
func (a *App) CancelPairing() {
	if svc, err := a.service(); err == nil {
		svc.CancelPairing()
	}
}

// Devices lists every device that has paired.
func (a *App) Devices() ([]service.Device, error) {
	svc, err := a.service()
	if err != nil {
		return nil, err
	}
	devices, err := svc.Devices(context.Background())
	if err != nil {
		return nil, err
	}
	if devices == nil {
		devices = []service.Device{}
	}
	return devices, nil
}

// Unpair revokes a device's access. Its files are left where they are.
func (a *App) Unpair(deviceID string) error {
	svc, err := a.service()
	if err != nil {
		return err
	}
	return svc.Unpair(context.Background(), deviceID)
}

// History lists received files, newest first.
//
// before is an RFC3339 timestamp for paging, or empty for the first page. The
// window passes back the timestamp of its last row rather than an offset, so a
// file arriving mid-scroll cannot make a row appear twice.
//
// limit is the caller's page size, and it is the caller's because the window
// needs it back: a page shorter than the limit is how it knows there is no
// more to fetch, and hiding "Show more" only after a click that returns
// nothing shows a control that does nothing.
func (a *App) History(deviceID, before string, limit int) ([]service.HistoryEntry, error) {
	svc, err := a.service()
	if err != nil {
		return nil, err
	}

	q := service.HistoryQuery{DeviceID: deviceID, Limit: limit}
	if strings.TrimSpace(before) != "" {
		t, err := time.Parse(time.RFC3339Nano, before)
		if err != nil {
			return nil, fmt.Errorf("%q is not a timestamp: %w", before, err)
		}
		q.Before = t
	}

	entries, err := svc.History(context.Background(), q)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []service.HistoryEntry{}
	}
	return entries, nil
}

// Transfers is the current live snapshot.
//
// The window is pushed snapshots as they change; this is what it calls once on
// load, so a window opened mid-transfer is not blank until the next event.
func (a *App) Transfers() Snapshot {
	return a.live.snapshot(time.Now())
}

// SettingsView is the settings screen's model.
type SettingsView struct {
	settings.Settings

	// TemplateVariables and TemplatePreview let the screen explain the
	// template without the user having to try one and see what happens.
	TemplateVariables []string `json:"template_variables"`
	TemplatePreview   string   `json:"template_preview"`

	// AutostartSupported is false where the OS offers no per-user login item,
	// so the screen can say why the control is missing instead of showing one
	// that does nothing.
	AutostartSupported bool `json:"autostart_supported"`

	// DefaultTemplate is offered as a reset.
	DefaultTemplate string `json:"default_template"`
}

// Settings returns the current settings.
func (a *App) Settings() (SettingsView, error) {
	a.mu.Lock()
	set := a.set
	a.mu.Unlock()

	// The stored value is the authority for the checkbox, but the OS is the
	// authority for what is actually configured: a user who removed the login
	// item in System Settings must not be shown a ticked box.
	if enabled, err := autostart.Enabled(); err == nil {
		set.Autostart = enabled
	}

	preview, err := service.TemplatePreview(set.Template)
	if err != nil {
		preview = ""
	}

	return SettingsView{
		Settings:           set,
		TemplateVariables:  naming.Variables,
		TemplatePreview:    preview,
		AutostartSupported: autostartSupported(),
		DefaultTemplate:    settings.DefaultTemplate,
	}, nil
}

// SaveSettings validates, stores, and applies new settings.
//
// Anything the running receiver cannot absorb -- a new port, a new
// destination -- means restarting it. That is quick and in-process, and the
// identity and the ledger are on disk, so no phone has to re-pair.
func (a *App) SaveSettings(next settings.Settings) (SettingsView, error) {
	next.Dest = strings.TrimSpace(next.Dest)
	next.Name = strings.TrimSpace(next.Name)
	next.Template = strings.TrimSpace(next.Template)
	// Onboarding is not a preference the screen owns; only FinishOnboarding
	// sets it, and a settings save must not be able to clear it.
	a.mu.Lock()
	previous := a.set
	a.mu.Unlock()
	next.Onboarded = previous.Onboarded
	next.AllowEphemeralPort = a.cfg.AllowEphemeralPort

	if err := next.Validate(); err != nil {
		return SettingsView{}, err
	}

	if err := a.persist(next); err != nil {
		return SettingsView{}, err
	}

	// The login item is the OS's state, not the ledger's, so it is applied
	// separately and a failure there must not lose the rest of the save.
	var autostartErr error
	if next.Autostart != previous.Autostart {
		autostartErr = autostart.Set(next.Autostart)
	}

	a.mu.Lock()
	a.set = next
	a.mu.Unlock()

	if settings.NeedsRestart(previous, next) {
		a.stopReceiver()
		if err := a.startReceiver(context.Background(), next); err != nil {
			// The settings are saved and the receiver is down. Reporting the
			// reason is the only way the user can fix it -- and the values
			// they typed are kept, so they can correct one field rather than
			// starting again.
			return a.settingsView(next), fmt.Errorf("saved, but the receiver could not restart: %w", err)
		}
	} else if err := a.setTemplate(next.Template); err != nil {
		return a.settingsView(next), err
	}

	return a.settingsView(next), autostartErr
}

// settingsView renders a view without re-reading the OS. Errors from the
// preview are swallowed: the template has already been validated.
func (a *App) settingsView(set settings.Settings) SettingsView {
	preview, _ := service.TemplatePreview(set.Template)
	return SettingsView{
		Settings:           set,
		TemplateVariables:  naming.Variables,
		TemplatePreview:    preview,
		AutostartSupported: autostartSupported(),
		DefaultTemplate:    settings.DefaultTemplate,
	}
}

// persist writes settings to the ledger, using the running service's handle
// when there is one and opening its own when there is not.
func (a *App) persist(set settings.Settings) error {
	ctx := context.Background()

	if svc, err := a.service(); err == nil {
		return settings.Save(ctx, svc.DB(), set)
	}

	db, err := store.Open(ctx, filepath.Join(a.cfg.StateDir, "ledger.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	return settings.Save(ctx, db, set)
}

// setTemplate applies a template change to the running receiver.
func (a *App) setTemplate(tmpl string) error {
	svc, err := a.service()
	if err != nil {
		return nil // nothing running to tell; the ledger already has it
	}
	return svc.SetTemplate(context.Background(), tmpl)
}

// PreviewTemplate renders a template against a representative asset, so the
// settings screen can show the effect of an edit before it is saved.
func (a *App) PreviewTemplate(tmpl string) (string, error) {
	return service.TemplatePreview(strings.TrimSpace(tmpl))
}

// ChooseDestination opens a folder picker and returns what was chosen.
//
// An empty string with no error means the user cancelled, which is not a
// failure and must not raise anything on screen.
func (a *App) ChooseDestination() (string, error) {
	if a.chooser == nil {
		return "", errors.New("no folder picker is available")
	}

	a.mu.Lock()
	current := a.set.Dest
	a.mu.Unlock()

	return a.chooser.ChooseFolder("Where should received files go?", current)
}

// OpenDestination shows the destination folder in the file manager.
func (a *App) OpenDestination() error {
	a.mu.Lock()
	dest := a.set.Dest
	a.mu.Unlock()
	return reveal.Dir(context.Background(), dest)
}

// RevealFile shows one received file in the file manager.
//
// storedPath is destination-relative, as the ledger holds it. It is resolved
// through the store rather than joined here, so a path that tried to climb out
// of the destination cannot.
func (a *App) RevealFile(storedPath string) error {
	svc, err := a.service()
	if err != nil {
		return err
	}
	abs := svc.AbsPath(storedPath)

	dest := svc.Dest()
	rel, err := filepath.Rel(dest, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s is not inside the destination folder", storedPath)
	}
	return reveal.File(context.Background(), abs)
}

// FinishOnboarding records that the user has been through the welcome screen.
func (a *App) FinishOnboarding() error {
	a.mu.Lock()
	set := a.set
	a.mu.Unlock()

	if set.Onboarded {
		return nil
	}
	set.Onboarded = true

	if err := a.persist(set); err != nil {
		return err
	}

	a.mu.Lock()
	a.set = set
	a.mu.Unlock()
	return nil
}

// Bus exposes the event bus, for the shell to attach the tray to.
func (a *App) Bus() *events.Bus { return a.bus }
