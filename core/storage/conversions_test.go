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

package storage_test

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/geda/geda-transfer/core/formats"
	"github.com/geda/geda-transfer/core/storage"
)

// What the ledger holds about one file's conversion.
type queued struct {
	Class      string
	Action     string
	State      string
	SourcePath string
	Note       string
}

func (f *fixture) preset(name string) {
	f.t.Helper()
	if err := f.db.SetSetting(f.t.Context(), formats.SettingPreset, name); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) custom(matrix string) {
	f.t.Helper()
	f.preset(formats.PresetCustom)
	if err := f.db.SetSetting(f.t.Context(), formats.SettingMatrix, matrix); err != nil {
		f.t.Fatal(err)
	}
}

// queuedFor returns the conversion recorded for a stored path, if any.
func (f *fixture) queuedFor(storedPath string) (queued, bool) {
	f.t.Helper()

	var q queued
	err := f.db.SQL().QueryRowContext(f.t.Context(), `
		SELECT class, action, state, source_path, note
		FROM conversions WHERE source_path = ?`, storedPath).Scan(
		&q.Class, &q.Action, &q.State, &q.SourcePath, &q.Note)
	if errors.Is(err, sql.ErrNoRows) {
		return queued{}, false
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return q, true
}

func (f *fixture) conversionCount() int {
	f.t.Helper()
	var n int
	if err := f.db.SQL().QueryRowContext(f.t.Context(),
		`SELECT COUNT(*) FROM conversions`).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// The default costs nothing: a receiver nobody has configured never writes a
// conversion row, so the table stays empty on every installation that has not
// asked for one.
func TestCommitQueuesNothingByDefault(t *testing.T) {
	f := newFixture(t)

	for _, name := range []string{"IMG_1.HEIC", "IMG_2.MOV", "IMG_3.DNG", "IMG_4.JPG"} {
		got := f.commit(storage.Incoming{
			OriginalName: name, CapturedAt: captured, Kind: kindFor(name),
		}, "bytes of "+name)
		if got.Conversion.Action != formats.ActionKeep {
			t.Fatalf("%s: the default preset decided %q", name, got.Conversion.Action)
		}
	}

	if n := f.conversionCount(); n != 0 {
		t.Fatalf("the default preset queued %d conversions", n)
	}
}

func TestCommitQueuesASidecarUnderCompatible(t *testing.T) {
	f := newFixture(t)
	f.preset(formats.PresetCompatible)

	got := f.commit(storage.Incoming{
		OriginalName: "IMG_4021.HEIC", CapturedAt: captured,
	}, "heic bytes")

	q, ok := f.queuedFor(got.Path)
	if !ok {
		t.Fatal("nothing was queued for a HEIC under the Compatible preset")
	}
	if q.Class != string(formats.ClassHEIC) || q.Action != string(formats.ActionSidecar) {
		t.Fatalf("queued %+v", q)
	}
	if q.State != formats.StatePending {
		t.Fatalf("state is %q, want %q", q.State, formats.StatePending)
	}
	if got.Conversion.Action != formats.ActionSidecar {
		t.Fatalf("Commit reported %q", got.Conversion.Action)
	}
}

// The half of the P8 gate that says a ProRAW file keeps its DNG. It is
// asserted here, on the path a real upload takes, and not only in the policy's
// own tests.
func TestCommitNeverQueuesARawNegative(t *testing.T) {
	for _, preset := range []string{
		formats.PresetOriginal, formats.PresetCompatible, formats.PresetSpaceSaving,
	} {
		t.Run(preset, func(t *testing.T) {
			f := newFixture(t)
			f.preset(preset)

			got := f.commit(storage.Incoming{
				OriginalName: "IMG_4021.DNG", CapturedAt: captured,
			}, "proraw bytes")

			if _, ok := f.queuedFor(got.Path); ok {
				t.Fatalf("%s queued a conversion for a DNG", preset)
			}
			if got.Conversion.Action != formats.ActionKeep {
				t.Fatalf("%s decided %q for a DNG", preset, got.Conversion.Action)
			}
			// And the file on disk is still the DNG that arrived.
			if f.read(got.Path) != "proraw bytes" {
				t.Fatal("the stored DNG is not what was sent")
			}
			if got.Path[len(got.Path)-4:] != ".DNG" {
				t.Fatalf("stored as %q; the extension did not survive", got.Path)
			}
		})
	}
}

// A custom matrix reaches Decide through the ledger, where nothing validated
// it. Converting somebody's negatives is not recoverable, so the refusal is
// enforced again on this path.
func TestCommitRefusesARawConversionFromTheLedger(t *testing.T) {
	f := newFixture(t)
	f.custom(`{"raw":"replace","heic":"sidecar"}`)

	raw := f.commit(storage.Incoming{OriginalName: "IMG_1.DNG", CapturedAt: captured}, "proraw bytes")
	if _, ok := f.queuedFor(raw.Path); ok {
		t.Fatal("a hand-edited ledger row got a DNG converted")
	}

	// A matrix that fails validation falls back to the default, which
	// converts nothing at all -- including the HEIC it also named.
	heic := f.commit(storage.Incoming{OriginalName: "IMG_2.HEIC", CapturedAt: captured}, "heic bytes")
	if _, ok := f.queuedFor(heic.Path); ok {
		t.Fatal("an unusable matrix was partly honoured; it must fall back whole")
	}
}

// Replacing one member of a Live Photo leaves a still that no longer moves.
// Both members are still converted -- only the deletion is refused.
func TestCommitDowngradesReplaceForPairMembers(t *testing.T) {
	f := newFixture(t)
	f.preset(formats.PresetSpaceSaving)

	photo := f.commit(storage.Incoming{
		OriginalName: "IMG_4021.HEIC", CapturedAt: captured,
		PairID: "pair-1", PairRole: storage.RolePrimary,
	}, "heic bytes")
	video := f.commit(storage.Incoming{
		OriginalName: "IMG_4021.MOV", CapturedAt: captured, Kind: storage.KindVideo,
		PairID: "pair-1", PairRole: storage.RoleSecondary,
	}, "mov bytes")

	for _, got := range []storage.Committed{photo, video} {
		q, ok := f.queuedFor(got.Path)
		if !ok {
			t.Fatalf("%s: nothing was queued", got.Path)
		}
		if q.Action != string(formats.ActionSidecar) {
			t.Fatalf("%s: action is %q, want %q", got.Path, q.Action, formats.ActionSidecar)
		}
		if q.Note == "" {
			t.Fatalf("%s: the downgrade was silent", got.Path)
		}
	}

	// And the pair still shares one basename, which is the other half of the
	// P8 gate.
	if base(photo.Path) != base(video.Path) {
		t.Fatalf("the pair was split: %q and %q", photo.Path, video.Path)
	}
}

func TestCommitQueuesAReplaceForALoneVideo(t *testing.T) {
	f := newFixture(t)
	f.preset(formats.PresetSpaceSaving)

	got := f.commit(storage.Incoming{
		OriginalName: "IMG_4021.MOV", CapturedAt: captured, Kind: storage.KindVideo,
	}, "hevc bytes")

	q, ok := f.queuedFor(got.Path)
	if !ok {
		t.Fatal("nothing was queued for a lone video under Space-saving")
	}
	if q.Action != string(formats.ActionReplace) {
		t.Fatalf("action is %q, want %q", q.Action, formats.ActionReplace)
	}
}

// An arbitrary file is transferred, not processed -- whatever it is called.
func TestCommitNeverQueuesAnArbitraryFile(t *testing.T) {
	f := newFixture(t)
	f.preset(formats.PresetSpaceSaving)

	got := f.commit(storage.Incoming{
		OriginalName: "render.mov", CapturedAt: captured, Kind: storage.KindFile,
	}, "project asset")

	if _, ok := f.queuedFor(got.Path); ok {
		t.Fatal("a file transfer got transcoded")
	}
}

// A file the receiver already had is not stored again, so there is nothing new
// to convert either. Queueing one would convert the existing copy a second
// time on every re-run.
func TestCommitQueuesNothingForADuplicate(t *testing.T) {
	f := newFixture(t)
	f.preset(formats.PresetCompatible)

	first := f.commit(storage.Incoming{OriginalName: "IMG_4021.HEIC", CapturedAt: captured}, "heic bytes")
	second := f.commit(storage.Incoming{OriginalName: "IMG_4021.HEIC", CapturedAt: captured}, "heic bytes")

	if !second.Deduplicated {
		t.Fatal("the second upload was not deduplicated")
	}
	if second.Conversion.Action != formats.ActionKeep {
		t.Fatalf("a deduplicated upload decided %q", second.Conversion.Action)
	}
	if n := f.conversionCount(); n != 1 {
		t.Fatalf("%d conversions queued for one stored file", n)
	}
	if _, ok := f.queuedFor(first.Path); !ok {
		t.Fatal("the first upload's conversion is missing")
	}
}

func TestCommitWakesTheConverter(t *testing.T) {
	f := newFixture(t)
	f.preset(formats.PresetCompatible)

	woken := 0
	f.NotifyConversions(func() { woken++ })

	f.commit(storage.Incoming{OriginalName: "IMG_1.HEIC", CapturedAt: captured}, "heic bytes")
	if woken != 1 {
		t.Fatalf("the converter was woken %d times, want 1", woken)
	}

	// A file with nothing to do must not wake it: on a library of JPEGs that
	// would be one wake-up per photo for no work.
	f.commit(storage.Incoming{OriginalName: "IMG_2.JPG", CapturedAt: captured}, "jpeg bytes")
	if woken != 1 {
		t.Fatalf("a file with no conversion woke the converter (%d)", woken)
	}
}

func kindFor(name string) string {
	switch formats.Classify(storage.KindPhoto, name) {
	case formats.ClassVideo:
		return storage.KindVideo
	default:
		return storage.KindPhoto
	}
}

// base is a stored path without its extension.
func base(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return path[:i]
		}
		if path[i] == '/' {
			break
		}
	}
	return path
}
