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

// Package qrsvg draws a pairing code the phone's camera can read.
//
// SVG rather than a bitmap because the window is resizable and the code has to
// stay sharp: a scaled-up PNG blurs the module edges, and a blurred QR code is
// one the camera hunts for instead of catching immediately. It is also the
// cheapest thing to hand a web view -- a string, with no image encoding, no
// data URI, and no second copy of the bytes.
package qrsvg

import (
	"fmt"
	"strings"

	"rsc.io/qr"
)

// quiet is the mandatory light margin around the symbol, in modules. Scanners
// use it to find the code; without it many will not register the symbol at all.
const quiet = 4

// Encode renders text as an SVG document.
//
// The colours are fixed rather than inherited from the page: a QR code must be
// dark modules on a light background, and one drawn in a dark theme's palette
// comes out inverted. Many scanners, the iOS camera included, will not invert
// (docs/DECISIONS.md), so the code carries its own white card in both themes.
func Encode(text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("qrsvg: nothing to encode")
	}

	// Level L on purpose: the pairing payload is a few hundred bytes, higher
	// redundancy makes a denser symbol, and this code is read off a bright
	// screen a foot away rather than off a crumpled label.
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return "", fmt.Errorf("encode QR code: %w", err)
	}

	size := code.Size + 2*quiet

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" `+
		`shape-rendering="crispEdges" role="img" aria-label="Pairing code">`, size, size)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#ffffff"/>`, size, size)

	// One <path> for every dark module rather than one <rect> each: a typical
	// pairing payload is around 900 modules, and 900 elements is enough DOM to
	// make a resize visibly stutter.
	b.WriteString(`<path fill="#000000" d="`)
	for y := range code.Size {
		for x := range code.Size {
			if code.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	b.WriteString(`"/></svg>`)

	return b.String(), nil
}
