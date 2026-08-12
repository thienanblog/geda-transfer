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

// Package qrterm draws a QR code in a terminal.
//
// A NAS is administered over SSH, so the pairing code has to appear in the
// terminal or the headless daemon cannot be paired with at all without a
// second machine.
//
// Two details make the difference between a code a phone reads instantly and
// one it never locks onto:
//
//   - Colours are set explicitly rather than inherited. A QR code needs dark
//     modules on a light background; drawn with the terminal's own palette it
//     comes out inverted on the dark themes most people use, and many scanners
//     will not invert.
//   - Each module is one character wide and half a character tall, using the
//     upper-half block. Terminal cells are about twice as tall as they are
//     wide, so this is what makes the modules square.
package qrterm

import (
	"fmt"
	"io"
	"strings"

	"rsc.io/qr"
)

// quiet is the mandatory light margin around a QR code, in modules. Scanners
// need it to find the symbol; without it a code against a dark terminal
// background often fails to register at all.
const quiet = 4

const (
	fgLight = "\x1b[97m"  // bright white foreground
	fgDark  = "\x1b[30m"  // black foreground
	bgLight = "\x1b[107m" // bright white background
	bgDark  = "\x1b[40m"  // black background
	reset   = "\x1b[0m"
)

// Columns reports how many terminal columns the code needs, quiet zone
// included. A QR code narrower than the window is scanned in a second; one
// that wraps is not a QR code at all, so callers check before drawing.
func Columns(text string) (int, error) {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return 0, fmt.Errorf("encode QR code: %w", err)
	}
	return code.Size + 2*quiet, nil
}

// Write renders text as a QR code.
//
// The level is deliberately low: the pairing payload is a few hundred bytes,
// higher redundancy would push the symbol past the width of a normal terminal,
// and a code being read off a screen a foot away is not a code being read off
// a crumpled label.
func Write(w io.Writer, text string) error {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return fmt.Errorf("encode QR code: %w", err)
	}

	size := code.Size + 2*quiet
	dark := func(x, y int) bool {
		x, y = x-quiet, y-quiet
		if x < 0 || y < 0 || x >= code.Size || y >= code.Size {
			return false
		}
		return code.Black(x, y)
	}

	var b strings.Builder
	for y := 0; y < size; y += 2 {
		for x := 0; x < size; x++ {
			top, bottom := dark(x, y), dark(x, y+1)

			// The half block paints the top module in the foreground colour
			// and the bottom one in the background colour, so one text row
			// carries two module rows.
			b.WriteString(colour(fgDark, fgLight, top))
			b.WriteString(colour(bgDark, bgLight, bottom))
			b.WriteString("▀")
		}
		b.WriteString(reset)
		b.WriteString("\n")
	}

	_, err = io.WriteString(w, b.String())
	return err
}

func colour(dark, light string, isDark bool) string {
	if isDark {
		return dark
	}
	return light
}
