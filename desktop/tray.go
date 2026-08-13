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

package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/energye/systray"

	"github.com/geda/geda-transfer/desktop/internal/app"
	"github.com/geda/geda-transfer/desktop/internal/icons"
)

// tray is the menu-bar or notification-area presence.
//
// It exists because the window is closable while the receiver keeps running.
// Without something visible, an app that stays resident after its window is
// dismissed is indistinguishable from one that leaked -- and the user has no
// way to stop it or to get the window back.
type tray struct {
	app     *app.App
	actions trayActions
	log     *slog.Logger

	stopFn func()

	mu     sync.Mutex
	status *systray.MenuItem
	cancel context.CancelFunc
}

// trayActions are the things only the shell can do.
type trayActions struct {
	Show func()
	Quit func()
}

func newTray(a *app.App, actions trayActions, log *slog.Logger) *tray {
	return &tray{app: a, actions: actions, log: log}
}

// start registers the icon.
//
// Register rather than Run: Run creates and owns an event loop, and on macOS
// that means a second NSApplication fighting the one the window already has.
// Register attaches to the loop that is already there.
func (t *tray) start() {
	systray.Register(t.onReady, nil)
}

func (t *tray) stop() {
	t.mu.Lock()
	cancel := t.cancel
	t.cancel = nil
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if t.stopFn != nil {
		t.stopFn()
	}
	systray.Quit()
}

func (t *tray) onReady() {
	if icons.TrayTemplate() {
		// A template image is recoloured by macOS for the menu bar's
		// appearance. Passing a coloured icon here makes it invisible against
		// a dark menu bar.
		systray.SetTemplateIcon(icons.Tray(), icons.Tray())
	} else {
		systray.SetIcon(icons.Tray())
	}
	systray.SetTooltip("Geda Transfer")

	status := systray.AddMenuItem("Starting…", "")
	status.Disable()
	t.mu.Lock()
	t.status = status
	t.mu.Unlock()

	systray.AddSeparator()

	open := systray.AddMenuItem("Open Geda Transfer", "Show the window")
	open.Click(t.actions.Show)

	folder := systray.AddMenuItem("Open Received Folder", "Show where files are saved")
	folder.Click(func() {
		if err := t.app.OpenDestination(); err != nil {
			t.log.Warn("could not open the destination folder", "error", err)
		}
	})

	systray.AddSeparator()

	quit := systray.AddMenuItem("Quit", "Stop receiving and quit")
	quit.Click(t.actions.Quit)

	// Clicking the icon itself opens the window, which is what people try
	// first on both platforms.
	systray.SetOnClick(func(systray.IMenu) { t.actions.Show() })

	ctx, cancel := context.WithCancel(context.Background())
	t.mu.Lock()
	t.cancel = cancel
	t.mu.Unlock()
	go t.follow(ctx)
}

// trayRefresh is how often the tray's one line of text is rebuilt.
//
// Deliberately far slower than the window's updates: this is a summary
// somebody glances at, and a menu-bar item that changes five times a second is
// a distraction, not information.
const trayRefresh = time.Second

// follow keeps the status line current.
func (t *tray) follow(ctx context.Context) {
	ticker := time.NewTicker(trayRefresh)
	defer ticker.Stop()

	last := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			line := t.summary()
			if line == last {
				continue
			}
			last = line

			t.mu.Lock()
			status := t.status
			t.mu.Unlock()
			if status != nil {
				status.SetTitle(line)
			}
		}
	}
}

// summary is the one line the tray shows.
func (t *tray) summary() string {
	snapshot := t.app.Transfers()
	if n := len(snapshot.Active); n > 0 {
		if snapshot.BytesPerSecond > 0 {
			return fmt.Sprintf("Receiving %s — %s/s", plural(n, "file"), humanBytes(int64(snapshot.BytesPerSecond)))
		}
		return "Receiving " + plural(n, "file")
	}

	status, err := t.app.Status()
	if err != nil || !status.Running {
		return "Not receiving"
	}
	if status.PairedDevices == 0 {
		return "Ready — no devices paired yet"
	}
	return fmt.Sprintf("Ready — %s", plural(status.PairedDevices, "device"))
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// humanBytes formats a size in the decimal units a network link is sold in.
func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}
