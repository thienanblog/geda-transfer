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

//go:build ignore

// Command gen draws the app's icons.
//
// They are generated rather than drawn by hand so that the tray icon, the
// Windows .ico, and the application icon cannot drift apart, and so that a
// change to the mark is one edit rather than three exports. Run it with:
//
//	go run ./internal/icons/gen.go
//
// The output is committed: a build must not need this to have been run.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run() error {
	// The application icon: the mark on the product's blue, rounded like every
	// other icon on both platforms.
	appIcon := draw(1024, color.NRGBA{0x1d, 0x4e, 0xd8, 0xff}, color.NRGBA{0xff, 0xff, 0xff, 0xff}, true)
	if err := writePNG(filepath.Join("build", "appicon.png"), appIcon); err != nil {
		return err
	}

	// The macOS tray icon is a template image: black shapes plus alpha, which
	// the system recolours for the menu bar's appearance. Anything coloured
	// here would come out wrong in dark mode, or in a menu bar with a tint.
	trayMac := draw(44, color.NRGBA{}, color.NRGBA{0, 0, 0, 0xff}, false)
	if err := writePNG(filepath.Join("internal", "icons", "tray_template.png"), trayMac); err != nil {
		return err
	}

	// Windows has no template convention, so the tray icon is the mark in
	// white on transparent, which reads on both the light and the dark taskbar.
	trayWin := draw(32, color.NRGBA{}, color.NRGBA{0xff, 0xff, 0xff, 0xff}, false)
	if err := writePNG(filepath.Join("internal", "icons", "tray.png"), trayWin); err != nil {
		return err
	}
	if err := writeICO(filepath.Join("internal", "icons", "tray.ico"), trayWin); err != nil {
		return err
	}
	return writeICO(filepath.Join("build", "windows", "icon.ico"),
		draw(256, color.NRGBA{0x1d, 0x4e, 0xd8, 0xff}, color.NRGBA{0xff, 0xff, 0xff, 0xff}, true))
}

// draw renders the mark: an arrow rising out of a tray.
//
// It is drawn from distance fields rather than from polygons so that every
// edge is antialiased at every size, which is what stops a 32-pixel tray icon
// from looking like a staircase.
func draw(size int, bg, fg color.NRGBA, rounded bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := float64(size)

	// Geometry in fractions of the canvas, so one description serves 32px and
	// 1024px alike.
	const (
		radius     = 0.2237 // the macOS "squircle" corner, near enough
		stroke     = 0.085
		arrowTop   = 0.20
		arrowBase  = 0.60
		headHalf   = 0.175
		headTop    = 0.355
		trayY      = 0.70
		trayHalf   = 0.255
		trayHeight = 0.10
	)

	for y := range size {
		for x := range size {
			// Sample at the pixel centre, normalised to 0..1.
			px, py := (float64(x)+0.5)/s, (float64(y)+0.5)/s

			var inside float64
			if rounded {
				inside = coverage(roundedRect(px, py, 0, 0, 1, 1, radius), s)
			} else {
				inside = 1
			}
			if inside <= 0 {
				continue
			}

			// The shaft of the arrow.
			d := roundedRect(px, py, 0.5-stroke/2, arrowTop, 0.5+stroke/2, arrowBase, stroke/2)
			// Its head.
			d = math.Min(d, triangle(px, py, 0.5, arrowTop, 0.5-headHalf, headTop, 0.5+headHalf, headTop))
			// The tray it rises from: an open-topped bracket.
			d = math.Min(d, bracket(px, py, trayY, trayHalf, trayHeight, stroke))

			mark := coverage(d, s)
			out := blend(bg, fg, mark)
			out.A = uint8(float64(out.A) * inside)
			img.SetNRGBA(x, y, out)
		}
	}
	return img
}

// coverage turns a signed distance into an alpha value, antialiased across
// roughly one pixel.
func coverage(dist, size float64) float64 {
	edge := 1.0 / size
	switch {
	case dist <= -edge:
		return 1
	case dist >= edge:
		return 0
	default:
		return 0.5 - dist/(2*edge)
	}
}

func blend(bg, fg color.NRGBA, t float64) color.NRGBA {
	if t <= 0 {
		return bg
	}
	if t >= 1 && bg.A == 0 {
		return fg
	}
	mix := func(a, b uint8) uint8 { return uint8(float64(a)*(1-t) + float64(b)*t) }
	return color.NRGBA{
		R: mix(bg.R, fg.R),
		G: mix(bg.G, fg.G),
		B: mix(bg.B, fg.B),
		A: mix(bg.A, fg.A),
	}
}

// roundedRect is the signed distance to a rectangle with rounded corners.
func roundedRect(px, py, x0, y0, x1, y1, r float64) float64 {
	cx, cy := (x0+x1)/2, (y0+y1)/2
	hx, hy := (x1-x0)/2-r, (y1-y0)/2-r
	qx, qy := math.Abs(px-cx)-hx, math.Abs(py-cy)-hy
	outside := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
	return outside + math.Min(math.Max(qx, qy), 0) - r
}

// triangle is the signed distance to the triangle through three points.
func triangle(px, py, ax, ay, bx, by, cx, cy float64) float64 {
	d := math.Min(segment(px, py, ax, ay, bx, by),
		math.Min(segment(px, py, bx, by, cx, cy), segment(px, py, cx, cy, ax, ay)))
	if sameSide(px, py, ax, ay, bx, by, cx, cy) {
		return -d
	}
	return d
}

func sameSide(px, py, ax, ay, bx, by, cx, cy float64) bool {
	cross := func(x0, y0, x1, y1, x2, y2 float64) float64 {
		return (x1-x0)*(y2-y0) - (y1-y0)*(x2-x0)
	}
	d1 := cross(ax, ay, bx, by, px, py)
	d2 := cross(bx, by, cx, cy, px, py)
	d3 := cross(cx, cy, ax, ay, px, py)
	neg := d1 < 0 || d2 < 0 || d3 < 0
	pos := d1 > 0 || d2 > 0 || d3 > 0
	return !(neg && pos)
}

// segment is the distance from a point to a line segment.
func segment(px, py, ax, ay, bx, by float64) float64 {
	vx, vy := bx-ax, by-ay
	wx, wy := px-ax, py-ay
	length := vx*vx + vy*vy
	t := 0.0
	if length > 0 {
		t = math.Max(0, math.Min(1, (wx*vx+wy*vy)/length))
	}
	return math.Hypot(px-(ax+t*vx), py-(ay+t*vy))
}

// bracket is the tray: a floor with two short uprights, open at the top.
func bracket(px, py, y, half, height, stroke float64) float64 {
	floor := roundedRect(px, py, 0.5-half, y+height-stroke, 0.5+half, y+height, stroke/2)
	left := roundedRect(px, py, 0.5-half, y, 0.5-half+stroke, y+height, stroke/2)
	right := roundedRect(px, py, 0.5+half-stroke, y, 0.5+half, y+height, stroke/2)
	return math.Min(floor, math.Min(left, right))
}

func writePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		return err
	}
	fmt.Println("wrote", path)
	return nil
}

// writeICO wraps a PNG in an ICO container.
//
// Windows Vista and later read PNG-compressed entries, so the container is a
// 22-byte header and the PNG bytes -- rather than a BMP encoder, an AND mask,
// and the bottom-up row order that goes with them.
func writeICO(path string, img image.Image) error {
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, img); err != nil {
		return err
	}

	bounds := img.Bounds()
	dim := func(n int) byte {
		if n >= 256 {
			return 0 // 0 means 256 in an ICO entry
		}
		return byte(n)
	}

	var out bytes.Buffer
	// ICONDIR: reserved, type 1 (icon), one image.
	binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, 1})
	// ICONDIRENTRY.
	out.WriteByte(dim(bounds.Dx()))
	out.WriteByte(dim(bounds.Dy()))
	out.WriteByte(0)                                    // palette size: none
	out.WriteByte(0)                                    // reserved
	binary.Write(&out, binary.LittleEndian, uint16(1))  // colour planes
	binary.Write(&out, binary.LittleEndian, uint16(32)) // bits per pixel
	binary.Write(&out, binary.LittleEndian, uint32(pngBytes.Len()))
	binary.Write(&out, binary.LittleEndian, uint32(22)) // offset past the header
	out.Write(pngBytes.Bytes())

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", path)
	return nil
}
