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

// Command desktop is the Geda Transfer app for macOS and Windows.
//
// It is a window over core/ and nothing more (AGENTS.md §2): the receiver it
// runs is core/service, the same one gedad runs on a NAS. What is here is the
// shell -- a window, a tray icon, a folder picker, and the wiring that lets
// the page be told when a transfer moves.
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/geda/geda-transfer/desktop/internal/app"
	"github.com/geda/geda-transfer/desktop/internal/settings"
)

//go:embed all:frontend/dist
var assets embed.FS

// version is stamped at build time:
//
//	wails build -ldflags "-X main.version=$(git describe --tags --always)"
var version = "dev"

func main() {
	background := flag.Bool("background", false,
		"start without showing the window, as the login item does")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("Geda Transfer", version)
		return
	}

	if err := run(*background); err != nil {
		fmt.Fprintln(os.Stderr, "Geda Transfer:", err)
		os.Exit(1)
	}
}

func run(background bool) error {
	stateDir, err := settings.StateDir()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	application := app.New(app.Config{
		Version:  version,
		StateDir: stateDir,
		Logger:   log,
	})

	sh := &shell{app: application, log: log}

	title := "Geda Transfer"
	if settings.IsDev() {
		// So a developer can tell at a glance which window is which, and does
		// not report a bug against the wrong one.
		title += " (dev)"
	}

	return wails.Run(&options.App{
		Title:  title,
		Width:  1040,
		Height: 720,
		// Small enough for half a laptop screen. Below this the transfer list
		// stops being readable, so the window refuses rather than reflowing
		// into something nobody can use.
		MinWidth:  760,
		MinHeight: 560,

		AssetServer: &assetserver.Options{Assets: assets},

		OnStartup:     sh.startup,
		OnShutdown:    sh.shutdown,
		OnBeforeClose: sh.beforeClose,

		// Closing the window leaves the receiver running, because a desktop
		// cannot be woken by a phone: if the app is not running there is
		// nothing to discover and nothing to send to (AGENTS.md §3.7). The
		// tray icon is what makes this honest -- the app is visibly still
		// there, and Quit is one click away.
		HideWindowOnClose: true,
		StartHidden:       background,

		// Two instances would open the same ledger and both answer pairing
		// requests. The second one hands over and brings this window forward
		// instead, which is also what a user double-clicking the icon while it
		// is already running expects.
		SingleInstanceLock: &options.SingleInstanceLock{
			// Qualified by the build variant so a `wails dev` run and the
			// installed app do not lock each other out -- they have separate
			// state and are separate programs as far as this is concerned.
			UniqueId:               "app.geda.transfer" + settings.Variant(),
			OnSecondInstanceLaunch: sh.secondInstance,
		},

		Bind: []any{application},

		Mac: &mac.Options{
			TitleBar:   mac.TitleBarHiddenInset(),
			Appearance: mac.DefaultAppearance,
			About: &mac.AboutInfo{
				Title:   "Geda Transfer " + version,
				Message: "Fast local transfer of photos, videos, and files.\nNo cloud, no account.\n\nApache-2.0",
			},
		},
	})
}

// shell holds the pieces that only exist while a window does.
type shell struct {
	app *app.App
	log *slog.Logger

	ctx     context.Context
	tray    *tray
	quitted bool
}

func (s *shell) startup(ctx context.Context) {
	s.ctx = ctx

	s.app.UseEmitter(emitter{ctx: ctx})
	s.app.UseChooser(chooser{ctx: ctx})

	if err := s.app.Start(ctx); err != nil {
		s.log.Error("could not start", "error", err)
		return
	}

	s.tray = newTray(s.app, trayActions{
		Show: func() { wruntime.Show(s.ctx) },
		Quit: func() {
			s.quitted = true
			wruntime.Quit(s.ctx)
		},
	}, s.log)
	s.tray.start()
}

func (s *shell) shutdown(context.Context) {
	if s.tray != nil {
		s.tray.stop()
	}
	s.app.Shutdown()
}

// beforeClose keeps the app alive when the window's close button is used.
//
// Quit from the tray or the application menu sets quitted first, so the two
// are not confused: one hides a window, the other stops receiving.
func (s *shell) beforeClose(context.Context) bool {
	return !s.quitted
}

func (s *shell) secondInstance(options.SecondInstanceData) {
	wruntime.Show(s.ctx)
}

// emitter sends an event to the page.
type emitter struct{ ctx context.Context }

func (e emitter) Emit(name string, data ...any) {
	wruntime.EventsEmit(e.ctx, name, data...)
}

// chooser opens the platform's folder picker.
type chooser struct{ ctx context.Context }

func (c chooser) ChooseFiles(title string) ([]string, error) {
	return wruntime.OpenMultipleFilesDialog(c.ctx, wruntime.OpenDialogOptions{
		Title: title,
	})
}

func (c chooser) ChooseFolder(title, defaultPath string) (string, error) {
	return wruntime.OpenDirectoryDialog(c.ctx, wruntime.OpenDialogOptions{
		Title:                title,
		DefaultDirectory:     defaultPath,
		CanCreateDirectories: true,
	})
}
