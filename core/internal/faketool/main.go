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

// Command faketool stands in for ffmpeg, ffprobe, and heif-convert in
// core/formats' tests.
//
// It is a program of its own rather than the test binary re-executing itself,
// which is the usual Go trick, because core's tests run under -race: the test
// binary links modernc's SQLite and takes seconds to start when instrumented,
// and the converter tests exec a tool dozens of times. This costs one build
// and then microseconds per run.
//
// It is also not a shell script, because core's tests run on Windows too.
package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// Behaviours, chosen by GEDA_FAKE_TOOL_MODE.
const (
	modeOK     = "ok"     // writes a plausible output file and exits 0
	modeFail   = "fail"   // exits 1 with something on stderr
	modeEmpty  = "empty"  // exits 0 having written an empty file
	modeSilent = "silent" // exits 0 without creating the file at all
	modeSlow   = "slow"   // blocks until it is killed
)

func main() { os.Exit(run(os.Getenv("GEDA_FAKE_TOOL_MODE"), os.Args[1:])) }

func run(mode string, args []string) int {
	// Asked for at detection time, before any conversion. A tool that cannot
	// answer this is treated as absent.
	if slices.Contains(args, "-version") || slices.Contains(args, "--version") {
		fmt.Println("fake tool version 1.0")
		return 0
	}

	// ffprobe, asking what codec is inside the container.
	if slices.Contains(args, "-show_entries") {
		codec := os.Getenv("GEDA_FAKE_TOOL_CODEC")
		if codec == "" {
			codec = "hevc"
		}
		fmt.Println(codec)
		return 0
	}

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "fake tool: nothing to do")
		return 2
	}
	// Every command core/formats builds puts the output path last.
	out := args[len(args)-1]

	switch mode {
	case modeFail:
		fmt.Fprintln(os.Stderr, "fake tool: banner line")
		fmt.Fprintln(os.Stderr, "fake tool: could not decode the input")
		return 1
	case modeSilent:
		os.Remove(out)
		return 0
	case modeEmpty:
		if err := os.WriteFile(out, nil, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	case modeSlow:
		select {}
	default:
		if err := os.WriteFile(out, []byte("converted:"+strings.Join(args, " ")), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
}
