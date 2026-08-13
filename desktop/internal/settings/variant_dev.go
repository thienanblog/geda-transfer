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

//go:build dev

package settings

// `wails dev` compiles with the `dev` tag, so a development run is separated
// from the installed app by construction rather than by remembering to set an
// environment variable.
//
// It matters more here than in most apps: pairing is the thing being worked
// on, and pairing writes device rows and a TLS identity into the ledger. A
// developer testing it against their real state accumulates junk devices in
// the app they actually use, and a mistake that damages the identity makes
// every phone they own fail with a pin mismatch that has no override
// (AGENTS.md §3.5).
const variant = "-dev"
