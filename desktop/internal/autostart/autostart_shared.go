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
	"fmt"
	"os"
	"path/filepath"
)

// executable is the path the OS should launch.
//
// It is resolved through symlinks because a Homebrew or a development build is
// usually reached through one, and an entry pointing at a symlink that a later
// upgrade replaces would start the wrong binary or none at all.
func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find this program: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}
