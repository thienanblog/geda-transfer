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

// Package reveal hands a path to the system file manager.
//
// The point of the whole product is that files end up somewhere the user can
// find them, so "show me" has to be one click from the transfer that just
// finished -- not a path the user is expected to copy and paste.
package reveal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// timeout bounds the helper process. It only has to ask the file manager to
// come to the front; if it has not returned by then something is wrong and
// blocking a UI callback forever is not the answer.
const timeout = 10 * time.Second

// File opens the file manager with path selected.
func File(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("that file is no longer there: %w", err)
	}
	return run(ctx, selectCommand(path))
}

// Dir opens path itself as a folder.
func Dir(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("that folder is no longer there: %w", err)
	}
	return run(ctx, openCommand(path))
}

func selectCommand(path string) []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"open", "-R", path}
	case "windows":
		// No space after the comma: explorer parses "/select,<path>" as one
		// argument and silently opens Documents if it is split in two.
		return []string{"explorer", "/select," + path}
	default:
		return []string{"xdg-open", path}
	}
}

func openCommand(path string) []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"open", path}
	case "windows":
		return []string{"explorer", path}
	default:
		return []string{"xdg-open", path}
	}
}

func run(ctx context.Context, argv []string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The path is passed as an argument to a known program, never through a
	// shell, so a filename containing shell metacharacters is just a filename.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if err := cmd.Run(); err != nil {
		// explorer.exe returns a non-zero status even when it succeeds, which
		// would otherwise show the user an error over a window that opened
		// correctly.
		if runtime.GOOS == "windows" {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return nil
			}
		}
		return fmt.Errorf("open %s: %w", argv[0], err)
	}
	return nil
}
