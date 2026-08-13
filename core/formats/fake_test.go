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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// Standing in for ffmpeg and heif-convert.
//
// core/internal/faketool is built once for the whole package and then pointed
// at by the GEDA_* overrides, so the tests exercise the real search, the real
// exec, and the real argument vectors -- everything except the encoder.

const (
	envFake  = "GEDA_FAKE_TOOL_MODE"
	envCodec = "GEDA_FAKE_TOOL_CODEC"
)

// Behaviours, matching core/internal/faketool.
const (
	fakeOK     = "ok"
	fakeFail   = "fail"
	fakeEmpty  = "empty"
	fakeSilent = "silent"
)

var (
	fakeOnce sync.Once
	fakePath string
	fakeErr  error
)

// fakeToolPath builds the stand-in, once.
func fakeToolPath(t *testing.T) string {
	t.Helper()

	fakeOnce.Do(func() {
		if _, err := exec.LookPath("go"); err != nil {
			fakeErr = err
			return
		}

		dir, err := os.MkdirTemp("", "geda-faketool-")
		if err != nil {
			fakeErr = err
			return
		}

		out := filepath.Join(dir, "faketool")
		if runtime.GOOS == "windows" {
			out += ".exe"
		}

		cmd := exec.Command("go", "build", "-o", out, "github.com/geda/geda-transfer/core/internal/faketool")
		if combined, err := cmd.CombinedOutput(); err != nil {
			fakeErr = fmtErr(err, combined)
			return
		}
		fakePath = out
	})

	if fakeErr != nil {
		t.Fatalf("could not build the stand-in converter: %v", fakeErr)
	}
	return fakePath
}

func fmtErr(err error, output []byte) error {
	if len(output) == 0 {
		return err
	}
	return &buildError{err: err, output: string(output)}
}

type buildError struct {
	err    error
	output string
}

func (e *buildError) Error() string { return e.err.Error() + ": " + e.output }
func (e *buildError) Unwrap() error { return e.err }

// fakeTools points every tool at the stand-in, in the given mode.
func fakeTools(t *testing.T, mode string) Tools {
	t.Helper()

	path := fakeToolPath(t)
	t.Setenv(envFake, mode)
	t.Setenv("GEDA_FFMPEG", path)
	t.Setenv("GEDA_FFPROBE", path)
	t.Setenv("GEDA_HEIF_CONVERT", path)
	return Detect(t.Context())
}

// noTools is a machine with nothing installed.
func noTools(t *testing.T) Tools {
	t.Helper()

	missing := filepath.Join(t.TempDir(), "not-installed")
	t.Setenv("GEDA_FFMPEG", missing)
	t.Setenv("GEDA_FFPROBE", missing)
	t.Setenv("GEDA_HEIF_CONVERT", missing)
	return Detect(t.Context())
}
