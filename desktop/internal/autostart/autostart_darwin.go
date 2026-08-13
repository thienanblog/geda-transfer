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

package autostart

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
)

// On macOS the entry is a LaunchAgent in the user's own library.
//
// A LaunchAgent rather than a Login Item because it is a file this app can
// write, read back, and remove -- so the checkbox in settings can show the
// true state. Login Items are managed through a framework whose modern form
// (SMAppService) needs the app to be a signed, notarised bundle in
// /Applications, which is true of a release and not of a developer build, and
// a checkbox that silently fails in development is one that gets shipped
// broken.
func path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find the home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist"), nil
}

// Enabled reports whether the app is set to start at login.
func Enabled() (bool, error) {
	p, err := path()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the login item: %w", err)
	}
	return true, nil
}

// Set turns the login item on or off.
func Set(enabled bool) error {
	p, err := path()
	if err != nil {
		return err
	}

	if !enabled {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove the login item: %w", err)
		}
		return nil
	}

	exe, err := executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("prepare the LaunchAgents directory: %w", err)
	}

	plist, err := agentPlist(exe)
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, plist, 0o644); err != nil {
		return fmt.Errorf("write the login item: %w", err)
	}
	return nil
}

// agentPlist builds the launchd job description.
//
// KeepAlive is deliberately absent: launchd restarting the app the moment the
// user quits it would make Quit look broken. RunAtLoad is the whole feature.
func agentPlist(exe string) ([]byte, error) {
	// The path is escaped as XML text rather than pasted into a template, so
	// that a user whose account name contains an ampersand still gets a plist
	// launchd can parse.
	program, err := xmlText(exe)
	if err != nil {
		return nil, err
	}
	label, err := xmlText(Label)
	if err != nil {
		return nil, err
	}

	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>--background</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`, label, program)
	return []byte(body), nil
}

func xmlText(s string) (string, error) {
	var out bytes.Buffer
	if err := xml.EscapeText(&out, []byte(s)); err != nil {
		return "", fmt.Errorf("escape %q: %w", s, err)
	}
	return out.String(), nil
}
