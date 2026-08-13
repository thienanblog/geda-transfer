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

//go:build !darwin && !windows

package autostart

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Elsewhere -- which in practice means a Linux desktop -- the entry is an XDG
// autostart desktop file. The headless case on Linux is gedad, which is
// started by systemd and has nothing to do with this.
func path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find the configuration directory: %w", err)
	}
	return filepath.Join(dir, "autostart", Label+".desktop"), nil
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
		return false, fmt.Errorf("read the autostart entry: %w", err)
	}
	return true, nil
}

// Set turns the autostart entry on or off.
func Set(enabled bool) error {
	p, err := path()
	if err != nil {
		return err
	}

	if !enabled {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove the autostart entry: %w", err)
		}
		return nil
	}

	exe, err := executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("prepare the autostart directory: %w", err)
	}

	entry := strings.Join([]string{
		"[Desktop Entry]",
		"Type=Application",
		"Name=" + DisplayName,
		"Exec=" + quote(exe) + " --background",
		"Terminal=false",
		"X-GNOME-Autostart-enabled=true",
		"",
	}, "\n")

	if err := os.WriteFile(p, []byte(entry), 0o644); err != nil {
		return fmt.Errorf("write the autostart entry: %w", err)
	}
	return nil
}

// quote wraps a path for the Exec key, which is whitespace-separated.
func quote(path string) string {
	if !strings.ContainsAny(path, ` "\`) {
		return path
	}
	replaced := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(path)
	return `"` + replaced + `"`
}
