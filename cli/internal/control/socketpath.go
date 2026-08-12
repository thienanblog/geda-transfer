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

package control

import (
	"fmt"
	"runtime"
)

// maxSocketPath is the length of sun_path in struct sockaddr_un, minus the
// terminating NUL. It is a kernel constant, not a filesystem limit, and a path
// that exceeds it fails with a bare "invalid argument" that says nothing about
// the cause -- so check it here and say what to do about it.
func maxSocketPath() int {
	switch runtime.GOOS {
	case "linux", "android":
		return 107
	default:
		// The BSDs, including macOS.
		return 103
	}
}

func checkSocketPath(path string) error {
	if limit := maxSocketPath(); len(path) > limit {
		return fmt.Errorf(
			"control socket path is %d characters, and this system allows %d: set control_socket to something shorter, for example /run/geda/gedad.sock",
			len(path), limit)
	}
	return nil
}
