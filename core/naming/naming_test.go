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

package naming_test

import (
	"errors"
	"path"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/geda/geda-transfer/core/naming"
)

var captured = time.Date(2026, 7, 4, 15, 9, 3, 0, time.UTC)

func vars() naming.Vars {
	return naming.Vars{
		CapturedAt:   captured,
		OriginalName: "IMG_4021.HEIC",
		Device:       "An's iPhone",
		Album:        "Da Nang 2026",
		Hash:         "9f2c1ab34d5e6f708192a3b4c5d6e7f8",
	}
}

func render(t *testing.T, tmpl string, v naming.Vars, counter int) naming.Result {
	t.Helper()
	got, err := naming.Render(tmpl, v, counter)
	if err != nil {
		t.Fatalf("Render(%q): %v", tmpl, err)
	}
	return got
}

func TestDefaultTemplate(t *testing.T) {
	got := render(t, naming.Default, vars(), 0)

	if want := "2026-07-04_150903_IMG_4021"; got.Base != want {
		t.Errorf("Base = %q, want %q", got.Base, want)
	}
	if got.Ext != "HEIC" {
		t.Errorf("Ext = %q, want HEIC", got.Ext)
	}
	if got.Dir != "" {
		t.Errorf("Dir = %q, want empty", got.Dir)
	}
	if want := "2026-07-04_150903_IMG_4021.HEIC"; got.Path() != want {
		t.Errorf("Path() = %q, want %q", got.Path(), want)
	}
}

func TestAllVariables(t *testing.T) {
	tmpl := "{yyyy}/{MM}/{dd}/{device}/{album}/{HH}{mm}{ss}_{original_name}_{hash8}.{ext}"
	got := render(t, tmpl, vars(), 0)

	if want := "2026/07/04/An's iPhone/Da Nang 2026"; got.Dir != want {
		t.Errorf("Dir = %q, want %q", got.Dir, want)
	}
	if want := "150903_IMG_4021_9f2c1ab3"; got.Base != want {
		t.Errorf("Base = %q, want %q", got.Base, want)
	}
}

func TestCounterAppendedWhenTemplateOmitsIt(t *testing.T) {
	got := render(t, naming.Default, vars(), 2)

	if want := "2026-07-04_150903_IMG_4021_2"; got.Base != want {
		t.Errorf("Base = %q, want %q", got.Base, want)
	}
	if got.Ext != "HEIC" {
		t.Errorf("counter must not disturb the extension, got %q", got.Ext)
	}
}

func TestCounterPlacedWhereTemplateAsks(t *testing.T) {
	got := render(t, "{original_name}{counter}.{ext}", vars(), 3)

	if want := "IMG_4021_3"; got.Base != want {
		t.Errorf("Base = %q, want %q", got.Base, want)
	}
	// It must not also be appended.
	if strings.HasSuffix(got.Base, "_3_3") {
		t.Error("counter was substituted and appended")
	}
}

func TestCounterZeroAddsNothing(t *testing.T) {
	withMarker := render(t, "{original_name}{counter}.{ext}", vars(), 0)
	if withMarker.Base != "IMG_4021" {
		t.Errorf("Base = %q, want IMG_4021", withMarker.Base)
	}
}

func TestNegativeCounterRejected(t *testing.T) {
	if _, err := naming.Render(naming.Default, vars(), -1); err == nil {
		t.Fatal("expected an error for a negative counter")
	}
}

// A missing album must not leave an empty directory level behind.
func TestEmptyVariablesCollapseSeparators(t *testing.T) {
	v := vars()
	v.Album = ""

	got := render(t, "{yyyy}/{album}/{original_name}.{ext}", v, 0)
	if got.Dir != "2026" {
		t.Errorf("Dir = %q, want 2026", got.Dir)
	}
}

func TestZeroCaptureDateRendersEmptyDateParts(t *testing.T) {
	v := vars()
	v.CapturedAt = time.Time{}

	got := render(t, "{yyyy}/{MM}/{original_name}.{ext}", v, 0)
	if got.Dir != "" {
		t.Errorf("Dir = %q, want empty for an unknown capture date", got.Dir)
	}
	if got.Base != "IMG_4021" {
		t.Errorf("Base = %q", got.Base)
	}
}

// Filenames, album names, and device names all arrive over the network. None
// of them may be able to write outside the destination directory.
func TestPathTraversalIsNeutralised(t *testing.T) {
	cases := []struct {
		name string
		v    naming.Vars
		tmpl string
	}{
		{"dots in original name", naming.Vars{OriginalName: "../../../etc/passwd"}, "{original_name}.{ext}"},
		{"dots in album", naming.Vars{OriginalName: "a.jpg", Album: "../.."}, "{album}/{original_name}.{ext}"},
		{"dots in device", naming.Vars{OriginalName: "a.jpg", Device: ".."}, "{device}/{original_name}.{ext}"},
		{"absolute path", naming.Vars{OriginalName: "/etc/shadow"}, "{original_name}.{ext}"},
		{"windows separators", naming.Vars{OriginalName: `..\..\windows\system32\cmd.exe`}, "{original_name}.{ext}"},
		{"unc path", naming.Vars{OriginalName: `\\server\share\x.jpg`}, "{original_name}.{ext}"},
		{"nul byte", naming.Vars{OriginalName: "safe\x00../../evil.jpg"}, "{original_name}.{ext}"},
		{"newline", naming.Vars{OriginalName: "a\n../b.jpg"}, "{original_name}.{ext}"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := naming.Render(tc.tmpl, tc.v, 0)
			if err != nil {
				return // refusing outright is also a correct answer
			}

			full := got.Path()
			if path.IsAbs(full) {
				t.Fatalf("rendered an absolute path: %q", full)
			}
			for _, seg := range strings.Split(full, "/") {
				if seg == ".." || seg == "." || seg == "" {
					t.Fatalf("path %q contains the traversal segment %q", full, seg)
				}
			}
			if cleaned := path.Clean(full); cleaned != full {
				t.Fatalf("path %q is not already clean (got %q)", full, cleaned)
			}
			if strings.ContainsAny(full, "\x00\n\r") {
				t.Fatalf("path %q retains a control character", full)
			}
		})
	}
}

func TestWindowsHostileNamesAreDefused(t *testing.T) {
	cases := []struct {
		original string
		reason   string
	}{
		{"CON.jpg", "reserved device name"},
		{"nul.HEIC", "reserved device name, lowercase"},
		{"report..jpg", "trailing dot before extension"},
		{`a<b>c:d"e|f?g*h.jpg`, "characters illegal on Windows and SMB"},
	}

	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			v := vars()
			v.OriginalName = tc.original

			got := render(t, "{original_name}.{ext}", v, 0)

			if strings.ContainsAny(got.Base, `<>:"|?*/\`) {
				t.Errorf("Base %q keeps a character Windows rejects", got.Base)
			}
			if strings.HasSuffix(got.Base, ".") || strings.HasSuffix(got.Base, " ") {
				t.Errorf("Base %q ends with a character Windows silently strips", got.Base)
			}

			upper := strings.ToUpper(got.Base)
			for _, reserved := range []string{"CON", "PRN", "AUX", "NUL", "COM1", "LPT1"} {
				if upper == reserved {
					t.Errorf("Base %q is a reserved Windows device name", got.Base)
				}
			}
		})
	}
}

// A format character such as an RTL override can make a .exe look like a .jpg
// in a file listing.
func TestBidiOverrideIsStripped(t *testing.T) {
	v := vars()
	v.OriginalName = "photo‮gpj.exe"

	got := render(t, "{original_name}.{ext}", v, 0)
	if strings.ContainsRune(got.Base, '‮') {
		t.Errorf("Base %q keeps a bidi override", got.Base)
	}
}

func TestLongNamesAreTruncatedToValidUTF8(t *testing.T) {
	v := vars()
	v.OriginalName = strings.Repeat("é", 500) + ".HEIC"

	got := render(t, "{original_name}.{ext}", v, 0)

	if len(got.Base) > 200 {
		t.Errorf("Base is %d bytes, want at most 200", len(got.Base))
	}
	if !utf8.ValidString(got.Base) {
		t.Error("truncation split a multi-byte rune")
	}
	if got.Ext != "HEIC" {
		t.Errorf("Ext = %q, want HEIC", got.Ext)
	}
}

// The counter must survive truncation: without room reserved for it, two
// distinct files would truncate to the same name and overwrite each other.
func TestCounterSurvivesTruncation(t *testing.T) {
	v := vars()
	v.OriginalName = strings.Repeat("a", 500) + ".HEIC"

	first := render(t, "{original_name}.{ext}", v, 0)
	second := render(t, "{original_name}.{ext}", v, 1)

	if first.Base == second.Base {
		t.Fatal("counter was truncated away, so two files share a name")
	}
	if !strings.HasSuffix(second.Base, "_1") {
		t.Errorf("Base %q does not end with the counter", second.Base)
	}
	if len(second.Base) > 200 {
		t.Errorf("Base is %d bytes, want at most 200", len(second.Base))
	}
}

func TestExtensionIsConstrained(t *testing.T) {
	cases := map[string]string{
		"a.HEIC":                       "HEIC",
		"a.jpg":                        "jpg",
		"a.tar.gz":                     "gz",
		"noextension":                  "",
		".hidden":                      "",
		"a." + strings.Repeat("x", 40): strings.Repeat("x", 16),
	}

	for original, want := range cases {
		v := vars()
		v.OriginalName = original

		got, err := naming.Render("{original_name}.{ext}", v, 0)
		if err != nil {
			t.Errorf("%q: %v", original, err)
			continue
		}
		if got.Ext != want {
			t.Errorf("%q: Ext = %q, want %q", original, got.Ext, want)
		}
	}
}

// ".hidden" is all base and no extension; treating the whole name as an
// extension would leave nothing to name the file with.
func TestDotfileKeepsItsName(t *testing.T) {
	v := vars()
	v.OriginalName = ".hidden"

	got := render(t, "{original_name}.{ext}", v, 0)
	if got.Base != ".hidden" {
		t.Errorf("Base = %q, want .hidden", got.Base)
	}
}

func TestEmptyRenderIsRejected(t *testing.T) {
	for _, tmpl := range []string{"", "{album}", "/", "///", "{album}/{device}"} {
		if _, err := naming.Render(tmpl, naming.Vars{}, 0); err == nil {
			t.Errorf("Render(%q) succeeded, want an error", tmpl)
		}
	}
}

func TestShortHashHandlesShortInput(t *testing.T) {
	v := vars()
	v.Hash = "abc"

	got := render(t, "{original_name}_{hash8}.{ext}", v, 0)
	if !strings.HasSuffix(got.Base, "_abc") {
		t.Errorf("Base = %q, want it to end with _abc", got.Base)
	}
}

func TestResultPathRoundTrip(t *testing.T) {
	cases := []naming.Result{
		{Dir: "2026/07", Base: "IMG_0001", Ext: "HEIC"},
		{Dir: "", Base: "IMG_0001", Ext: "HEIC"},
		{Dir: "2026", Base: "noext", Ext: ""},
	}
	want := []string{"2026/07/IMG_0001.HEIC", "IMG_0001.HEIC", "2026/noext"}

	for i, r := range cases {
		if got := r.Path(); got != want[i] {
			t.Errorf("Path() = %q, want %q", got, want[i])
		}
	}
}

func TestValidate(t *testing.T) {
	valid := []string{
		naming.Default,
		"{yyyy}/{MM}/{original_name}.{ext}",
		"{device}_{hash8}_{counter}.{ext}",
		"photos/fixed-name.{ext}",
	}
	for _, tmpl := range valid {
		if err := naming.Validate(tmpl); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", tmpl, err)
		}
	}

	invalid := map[string]string{
		"empty":            "",
		"whitespace only":  "   ",
		"unclosed brace":   "{yyyy",
		"misspelled":       "{yyy}-{original_name}.{ext}",
		"unknown variable": "{camera}/{original_name}.{ext}",
		// Renders to nothing for a screenshot, which has no album -- the
		// files most likely to go missing quietly.
		"empty for some files": "{album}",
	}
	for name, tmpl := range invalid {
		t.Run(name, func(t *testing.T) {
			err := naming.Validate(tmpl)
			if err == nil {
				t.Fatalf("Validate(%q) = nil, want an error", tmpl)
			}
			if !errors.Is(err, naming.ErrBadTemplate) {
				t.Errorf("err = %v, want ErrBadTemplate", err)
			}
		})
	}
}

// Every variable the documentation promises must actually substitute; a
// variable listed but not replaced would render literally into filenames.
func TestEveryDocumentedVariableIsSubstituted(t *testing.T) {
	for _, v := range naming.Variables {
		if v == "counter" {
			// {counter} is empty unless there is a collision, which is what
			// TestCounter covers.
			continue
		}
		tmpl := "{" + v + "}.jpg"
		res, err := naming.Render(tmpl, naming.Vars{
			CapturedAt:   time.Date(2026, 7, 4, 15, 9, 3, 0, time.UTC),
			OriginalName: "IMG_0001.HEIC",
			Device:       "iPhone",
			Album:        "Recents",
			Hash:         "0123456789abcdef",
		}, 0)
		if err != nil {
			t.Errorf("Render(%q): %v", tmpl, err)
			continue
		}
		if strings.Contains(res.Base, "{") {
			t.Errorf("Render(%q) left the variable unsubstituted: %q", tmpl, res.Base)
		}
	}
}
