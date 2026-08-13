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

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		kind string
		file string
		want Class
	}{
		{"iphone still", KindPhoto, "IMG_0042.HEIC", ClassHEIC},
		{"heif", KindPhoto, "photo.heif", ClassHEIC},
		{"proraw", KindPhoto, "IMG_0042.DNG", ClassRAW},
		{"lower case dng", KindPhoto, "IMG_0042.dng", ClassRAW},
		{"sony raw", KindPhoto, "DSC00001.ARW", ClassRAW},
		{"canon raw", KindPhoto, "IMG_0001.CR3", ClassRAW},
		{"live photo video", KindVideo, "IMG_0042.MOV", ClassVideo},
		{"mp4", KindVideo, "clip.mp4", ClassVideo},
		{"jpeg", KindPhoto, "IMG_0042.JPG", ClassOther},
		{"png", KindPhoto, "screenshot.png", ClassOther},
		{"no extension, photo", KindPhoto, "IMG_0042", ClassOther},
		{"no extension, video", KindVideo, "IMG_0042", ClassVideo},

		// The kind is the sender saying "this is not library media", and it
		// beats the extension every time. Somebody backing up a video
		// project's .mov assets is transferring files, not photos.
		{"a mov inside a file transfer", KindFile, "render.mov", ClassOther},
		{"a heic inside a file transfer", KindFile, "asset.heic", ClassOther},
		{"an archive", KindFile, "backup.zip", ClassOther},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.kind, tc.file); got != tc.want {
				t.Fatalf("Classify(%q, %q) = %q, want %q", tc.kind, tc.file, got, tc.want)
			}
		})
	}
}

func TestPresetActions(t *testing.T) {
	cases := []struct {
		preset string
		want   map[Class]Action
	}{
		{PresetOriginal, map[Class]Action{
			ClassHEIC: ActionKeep, ClassVideo: ActionKeep,
			ClassRAW: ActionKeep, ClassOther: ActionKeep,
		}},
		{PresetCompatible, map[Class]Action{
			ClassHEIC: ActionSidecar, ClassVideo: ActionSidecar,
			ClassRAW: ActionKeep, ClassOther: ActionKeep,
		}},
		{PresetSpaceSaving, map[Class]Action{
			ClassHEIC: ActionReplace, ClassVideo: ActionReplace,
			ClassRAW: ActionKeep, ClassOther: ActionKeep,
		}},
	}

	for _, tc := range cases {
		t.Run(tc.preset, func(t *testing.T) {
			got := Policy{Preset: tc.preset}.Effective()
			for class, want := range tc.want {
				if got[class] != want {
					t.Fatalf("%s: %s is %q, want %q", tc.preset, class, got[class], want)
				}
			}
		})
	}
}

// A raw negative is the half of the P8 gate that says ProRAW keeps its DNG.
// There is no preset, no matrix, and no ledger row that can change it.
func TestRawIsNeverConverted(t *testing.T) {
	policies := []Policy{
		{Preset: PresetOriginal},
		{Preset: PresetCompatible},
		{Preset: PresetSpaceSaving},
		{Preset: PresetCustom, Matrix: map[Class]Action{ClassRAW: ActionReplace}},
		{Preset: PresetCustom, Matrix: map[Class]Action{ClassRAW: ActionSidecar}},
		{Preset: "something a later version invented"},
		{},
	}

	for _, p := range policies {
		if got := p.Action(ClassRAW); got != ActionKeep {
			t.Fatalf("policy %+v converts raw with %q", p, got)
		}
		d := p.Decide(File{Name: "IMG_0042.DNG", Kind: KindPhoto})
		if d.Class != ClassRAW || d.Action != ActionKeep {
			t.Fatalf("policy %+v decided %+v for a DNG", p, d)
		}
	}
}

// Replacing one member of a Live Photo leaves a still that no longer moves.
// The conversion still happens; only the deletion is refused.
func TestPairMemberIsNeverReplaced(t *testing.T) {
	p := Policy{Preset: PresetSpaceSaving}

	lone := p.Decide(File{Name: "IMG_0042.HEIC", Kind: KindPhoto})
	if lone.Action != ActionReplace {
		t.Fatalf("a lone HEIC got %q, want %q", lone.Action, ActionReplace)
	}

	paired := p.Decide(File{Name: "IMG_0042.HEIC", Kind: KindPhoto, Paired: true})
	if paired.Action != ActionSidecar {
		t.Fatalf("a paired HEIC got %q, want %q", paired.Action, ActionSidecar)
	}
	if paired.Downgraded == "" {
		t.Fatal("the downgrade was silent; the user asked for space back and did not get it")
	}

	video := p.Decide(File{Name: "IMG_0042.MOV", Kind: KindVideo, Paired: true})
	if video.Action != ActionSidecar {
		t.Fatalf("a paired MOV got %q, want %q", video.Action, ActionSidecar)
	}
}

func TestArbitraryFilesAreNeverTouched(t *testing.T) {
	for _, preset := range []string{PresetOriginal, PresetCompatible, PresetSpaceSaving} {
		p := Policy{Preset: preset}
		for _, name := range []string{"backup.zip", "notes.pdf", "clip.mov", "photo.heic"} {
			d := p.Decide(File{Name: name, Kind: KindFile})
			if d.Action != ActionKeep {
				t.Fatalf("%s did %q to %s, which was sent as a file", preset, d.Action, name)
			}
		}
	}
}

func TestValidate(t *testing.T) {
	valid := []Policy{
		{},
		{Preset: PresetOriginal},
		{Preset: PresetCompatible},
		{Preset: PresetSpaceSaving},
		{Preset: PresetCustom},
		{Preset: PresetCustom, Matrix: map[Class]Action{ClassHEIC: ActionSidecar, ClassRAW: ActionKeep}},
	}
	for _, p := range valid {
		if err := p.Validate(); err != nil {
			t.Fatalf("%+v should be valid: %v", p, err)
		}
	}

	invalid := []Policy{
		{Preset: "smallest"},
		{Preset: PresetCustom, Matrix: map[Class]Action{ClassHEIC: "delete"}},
		{Preset: PresetCustom, Matrix: map[Class]Action{ClassRAW: ActionReplace}},
		{Preset: PresetCustom, Matrix: map[Class]Action{"tiff": ActionSidecar}},
	}
	for _, p := range invalid {
		if err := p.Validate(); err == nil {
			t.Fatalf("%+v should not be valid", p)
		}
	}
}

func TestCustomMatrixFallsBackToKeep(t *testing.T) {
	p := Policy{Preset: PresetCustom, Matrix: map[Class]Action{ClassHEIC: ActionSidecar}}

	if got := p.Action(ClassHEIC); got != ActionSidecar {
		t.Fatalf("HEIC got %q", got)
	}
	// A class the matrix says nothing about is left alone, rather than
	// inheriting whatever the last preset happened to do.
	if got := p.Action(ClassVideo); got != ActionKeep {
		t.Fatalf("an unlisted class got %q, want %q", got, ActionKeep)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	cases := []Policy{
		{Preset: PresetOriginal},
		{Preset: PresetCompatible},
		{Preset: PresetSpaceSaving},
		{Preset: PresetCustom, Matrix: map[Class]Action{ClassHEIC: ActionSidecar, ClassVideo: ActionReplace}},
	}

	for _, want := range cases {
		preset, matrix, err := want.Marshal()
		if err != nil {
			t.Fatalf("Marshal(%+v): %v", want, err)
		}
		got := Unmarshal(preset, matrix)
		for _, class := range Classes {
			if got.Action(class) != want.Action(class) {
				t.Fatalf("%+v round-tripped to %+v: %s differs", want, got, class)
			}
		}
	}
}

// A ledger this receiver cannot make sense of must not start converting. The
// default is the only safe answer, because it is the one that changes nothing.
func TestUnmarshalFallsBackToTheDefault(t *testing.T) {
	cases := []struct{ preset, matrix string }{
		{"", ""},
		{PresetCustom, "not json"},
		{PresetCustom, `{"raw":"replace"}`},
		{PresetCustom, `{"heic":"delete"}`},
	}

	for _, tc := range cases {
		p := Unmarshal(tc.preset, tc.matrix)
		for _, class := range Classes {
			if p.Action(class) != ActionKeep {
				t.Fatalf("Unmarshal(%q, %q) converts %s with %q", tc.preset, tc.matrix, class, p.Action(class))
			}
		}
	}
}
