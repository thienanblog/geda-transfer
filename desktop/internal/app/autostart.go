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

package app

import "github.com/geda/geda-transfer/desktop/internal/autostart"

// autostartSupported reports whether this system has a per-user login item the
// app can set.
//
// Asked by probing rather than by listing platforms: what decides it is
// whether the entry can be read, and a home directory that cannot be resolved
// fails on every platform equally.
func autostartSupported() bool {
	_, err := autostart.Enabled()
	return err == nil
}
