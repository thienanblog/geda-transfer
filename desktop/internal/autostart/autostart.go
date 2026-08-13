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

// Package autostart asks the OS to launch the app when the user logs in.
//
// It matters more here than in most apps. A desktop cannot be woken by a
// phone: there is no push channel, and a receiver that is not running is not
// discoverable at all (AGENTS.md §3.7). "Send from my phone" therefore means
// "the desktop app was already running", and the only way that is true without
// the user thinking about it is if it starts at login.
//
// Every mechanism used here is per-user and needs no elevation. Anything
// requiring an administrator would be a system-wide change, which is not what
// the user agreed to when they ticked a box in a transfer app.
package autostart

// Label identifies this app's autostart entry. It is part of the on-disk
// filename and the registry value name, so changing it orphans the entry the
// previous version created and the app silently stops starting at login.
const Label = "app.geda.transfer"

// DisplayName is what the OS shows the user in its own login-items UI.
const DisplayName = "Geda Transfer"
