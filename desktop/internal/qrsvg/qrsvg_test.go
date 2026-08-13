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

package qrsvg_test

import (
	"strings"
	"testing"

	"github.com/geda/geda-transfer/desktop/internal/qrsvg"
)

const uri = "geda://pair/eyJ2IjoxLCJkZXZpY2VfaWQiOiJhYmMifQ"

func TestEncodeProducesADrawableSymbol(t *testing.T) {
	svg, err := qrsvg.Encode(uri)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(svg, "<svg") || !strings.HasSuffix(svg, "</svg>") {
		t.Fatalf("not an SVG document: %.60s…", svg)
	}
	if !strings.Contains(svg, "viewBox=") {
		t.Error("no viewBox, so the code would not scale with the window")
	}
	if !strings.Contains(svg, `<path fill="#000000"`) {
		t.Error("the symbol has no dark modules")
	}

	// Dark on light, fixed, in both themes: many scanners will not invert.
	if !strings.Contains(svg, `fill="#ffffff"`) {
		t.Error("the code carries no light background")
	}
	if strings.Contains(svg, "currentColor") {
		t.Error("the code inherits the page's colour, so a dark theme would invert it")
	}
}

// A quiet zone is what lets a scanner find the symbol at all.
func TestEncodeIncludesTheQuietZone(t *testing.T) {
	svg, err := qrsvg.Encode(uri)
	if err != nil {
		t.Fatal(err)
	}

	// No module may start at 0: the margin is four modules wide on every side.
	if strings.Contains(svg, `d="M0 0`) {
		t.Error("a module sits in the quiet zone")
	}
}

func TestEncodeRefusesNothing(t *testing.T) {
	for _, empty := range []string{"", "   "} {
		if _, err := qrsvg.Encode(empty); err == nil {
			t.Errorf("encoding %q produced a code", empty)
		}
	}
}
