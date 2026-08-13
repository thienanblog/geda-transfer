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
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Running the converters.
//
// Every command here is built as an argument vector and handed to exec
// directly. Nothing is passed through a shell: filenames come from a phone,
// and a photo called `; rm -rf ~` is a filename, not a command.

// Output extensions. They are lower case because a converted file is this
// product's own output, not the sender's, and there is no reason to inherit
// the shouty extension a camera chose.
const (
	extJPEG = "jpg"
	extMP4  = "mp4"
)

// jpegQuality is the quality both encoders are asked for.
//
// 92 is where a HEIC-to-JPEG conversion stops being visibly worse on a photo
// with sky or skin in it, and it is well past where the file stops getting
// meaningfully smaller. It is not configurable: the setting that matters is
// whether to convert at all.
const jpegQuality = 92

// crf is x264's quality target for the video path. 20 is visually
// transparent for phone footage at the resolutions phones shoot.
const crf = "20"

// ErrNoTool reports that the policy asks for a conversion the machine cannot
// perform. It is not a failure of the transfer: the file is already stored.
var ErrNoTool = errors.New("no converter installed")

// ErrNotNeeded reports that a file is already in the target format, so the
// only thing a conversion could do is make it worse.
var ErrNotNeeded = errors.New("already in the target format")

// Job is one conversion.
type Job struct {
	// Source is the absolute path of the received file.
	Source string

	// Class decides which converter runs.
	Class Class

	// Dest is the absolute path the converted file should end up at. Its
	// extension is chosen by the converter, so callers pass the basename and
	// read the result back from Output.
	Dest string

	// ModTime is the asset's capture date, stamped on the output so that a
	// converted copy sorts beside its original rather than under today.
	ModTime time.Time
}

// Result is what a conversion produced.
type Result struct {
	// Output is the absolute path of the converted file.
	Output string

	// Size is its size in bytes.
	Size int64

	// Tool is what performed the conversion, for the history view.
	Tool string
}

// Converter runs conversions with a fixed set of tools.
type Converter struct {
	tools Tools

	// timeout bounds a single conversion. A 40-minute 4K video is real work,
	// but a converter that has hung on a corrupt file must not hold a worker
	// slot forever.
	timeout time.Duration
}

// DefaultTimeout is how long one file may take.
const DefaultTimeout = 2 * time.Hour

// NewConverter builds a converter over an already-detected tool set.
func NewConverter(tools Tools) *Converter {
	return &Converter{tools: tools, timeout: DefaultTimeout}
}

// Tools is the tool set this converter was built with.
func (c *Converter) Tools() Tools { return c.tools }

// SetTimeout overrides how long one conversion may run. Zero restores the
// default.
func (c *Converter) SetTimeout(d time.Duration) {
	if d <= 0 {
		d = DefaultTimeout
	}
	c.timeout = d
}

// Convert produces a converted copy of one file.
//
// It never writes to Dest directly. The output is built under a temporary
// name in the same directory and moved into place with an exclusive claim, so
// that a conversion killed half way through cannot leave a truncated JPEG
// looking like a finished one.
func (c *Converter) Convert(ctx context.Context, job Job) (Result, error) {
	if job.Source == "" || job.Dest == "" {
		return Result{}, errors.New("convert: source and destination are required")
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	switch job.Class {
	case ClassHEIC:
		return c.run(ctx, job, extJPEG, c.stillArgs)
	case ClassVideo:
		// A file that already carries the target extension has nowhere to put
		// the output: the converted name would be the source's own. Writing it
		// beside itself as `name_1.mp4` breaks the one thing a pair member's
		// name has to do -- share a basename with the rest of the pair -- and
		// under a replace it renames the user's file for no reason.
		if sameExt(job.Dest, extMP4) {
			return Result{}, ErrNotNeeded
		}
		needed, err := c.videoNeedsWork(ctx, job.Source)
		if err != nil {
			return Result{}, err
		}
		if !needed {
			return Result{}, ErrNotNeeded
		}
		return c.run(ctx, job, extMP4, c.videoArgs)
	default:
		return Result{}, fmt.Errorf("%w: nothing converts a %s file", ErrNoTool, job.Class)
	}
}

// sameExt reports whether a path already carries this extension.
func sameExt(path, ext string) bool {
	return strings.EqualFold(strings.TrimPrefix(filepath.Ext(path), "."), ext)
}

// argsFor builds a command for one conversion, given the temporary output.
type argsFor func(src, tmp string) (tool string, argv []string, err error)

func (c *Converter) run(ctx context.Context, job Job, ext string, build argsFor) (Result, error) {
	dest := withExt(job.Dest, ext)

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return Result{}, fmt.Errorf("prepare the output directory: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".converting-*."+ext)
	if err != nil {
		return Result{}, fmt.Errorf("prepare the converted file: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	// The encoders want to create the file themselves; an empty one here only
	// reserves the name.
	defer os.Remove(tmpPath)

	tool, argv, err := build(job.Source, tmpPath)
	if err != nil {
		return Result{}, err
	}

	cmd := exec.CommandContext(ctx, tool, argv...)
	// Nothing on the other end of stdin. Without this ffmpeg waits at a
	// prompt when it thinks it is talking to a terminal, and a worker with no
	// terminal waits with it until the timeout.
	cmd.Stdin = nil
	var stderr strings.Builder
	cmd.Stderr = &limitWriter{w: &stderr, remaining: stderrLimit}

	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); errors.Is(ctxErr, context.DeadlineExceeded) {
			return Result{}, fmt.Errorf("%s gave up after %s", filepath.Base(tool), c.timeout)
		} else if ctxErr != nil {
			return Result{}, ctxErr
		}
		return Result{}, fmt.Errorf("%s: %w: %s", filepath.Base(tool), err, lastLine(stderr.String()))
	}

	info, err := os.Stat(tmpPath)
	if err != nil {
		return Result{}, fmt.Errorf("%s reported success but wrote nothing: %w", filepath.Base(tool), err)
	}
	if info.Size() == 0 {
		return Result{}, fmt.Errorf("%s reported success but wrote an empty file", filepath.Base(tool))
	}

	if !job.ModTime.IsZero() {
		// Cosmetic, so a failure must not fail the conversion.
		_ = os.Chtimes(tmpPath, job.ModTime, job.ModTime)
	}

	final, err := claim(tmpPath, dest)
	if err != nil {
		return Result{}, err
	}

	return Result{Output: final, Size: info.Size(), Tool: filepath.Base(tool)}, nil
}

// claim moves tmp to dest, or to dest with a counter when the name is taken.
//
// The name should be free -- storage allocates one basename per file and per
// pair -- but a user who dropped their own IMG_0042.jpg into the destination
// is entitled not to have it overwritten by a conversion.
func claim(tmp, dest string) (string, error) {
	const attempts = 100
	dir, name := filepath.Split(dest)
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)

	for counter := range attempts {
		candidate := dest
		if counter > 0 {
			candidate = filepath.Join(dir, fmt.Sprintf("%s_%d%s", base, counter, ext))
		}

		f, err := os.OpenFile(candidate, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("claim %s: %w", candidate, err)
		}
		f.Close()

		if err := os.Rename(tmp, candidate); err != nil {
			os.Remove(candidate)
			return "", fmt.Errorf("move the converted file into place: %w", err)
		}
		return candidate, nil
	}
	return "", fmt.Errorf("no free name for %s after %d attempts", dest, attempts)
}

// stillArgs converts a HEIC to a JPEG.
func (c *Converter) stillArgs(src, tmp string) (string, []string, error) {
	if c.tools.HeifConvert.Found() {
		return c.tools.HeifConvert.Path, []string{
			"-q", fmt.Sprint(jpegQuality),
			src, tmp,
		}, nil
	}
	if c.tools.FFmpeg.Found() {
		return c.tools.FFmpeg.Path, []string{
			"-nostdin", "-y",
			"-i", src,
			// 2 is ffmpeg's best usable JPEG quality on its 2-31 scale. The
			// two encoders do not share a scale, so this is the nearest
			// equivalent to jpegQuality rather than the same number.
			"-q:v", "2",
			"-map_metadata", "0",
			tmp,
		}, nil
	}
	return "", nil, fmt.Errorf("%w: install libheif or ffmpeg to convert HEIC photos", ErrNoTool)
}

// videoArgs re-encodes a video to H.264 in MP4.
func (c *Converter) videoArgs(src, tmp string) (string, []string, error) {
	if !c.tools.FFmpeg.Found() {
		return "", nil, fmt.Errorf("%w: install ffmpeg to convert videos", ErrNoTool)
	}
	return c.tools.FFmpeg.Path, []string{
		"-nostdin", "-y",
		"-i", src,
		"-c:v", "libx264",
		"-preset", "medium",
		"-crf", crf,
		// Baseline-ish pixel format. 10-bit HEVC in a 4:2:0 profile nothing
		// can play is exactly the compatibility problem this preset exists
		// to solve, so the output is the profile everything plays.
		"-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-b:a", "192k",
		// Capture date, orientation, and location live in the container's
		// metadata; dropping them would file the copy under today.
		"-map_metadata", "0",
		"-movflags", "+faststart",
		tmp,
	}, nil
}

// videoNeedsWork reports whether the video is not already H.264.
//
// Without ffprobe the answer is yes, which re-encodes an H.264 file into a
// slightly worse H.264 file. That is wasteful but not wrong, and it is what a
// machine with ffmpeg and no ffprobe -- an unusual pairing -- gets.
func (c *Converter) videoNeedsWork(ctx context.Context, src string) (bool, error) {
	if !c.tools.FFmpeg.Found() {
		return false, fmt.Errorf("%w: install ffmpeg to convert videos", ErrNoTool)
	}
	if !c.tools.FFprobe.Found() {
		return true, nil
	}

	cmd := exec.CommandContext(ctx, c.tools.FFprobe.Path,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_name",
		"-of", "default=noprint_wrappers=1:nokey=1",
		src)
	out, err := cmd.Output()
	if err != nil {
		// A file ffprobe cannot read is one ffmpeg will not convert either,
		// but that is the converter's error to report, not a reason to skip.
		return true, nil
	}

	codec := strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0])
	switch codec {
	case "h264", "mpeg4":
		return false, nil
	default:
		return true, nil
	}
}

// withExt replaces a path's extension.
func withExt(path, ext string) string {
	return strings.TrimSuffix(path, filepath.Ext(path)) + "." + ext
}

// stderrLimit bounds how much of a failing tool's output is kept. ffmpeg on a
// corrupt file can print a line per frame, and a megabyte of that in an error
// column helps nobody.
const stderrLimit = 8 << 10

type limitWriter struct {
	w         *strings.Builder
	remaining int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.remaining > 0 {
		if len(p) < l.remaining {
			l.w.Write(p)
			l.remaining -= len(p)
		} else {
			l.w.Write(p[:l.remaining])
			l.remaining = 0
		}
	}
	// The tool must never see a short write: ffmpeg treats one as a broken
	// pipe and aborts a conversion that was going perfectly well.
	return len(p), nil
}

// lastLine is the part of a tool's output worth showing. Encoders put the
// banner first and the reason last.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return "no output"
}
