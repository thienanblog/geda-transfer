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
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// On Windows the entry is a value under the per-user Run key.
//
// HKEY_CURRENT_USER rather than HKEY_LOCAL_MACHINE: the machine-wide key
// starts the app for every account on the computer and needs an administrator
// to write, neither of which is what a person ticking a box in a transfer app
// is asking for. The Startup *folder* was the alternative and was rejected --
// an entry there is a .lnk shortcut, which can only be written through COM.
const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// Enabled reports whether the app is set to start at login.
func Enabled() (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open the Run key: %w", err)
	}
	defer key.Close()

	value, _, err := key.GetStringValue(Label)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the Run entry: %w", err)
	}
	return strings.TrimSpace(value) != "", nil
}

// Set turns the Run entry on or off.
func Set(enabled bool) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open the Run key: %w", err)
	}
	defer key.Close()

	if !enabled {
		if err := key.DeleteValue(Label); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("remove the Run entry: %w", err)
		}
		return nil
	}

	exe, err := executable()
	if err != nil {
		return err
	}
	// Quoted because Program Files has a space in it and an unquoted path is
	// split on the first one, which starts "C:\Program" and fails.
	command := `"` + exe + `" --background`
	if err := key.SetStringValue(Label, command); err != nil {
		return fmt.Errorf("write the Run entry: %w", err)
	}
	return nil
}
