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

package formats

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDetectFindsAnOverride(t *testing.T) {
	tools := fakeTools(t, fakeOK)

	for _, tool := range []Tool{tools.FFmpeg, tools.FFprobe, tools.HeifConvert} {
		if !tool.Found() {
			t.Fatalf("%s was not found at its override", tool.Name)
		}
		if tool.Version == "" {
			t.Fatalf("%s has no version; the settings screen would show a blank", tool.Name)
		}
	}
}

// An override pointing at nothing must not fall back to a different binary.
// Silently converting with a tool nobody chose is worse than not converting.
func TestDetectHonoursAMissingOverride(t *testing.T) {
	tools := noTools(t)

	if tools.FFmpeg.Found() || tools.FFprobe.Found() || tools.HeifConvert.Found() {
		t.Fatalf("a missing override resolved to something: %+v", tools)
	}
}

func TestMissingAndExplain(t *testing.T) {
	tools := noTools(t)

	// The default preset converts nothing, so there is nothing to install and
	// nothing to say about it.
	if missing := tools.Missing(Default()); len(missing) != 0 {
		t.Fatalf("the Original preset wants %v installed", missing)
	}
	if msg := tools.Explain(Default()); msg != "" {
		t.Fatalf("the Original preset explains itself: %q", msg)
	}

	compatible := Policy{Preset: PresetCompatible}
	if missing := tools.Missing(compatible); len(missing) != 2 {
		t.Fatalf("Missing = %v, want a still converter and a video converter", missing)
	}

	msg := tools.Explain(compatible)
	if msg == "" {
		t.Fatal("nothing is installed and nothing was said about it")
	}
	// The message has to say the files are safe in the same breath as the
	// problem, or a missing dependency reads like data loss.
	if !strings.Contains(msg, "stored exactly as they were sent") {
		t.Fatalf("the message does not say the files are safe: %q", msg)
	}
	if !strings.Contains(msg, "install") && !strings.Contains(msg, "Install") {
		t.Fatalf("the message does not say what to do: %q", msg)
	}
}

func TestExplainSaysNothingWhenTheToolsArePresent(t *testing.T) {
	tools := fakeTools(t, fakeOK)
	if msg := tools.Explain(Policy{Preset: PresetSpaceSaving}); msg != "" {
		t.Fatalf("everything is installed and it still complained: %q", msg)
	}
}

func TestConvertStill(t *testing.T) {
	tools := fakeTools(t, fakeOK)
	dir := t.TempDir()

	src := filepath.Join(dir, "IMG_0042.HEIC")
	write(t, src, "heic bytes")

	captured := time.Date(2024, 5, 1, 9, 30, 0, 0, time.UTC)
	result, err := NewConverter(tools).Convert(t.Context(), Job{
		Source: src, Class: ClassHEIC, Dest: src, ModTime: captured,
	})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	if got := filepath.Base(result.Output); got != "IMG_0042.jpg" {
		t.Fatalf("output is %q, want IMG_0042.jpg beside the original", got)
	}
	if result.Size == 0 {
		t.Fatal("the result reports no size")
	}

	// The original is never touched by a conversion. Removing it, when the
	// policy asks for that, is the queue's job and happens afterwards.
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("the original is gone: %v", err)
	}

	info, err := os.Stat(result.Output)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(captured) {
		t.Fatalf("mtime is %s, want the capture date %s: a converted copy has to sort beside its original",
			info.ModTime(), captured)
	}

	// Nothing may be left behind in the destination folder the user browses.
	for _, entry := range readDir(t, dir) {
		if strings.HasPrefix(entry, ".converting-") {
			t.Fatalf("a temporary file survived: %s", entry)
		}
	}
}

func TestConvertVideoSkipsWhatIsAlreadyH264(t *testing.T) {
	tools := fakeTools(t, fakeOK)
	t.Setenv(envCodec, "h264")

	dir := t.TempDir()
	src := filepath.Join(dir, "IMG_0043.MOV")
	write(t, src, "h264 bytes")

	_, err := NewConverter(tools).Convert(t.Context(), Job{Source: src, Class: ClassVideo, Dest: src})
	if !errors.Is(err, ErrNotNeeded) {
		t.Fatalf("Convert = %v, want ErrNotNeeded: re-encoding H.264 only makes it worse", err)
	}
}

func TestConvertVideoRunsOnHEVC(t *testing.T) {
	tools := fakeTools(t, fakeOK)
	t.Setenv(envCodec, "hevc")

	dir := t.TempDir()
	src := filepath.Join(dir, "IMG_0043.MOV")
	write(t, src, "hevc bytes")

	result, err := NewConverter(tools).Convert(t.Context(), Job{Source: src, Class: ClassVideo, Dest: src})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if got := filepath.Base(result.Output); got != "IMG_0043.mp4" {
		t.Fatalf("output is %q, want IMG_0043.mp4", got)
	}
}

// A file that already carries the target extension has nowhere to put its
// output. Writing it beside itself as `name_1.mp4` would break the one thing a
// pair member's name has to do, and under a replace it would rename the user's
// file for nothing.
func TestConvertSkipsWhenTheExtensionAlreadyMatches(t *testing.T) {
	tools := fakeTools(t, fakeOK)
	t.Setenv(envCodec, "hevc")

	dir := t.TempDir()
	src := filepath.Join(dir, "IMG_0043.mp4")
	write(t, src, "hevc in mp4")

	if _, err := NewConverter(tools).Convert(t.Context(), Job{
		Source: src, Class: ClassVideo, Dest: src,
	}); !errors.Is(err, ErrNotNeeded) {
		t.Fatalf("Convert = %v, want ErrNotNeeded", err)
	}

	if entries := readDir(t, dir); len(entries) != 1 {
		t.Fatalf("the directory holds %v, want only the original", entries)
	}
}

func TestConvertWithoutTools(t *testing.T) {
	tools := noTools(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "IMG_0042.HEIC")
	write(t, src, "heic bytes")

	c := NewConverter(tools)

	if _, err := c.Convert(t.Context(), Job{Source: src, Class: ClassHEIC, Dest: src}); !errors.Is(err, ErrNoTool) {
		t.Fatalf("HEIC without tools = %v, want ErrNoTool", err)
	}
	if _, err := c.Convert(t.Context(), Job{Source: src, Class: ClassVideo, Dest: src}); !errors.Is(err, ErrNoTool) {
		t.Fatalf("video without ffmpeg = %v, want ErrNoTool", err)
	}
}

// A conversion that failed must leave nothing behind that could be mistaken
// for a finished file.
func TestConvertLeavesNothingWhenTheToolFails(t *testing.T) {
	for _, mode := range []string{fakeFail, fakeEmpty, fakeSilent} {
		t.Run(mode, func(t *testing.T) {
			tools := fakeTools(t, mode)
			dir := t.TempDir()
			src := filepath.Join(dir, "IMG_0042.HEIC")
			write(t, src, "heic bytes")

			if _, err := NewConverter(tools).Convert(t.Context(), Job{
				Source: src, Class: ClassHEIC, Dest: src,
			}); err == nil {
				t.Fatal("Convert reported success")
			}

			entries := readDir(t, dir)
			if len(entries) != 1 || entries[0] != "IMG_0042.HEIC" {
				t.Fatalf("the directory holds %v, want only the original", entries)
			}
		})
	}
}

// The stderr of a failing tool is what somebody reads to find out why, so the
// last line of it has to survive into the error.
func TestConvertReportsTheToolsComplaint(t *testing.T) {
	tools := fakeTools(t, fakeFail)
	dir := t.TempDir()
	src := filepath.Join(dir, "IMG_0042.HEIC")
	write(t, src, "heic bytes")

	_, err := NewConverter(tools).Convert(t.Context(), Job{Source: src, Class: ClassHEIC, Dest: src})
	if err == nil {
		t.Fatal("Convert reported success")
	}
	if !strings.Contains(err.Error(), "could not decode the input") {
		t.Fatalf("the tool's reason did not survive: %v", err)
	}
}

// A JPEG the user put in the destination themselves is not this product's to
// overwrite, even when a conversion wants exactly that name.
func TestConvertDoesNotOverwriteAnExistingFile(t *testing.T) {
	tools := fakeTools(t, fakeOK)
	dir := t.TempDir()

	src := filepath.Join(dir, "IMG_0042.HEIC")
	write(t, src, "heic bytes")

	theirs := filepath.Join(dir, "IMG_0042.jpg")
	write(t, theirs, "a file the user put here")

	result, err := NewConverter(tools).Convert(t.Context(), Job{Source: src, Class: ClassHEIC, Dest: src})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if result.Output == theirs {
		t.Fatal("the conversion took a name that was already in use")
	}
	if got := read(t, theirs); got != "a file the user put here" {
		t.Fatalf("the user's file now reads %q", got)
	}
}

func TestConvertHonoursCancellation(t *testing.T) {
	tools := fakeTools(t, fakeOK)
	dir := t.TempDir()
	src := filepath.Join(dir, "IMG_0042.HEIC")
	write(t, src, "heic bytes")

	ctx, cancel := contextWithCancel(t)
	cancel()

	if _, err := NewConverter(tools).Convert(ctx, Job{Source: src, Class: ClassHEIC, Dest: src}); err == nil {
		t.Fatal("a cancelled conversion reported success")
	}
}

func TestWithExt(t *testing.T) {
	cases := map[string]string{
		"/a/b/IMG_0042.HEIC":  "/a/b/IMG_0042.jpg",
		"/a/b/IMG_0042":       "/a/b/IMG_0042.jpg",
		"/a/b.c/IMG_0042.mov": "/a/b.c/IMG_0042.jpg",
	}
	for in, want := range cases {
		if got := withExt(in, extJPEG); got != want {
			t.Fatalf("withExt(%q) = %q, want %q", in, got, want)
		}
	}
}

// ffmpeg treats a short write on stderr as a broken pipe and aborts, so the
// limit must swallow output rather than refuse it.
func TestLimitWriterNeverReportsAShortWrite(t *testing.T) {
	var b strings.Builder
	w := &limitWriter{w: &b, remaining: 4}

	for range 3 {
		n, err := w.Write([]byte("0123456789"))
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		if n != 10 {
			t.Fatalf("Write reported %d of 10 bytes", n)
		}
	}
	if b.Len() != 4 {
		t.Fatalf("kept %d bytes, want 4", b.Len())
	}
}
