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

// Package naming renders the user's filename template into a destination path.
//
// Templates are configured by the user on both sides (AGENTS.md §3.6), so this
// package has two jobs of equal weight: substituting variables, and making
// certain that whatever a peer sends cannot escape the destination directory
// or produce a name the host filesystem refuses.
//
// Every value that reaches a rendered path is sanitised. Filenames, album
// names, and device names all arrive over the network and none of them are
// trusted.
package naming

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Default is the out-of-the-box template. It sorts chronologically in a file
// browser while keeping the original name visible.
const Default = "{yyyy}-{MM}-{dd}_{HH}{mm}{ss}_{original_name}.{ext}"

// maxComponent bounds a single path component. Most filesystems allow 255
// bytes; staying under it leaves room for the collision suffix and for the
// longest extension a phone will send.
const maxComponent = 200

// ErrEmptyResult reports a template that rendered to nothing usable, which
// would otherwise silently produce a file named after the counter alone.
var ErrEmptyResult = errors.New("template rendered to an empty filename")

// Vars are the values a template can reference. Unset fields render as empty
// and their surrounding separators are collapsed, so a template mentioning
// {album} still produces a sensible path for an asset that has no album.
type Vars struct {
	// CapturedAt is the asset's capture date, not the transfer time. A zero
	// value renders the date and time variables as empty.
	CapturedAt time.Time

	// OriginalName is the filename as it exists on the sending device,
	// extension included. Untrusted.
	OriginalName string

	// Device is the sending device's display name. Untrusted.
	Device string

	// Album is the source album, if any. Untrusted.
	Album string

	// Hash is the full-file BLAKE3 in hex, used by {hash8}.
	Hash string
}

// Result is a rendered destination, split the way the ledger stores it.
type Result struct {
	// Dir is the destination-relative directory, using forward slashes. It is
	// empty when the template places the file at the root.
	Dir string

	// Base is the filename without its extension. The collision counter
	// applies here.
	Base string

	// Ext is the extension without a leading dot. It may be empty.
	Ext string
}

// Path joins the result back into a destination-relative path.
func (r Result) Path() string {
	name := r.Base
	if r.Ext != "" {
		name += "." + r.Ext
	}
	if r.Dir == "" {
		return name
	}
	return r.Dir + "/" + name
}

// Render substitutes vars into tmpl.
//
// counter is the collision suffix: 0 renders the plain name, and 1 or more
// renders "_1", "_2", and so on. If the template mentions {counter} the value
// is placed there instead of being appended.
func Render(tmpl string, vars Vars, counter int) (Result, error) {
	if counter < 0 {
		return Result{}, fmt.Errorf("counter %d is negative", counter)
	}

	origBase, origExt := splitExt(vars.OriginalName)

	counterText := ""
	if counter > 0 {
		counterText = fmt.Sprintf("_%d", counter)
	}

	usedCounter := strings.Contains(tmpl, "{counter}")

	repl := strings.NewReplacer(
		"{yyyy}", formatTime(vars.CapturedAt, "2006"),
		"{MM}", formatTime(vars.CapturedAt, "01"),
		"{dd}", formatTime(vars.CapturedAt, "02"),
		"{HH}", formatTime(vars.CapturedAt, "15"),
		"{mm}", formatTime(vars.CapturedAt, "04"),
		"{ss}", formatTime(vars.CapturedAt, "05"),
		"{original_name}", cleanComponent(origBase),
		"{device}", cleanComponent(vars.Device),
		"{album}", cleanComponent(vars.Album),
		"{counter}", counterText,
		"{hash8}", shortHash(vars.Hash),
		"{ext}", sanitizeExt(origExt),
	)

	rendered := repl.Replace(tmpl)

	res, err := splitRendered(rendered)
	if err != nil {
		return Result{}, err
	}

	// A template that does not mention {counter} still needs somewhere to put
	// the collision suffix, and the only safe place is the end of the base.
	if !usedCounter && counterText != "" {
		res.Base = fitBase(res.Base, res.Ext, len(counterText)) + counterText
	}

	if res.Base == "" {
		return Result{}, ErrEmptyResult
	}
	return res, nil
}

// splitRendered turns rendered template output into a Result, discarding any
// attempt to escape the destination directory.
func splitRendered(rendered string) (Result, error) {
	rendered = strings.ReplaceAll(rendered, `\`, "/")

	// Rebuild the path from sanitised components rather than trusting
	// path.Clean, so that "..", empty segments, and absolute paths cannot
	// survive in any form.
	var parts []string
	for _, seg := range strings.Split(rendered, "/") {
		if seg = cleanComponent(seg); seg == "" {
			continue
		}
		parts = append(parts, seg)
	}
	if len(parts) == 0 {
		return Result{}, ErrEmptyResult
	}

	// Directory components are shortened here; the filename is left intact so
	// that fitBase below can shorten it around its extension.
	for i := range len(parts) - 1 {
		parts[i] = truncate(parts[i], maxComponent)
	}

	// The extension is split off before the base is shortened. Truncating the
	// whole filename instead would eat the extension on any long name, leaving
	// a HEIC that no viewer recognises.
	name := parts[len(parts)-1]
	dir := strings.Join(parts[:len(parts)-1], "/")

	rawBase, rawExt := splitExt(name)
	ext := sanitizeExt(rawExt)
	base := fitBase(cleanComponent(rawBase), ext, 0)

	if base == "" {
		return Result{}, ErrEmptyResult
	}

	return Result{Dir: dir, Base: base, Ext: ext}, nil
}

// splitExt divides a filename into its base and extension, without the dot.
// A leading dot is part of the base, so ".hidden" does not become all
// extension.
func splitExt(name string) (base, ext string) {
	e := path.Ext(name)
	if e == "" || e == name {
		return name, ""
	}
	return strings.TrimSuffix(name, e), strings.TrimPrefix(e, ".")
}

func formatTime(t time.Time, layout string) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(layout)
}

func shortHash(hash string) string {
	if len(hash) < 8 {
		return hash
	}
	return hash[:8]
}

// windowsReserved are device names that Windows refuses to use as a filename,
// with or without an extension. A NAS writing to a Samba share hits these too.
var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// fitBase shortens base so that base, its extension, and a suffix of
// suffixLen bytes all fit inside one path component. The extension and the
// collision counter are both load-bearing -- losing either produces a file the
// user cannot open, or two different files sharing a name -- so the base is
// what gives way.
func fitBase(base, ext string, suffixLen int) string {
	budget := maxComponent - suffixLen
	if ext != "" {
		budget -= len(ext) + 1 // the dot
	}
	return truncate(base, budget)
}

// cleanComponent makes s safe to use as one path component on macOS, Windows,
// and Linux. It never returns a string containing a separator, and it never
// returns "." or "..". It does not shorten s; callers decide the budget.
func cleanComponent(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	for _, r := range s {
		switch {
		case r == '/' || r == '\\':
			// Separators are how a peer would try to escape the destination.
			b.WriteRune('_')
		case r < 0x20 || r == 0x7f:
			// Control characters, including the NUL that truncates a path in
			// any C API underneath us.
			b.WriteRune('_')
		case strings.ContainsRune(`<>:"|?*`, r):
			// Illegal on Windows and on SMB shares served from a NAS.
			b.WriteRune('_')
		case unicode.Is(unicode.Cf, r):
			// Format characters such as RTL overrides, which can disguise an
			// extension in a file listing.
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}

	out := b.String()

	// Windows silently strips trailing dots and spaces, which would turn
	// "report." into "report" and create a collision the ledger did not
	// predict.
	out = strings.TrimRight(out, " .")
	out = strings.TrimLeft(out, " ")

	if out == "." || out == ".." {
		return ""
	}
	if windowsReserved[strings.ToUpper(out)] {
		out = "_" + out
	}

	return out
}

// sanitizeExt cleans an extension. Extensions are far more constrained than
// names: anything exotic is a sign of a crafted filename rather than a real
// asset.
func sanitizeExt(ext string) string {
	ext = cleanComponent(ext)
	ext = strings.TrimLeft(ext, ".")

	var b strings.Builder
	for _, r := range ext {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return truncate(b.String(), 16)
}

// truncate limits s to n bytes without splitting a multi-byte rune, so a name
// full of non-ASCII characters cannot produce invalid UTF-8 on disk.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}

	cut := s[:n]
	for len(cut) > 0 {
		if r, size := utf8.DecodeLastRuneInString(cut); r != utf8.RuneError || size > 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return strings.TrimRight(cut, " .")
}
