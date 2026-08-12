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

//go:build windows

package discovery

import "golang.org/x/sys/windows"

// setSocketOptions enables broadcast, and port sharing when the caller wants
// it.
//
// Windows has no SO_REUSEPORT: SO_REUSEADDR is what lets several sockets share
// a port there, so it is set only when sharing is actually wanted.
func setSocketOptions(fd uintptr, shared bool) error {
	h := windows.Handle(fd)
	if shared {
		if err := windows.SetsockoptInt(h, windows.SOL_SOCKET, windows.SO_REUSEADDR, 1); err != nil {
			return err
		}
	}
	return windows.SetsockoptInt(h, windows.SOL_SOCKET, windows.SO_BROADCAST, 1)
}
