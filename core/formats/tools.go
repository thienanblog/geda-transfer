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
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Finding the converters.
//
// ffmpeg and libheif are external binaries, invoked as separate processes and
// never linked into this build (AGENTS.md §3.3). That is a licensing
// requirement, not a preference: a GPL ffmpeg linked in would relicense the
// whole product and end App Store distribution. So this file is about
// locating programs, and convert.go is about running them.

// Tool is one external program the receiver may use.
type Tool struct {
	// Name is what to install, as a person would type it.
	Name string `json:"name"`

	// Path is where it was found. Empty means it is not installed.
	Path string `json:"path"`

	// Version is the first line the program prints for --version, trimmed.
	// It is for the settings screen, and for a bug report six months from now.
	Version string `json:"version"`
}

// Found reports whether the tool is usable.
func (t Tool) Found() bool { return t.Path != "" }

// Tools is everything the converter can call.
type Tools struct {
	// FFmpeg converts video, and stills when heif-convert is absent.
	FFmpeg Tool `json:"ffmpeg"`

	// FFprobe reads what codec is inside a container, so that a video already
	// in H.264 is left alone instead of being re-encoded into a worse copy of
	// itself.
	FFprobe Tool `json:"ffprobe"`

	// HeifConvert is libheif's own tool. Preferred for HEIC because it is
	// what the format's reference implementation ships, and because many
	// distribution ffmpeg builds have no HEIF decoder at all.
	HeifConvert Tool `json:"heif_convert"`
}

// EnvPrefix names the environment variables that override the search:
// GEDA_FFMPEG, GEDA_FFPROBE, GEDA_HEIF_CONVERT.
const EnvPrefix = "GEDA_"

// detectTimeout bounds asking a program for its version. A binary on a
// sleeping network mount would otherwise hang the settings screen.
const detectTimeout = 3 * time.Second

// Detect finds the external tools.
func Detect(ctx context.Context) Tools {
	return Tools{
		FFmpeg:      lookup(ctx, "ffmpeg", "GEDA_FFMPEG", "-version"),
		FFprobe:     lookup(ctx, "ffprobe", "GEDA_FFPROBE", "-version"),
		HeifConvert: lookup(ctx, "heif-convert", "GEDA_HEIF_CONVERT", "--version"),
	}
}

// lookup resolves one tool: the override first, then PATH, then the places a
// package manager puts things.
func lookup(ctx context.Context, name, env, versionFlag string) Tool {
	t := Tool{Name: name}

	if override := strings.TrimSpace(os.Getenv(env)); override != "" {
		// An explicit path is honoured exactly as given, including when it
		// does not exist: silently falling back to a different binary than
		// the one somebody named is how a machine ends up converting with a
		// tool nobody chose.
		if abs, err := filepath.Abs(override); err == nil {
			if isExecutable(abs) {
				t.Path = abs
			}
		}
		t.Version = versionOf(ctx, t.Path, versionFlag)
		return t
	}

	if path, err := exec.LookPath(name); err == nil {
		t.Path = path
		t.Version = versionOf(ctx, t.Path, versionFlag)
		return t
	}

	// A desktop app launched from Finder or the Dock inherits a PATH of
	// /usr/bin:/bin:/usr/sbin:/sbin -- not the one the user's shell has. So a
	// Homebrew ffmpeg that works perfectly in Terminal is invisible to the
	// app, and the user is told to install something they already have.
	for _, dir := range searchDirs() {
		candidate := filepath.Join(dir, name+exeSuffix)
		if isExecutable(candidate) {
			t.Path = candidate
			t.Version = versionOf(ctx, t.Path, versionFlag)
			return t
		}
	}
	return t
}

// exeSuffix is what an executable is called on this platform.
var exeSuffix = func() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}()

// searchDirs are the well-known install locations, in the order a machine that
// has several should prefer them.
func searchDirs() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/opt/homebrew/bin", // Apple silicon Homebrew
			"/usr/local/bin",    // Intel Homebrew, and manual installs
			"/opt/local/bin",    // MacPorts
			"/sw/bin",           // Fink
		}
	case "windows":
		var dirs []string
		for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA")} {
			if base = strings.TrimSpace(base); base != "" {
				dirs = append(dirs, filepath.Join(base, "ffmpeg", "bin"))
			}
		}
		return dirs
	default:
		return []string{"/usr/local/bin", "/usr/bin", "/bin", "/snap/bin"}
	}
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode().Perm()&0o111 != 0
}

// versionOf returns the first line of the program's version banner.
func versionOf(ctx context.Context, path, flag string) string {
	if path == "" {
		return ""
	}

	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, flag)
	// heif-convert prints its version to stderr, ffmpeg to stdout. Reading
	// only one of them would leave half the tools with a blank version.
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return ""
	}

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text())
	}
	return ""
}

// CanConvert reports whether the tools present can do a job of this class.
func (t Tools) CanConvert(class Class) bool {
	switch class {
	case ClassHEIC:
		return t.HeifConvert.Found() || t.FFmpeg.Found()
	case ClassVideo:
		return t.FFmpeg.Found()
	default:
		return false
	}
}

// Missing lists the tools a policy needs and does not have.
//
// It is computed from the policy rather than reported unconditionally: a
// receiver on the Original preset needs no converter at all, and telling that
// user to install ffmpeg is noise about a problem they do not have.
func (t Tools) Missing(p Policy) []string {
	var missing []string
	for _, class := range Classes {
		if p.Action(class) == ActionKeep || t.CanConvert(class) {
			continue
		}
		if name := MissingFor(class); name != "" {
			missing = append(missing, name)
		}
	}
	return dedupe(missing)
}

// MissingFor names what has to be installed to convert this class.
//
// Exported so a settings screen can say it for a preset the user is only
// considering. Waiting until the policy is saved would mean the one message
// that explains why nothing will be converted appears after the decision
// rather than during it.
func MissingFor(class Class) string {
	switch class {
	case ClassHEIC:
		return "libheif (heif-convert) or ffmpeg"
	case ClassVideo:
		return "ffmpeg"
	default:
		return ""
	}
}

// Explain is the message a settings screen shows when something is absent.
// Empty means there is nothing to say.
func (t Tools) Explain(p Policy) string {
	missing := t.Missing(p)
	if len(missing) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s is not installed, so nothing will be converted. ", strings.Join(missing, " and "))
	// The files are safe and the user should be told so in the same sentence
	// as the problem: an alarming message about a missing dependency reads
	// like data loss when the actual outcome is that originals are kept.
	b.WriteString("Files still arrive and are stored exactly as they were sent. ")
	b.WriteString(InstallHint())
	return b.String()
}

// InstallHint is how to get the converters on this platform.
func InstallHint() string {
	switch runtime.GOOS {
	case "darwin":
		return "Install with Homebrew: brew install ffmpeg libheif"
	case "windows":
		return "Install with winget: winget install Gyan.FFmpeg"
	default:
		return "Install with your package manager, for example: apt install ffmpeg libheif-examples"
	}
}

func dedupe(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := values[:0]
	for _, v := range values {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
