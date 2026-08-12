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

package qrterm

import (
	"strings"
	"testing"
)

const samplePayload = "geda://pair/" + "eyJ2IjoxLCJkZXZpY2VfaWQiOiI5MTlmN2Y1N2RjOTZlZTI2MGM4NTBlNTAxOGU0MzY3OSIsIm5hbWUiOiJMaXZpbmcgUm9vbSBOQVMiLCJzcGtpIjoiL3JsYnplQXVBVzRzVENoN1lvRzdRc056bVNWZkw0VlJMWmx4WjFxNGpsTT0ifQ"

func TestWriteMatchesColumns(t *testing.T) {
	columns, err := Columns(samplePayload)
	if err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	if err := Write(&b, samplePayload); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != (columns+1)/2 {
		t.Errorf("drew %d rows for %d module rows; each row carries two", len(lines), columns)
	}
	for i, line := range lines {
		// Columns is what the caller compares against the terminal width, so
		// it has to be the real drawn width, not an estimate.
		if n := strings.Count(line, "▀"); n != columns {
			t.Fatalf("row %d is %d cells wide, Columns reported %d", i, n, columns)
		}
	}
}

// The quiet zone is not decoration: without a light margin many scanners never
// find the symbol at all, and a terminal background is usually dark.
func TestQuietZoneIsLight(t *testing.T) {
	var b strings.Builder
	if err := Write(&b, samplePayload); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	for i := 0; i < quiet/2; i++ {
		if strings.Contains(lines[i], fgDark) || strings.Contains(lines[i], bgDark) {
			t.Fatalf("row %d of the quiet zone contains dark modules", i)
		}
	}
	// Both ends: the margin below the code matters as much as the one above.
	for i := len(lines) - quiet/2; i < len(lines); i++ {
		if strings.Contains(lines[i], fgDark) || strings.Contains(lines[i], bgDark) {
			t.Fatalf("row %d of the trailing quiet zone contains dark modules", i)
		}
	}
}

// Colours are set explicitly because a code drawn in the terminal's own
// palette comes out inverted on a dark theme, and many scanners will not
// invert.
func TestColoursAreExplicit(t *testing.T) {
	var b strings.Builder
	if err := Write(&b, samplePayload); err != nil {
		t.Fatal(err)
	}
	out := b.String()

	for _, code := range []string{fgLight, fgDark, bgLight, bgDark, reset} {
		if !strings.Contains(out, code) {
			t.Errorf("output never sets %q", strings.ReplaceAll(code, "\x1b", "ESC"))
		}
	}
	// A row that does not reset leaks its background into the rest of the
	// terminal.
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if !strings.HasSuffix(line, reset) {
			t.Fatalf("row %d does not reset the colours", i)
		}
	}
}

func TestColumnsGrowsWithThePayload(t *testing.T) {
	small, err := Columns("geda://pair/abc")
	if err != nil {
		t.Fatal(err)
	}
	large, err := Columns(samplePayload)
	if err != nil {
		t.Fatal(err)
	}
	if large <= small {
		t.Errorf("a longer payload should need more columns: %d then %d", small, large)
	}
}
