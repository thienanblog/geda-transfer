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

// Package formats decides what, if anything, happens to a file after it has
// been received.
//
// The phone sends originals and never transcodes (AGENTS.md §3.3), so every
// conversion in the product happens here, on a real CPU, after the bytes are
// safely on disk. This package owns the policy; running the external tools is
// the other half of the package, in tools.go and convert.go.
//
// Three rules are not configurable, and each of them is a data-loss bug if it
// is relaxed:
//
//   - A raw negative is never converted. It is the thing the user kept the
//     disk space for, and there is no output that is not a downgrade.
//   - A member of a Live Photo or RAW+JPEG pair never has its original
//     replaced. Half a converted pair is a still photo that no longer moves
//     and a video nobody will open.
//   - An arbitrary file -- a ZIP, a PDF, a project folder -- is never touched
//     at all. This product transfers those; it does not process them.
package formats

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Preset is the user-facing choice. Custom is what the advanced per-type
// matrix saves as.
const (
	PresetOriginal    = "original"
	PresetCompatible  = "compatible"
	PresetSpaceSaving = "space-saving"
	PresetCustom      = "custom"
)

// Presets in the order a settings screen should offer them.
var Presets = []string{PresetOriginal, PresetCompatible, PresetSpaceSaving}

// Class is a family of received file, as far as conversion is concerned.
//
// It is deliberately coarser than a file type. "Which container is this" is a
// question an extension can answer; "which codec is inside it" is not -- an
// iPhone .MOV holds HEVC or H.264 depending on a setting the sender may have
// changed years ago -- so the class says only that a file is video and the
// converter probes for the codec before deciding there is work to do.
type Class string

const (
	// ClassHEIC is a HEIC/HEIF still, the format an iPhone shoots by default.
	ClassHEIC Class = "heic"

	// ClassRAW is a raw negative: ProRAW's DNG, or any camera's raw file.
	ClassRAW Class = "raw"

	// ClassVideo is any video container.
	ClassVideo Class = "video"

	// ClassOther is everything else -- JPEG, PNG, and anything unrecognised.
	ClassOther Class = "other"
)

// Classes in the order the advanced matrix should list them.
var Classes = []Class{ClassHEIC, ClassVideo, ClassRAW, ClassOther}

// Action is what the receiver does with a file of a given class.
type Action string

const (
	// ActionKeep leaves the file exactly as it arrived.
	ActionKeep Action = "keep"

	// ActionSidecar writes a converted copy beside the original, sharing its
	// basename. Both files survive; the destination grows.
	ActionSidecar Action = "sidecar"

	// ActionReplace writes the converted copy and then removes the received
	// original, once the copy has been verified to exist and decode.
	//
	// This is the only destructive action in the package, it is never a
	// default, and it is refused for pair members and raw negatives.
	ActionReplace Action = "replace"
)

// Actions in the order the advanced matrix should offer them.
var Actions = []Action{ActionKeep, ActionSidecar, ActionReplace}

// Kinds of file, matching storage's. An arbitrary file is never converted, so
// the kind is part of classification rather than something the caller checks.
const (
	KindPhoto = "photo"
	KindVideo = "video"
	KindFile  = "file"
)

// Policy is the user's output configuration.
//
// The zero value is the default: originals, untouched. That matters more than
// it looks -- a receiver whose settings could not be read must not start
// converting, and a zero value that meant "space-saving" would delete
// originals on a machine whose ledger was merely unreadable.
type Policy struct {
	// Preset is one of the four constants above.
	Preset string `json:"preset"`

	// Matrix is consulted only when Preset is PresetCustom. A class missing
	// from it falls back to ActionKeep.
	Matrix map[Class]Action `json:"matrix,omitempty"`
}

// Default is what a receiver nobody has configured uses.
func Default() Policy { return Policy{Preset: PresetOriginal} }

// presetMatrix is the fixed action table behind each named preset.
//
// ClassRAW is ActionKeep in every one of them, and Action() enforces that
// again for custom policies. It is stated twice on purpose: this is the half
// of the P8 gate that says a ProRAW file keeps its DNG.
var presetMatrix = map[string]map[Class]Action{
	PresetOriginal: {
		ClassHEIC:  ActionKeep,
		ClassVideo: ActionKeep,
		ClassRAW:   ActionKeep,
		ClassOther: ActionKeep,
	},
	// Compatible is additive: it answers "this Mac from 2013 cannot open my
	// photos" without answering it by throwing the photos away.
	PresetCompatible: {
		ClassHEIC:  ActionSidecar,
		ClassVideo: ActionSidecar,
		ClassRAW:   ActionKeep,
		ClassOther: ActionKeep,
	},
	// Space-saving is the destructive one, and the only reason to choose it
	// is that the disk is the constraint.
	PresetSpaceSaving: {
		ClassHEIC:  ActionReplace,
		ClassVideo: ActionReplace,
		ClassRAW:   ActionKeep,
		ClassOther: ActionKeep,
	},
}

// Validate rejects a policy that cannot be applied, with a message a person
// can act on.
func (p Policy) Validate() error {
	switch p.Preset {
	case "", PresetOriginal, PresetCompatible, PresetSpaceSaving:
		return nil
	case PresetCustom:
	default:
		return fmt.Errorf("%q is not an output preset; choose one of %s or %s",
			p.Preset, strings.Join(Presets, ", "), PresetCustom)
	}

	for _, class := range sortedClasses(p.Matrix) {
		action := p.Matrix[class]
		switch action {
		case ActionKeep, ActionSidecar, ActionReplace:
		default:
			return fmt.Errorf("%q is not something that can be done to a %s file", action, class)
		}
		if class == ClassRAW && action != ActionKeep {
			// Refused rather than quietly corrected. Somebody who set this
			// meant to convert their negatives, and finding out months later
			// that it silently did nothing is worse than being told now.
			return fmt.Errorf("a raw negative is always kept as it arrived; %q cannot be applied to it", action)
		}
		if _, known := knownClass[class]; !known {
			return fmt.Errorf("%q is not a file class", class)
		}
	}
	return nil
}

var knownClass = func() map[Class]struct{} {
	m := make(map[Class]struct{}, len(Classes))
	for _, c := range Classes {
		m[c] = struct{}{}
	}
	return m
}()

// Action reports what happens to a file of this class.
func (p Policy) Action(class Class) Action {
	// Stated once in the preset tables and again here, because a custom
	// matrix reaches this function without passing Validate on an older
	// ledger row, and a raw negative that got converted cannot be recovered.
	if class == ClassRAW {
		return ActionKeep
	}

	if p.Preset == PresetCustom {
		if action, ok := p.Matrix[class]; ok {
			return action
		}
		return ActionKeep
	}

	table, ok := presetMatrix[p.Preset]
	if !ok {
		// An unset or unrecognised preset behaves as Original. A receiver
		// updated from a future version's ledger keeps the user's files
		// rather than guessing at their intent.
		return ActionKeep
	}
	return table[class]
}

// Effective is the full action table this policy resolves to, for a settings
// screen that shows what a preset actually does.
func (p Policy) Effective() map[Class]Action {
	out := make(map[Class]Action, len(Classes))
	for _, class := range Classes {
		out[class] = p.Action(class)
	}
	return out
}

// File is what the policy is asked about.
type File struct {
	// Name is the stored filename; only its extension is read.
	Name string

	// Kind is photo, video, or file.
	Kind string

	// Paired is true when this file is one member of a Live Photo or
	// RAW+JPEG group.
	Paired bool
}

// Decision is what to do with one received file.
type Decision struct {
	Class  Class
	Action Action

	// Downgraded records that a replace became a sidecar, and why. The user
	// asked for the disk space back and did not get it, so the reason has to
	// survive as far as the screen that shows it.
	Downgraded string
}

// Decide resolves the policy for one file.
func (p Policy) Decide(f File) Decision {
	class := Classify(f.Kind, f.Name)
	d := Decision{Class: class, Action: p.Action(class)}

	if d.Action == ActionReplace && f.Paired {
		// Replacing one member of a Live Photo leaves a still that no longer
		// moves and a MOV with nothing to pair it to. The converted copy is
		// still useful, so the work is done -- only the deletion is refused.
		d.Action = ActionSidecar
		d.Downgraded = "kept the original: it is one half of a pair, and replacing it would break the pair"
	}
	return d
}

// Classify sorts a received file into a class.
//
// An arbitrary file is ClassOther whatever it is called: this product
// transfers a .mov inside somebody's video project untouched, and a kind of
// "file" is the sender saying it is not library media.
func Classify(kind, name string) Class {
	if kind == KindFile {
		return ClassOther
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	switch {
	case heicExt[ext]:
		return ClassHEIC
	case rawExt[ext]:
		return ClassRAW
	case videoExt[ext]:
		return ClassVideo
	}

	// The extension did not settle it, so the sender's kind does. A phone
	// that sends a video with no extension at all still gets a video's
	// treatment rather than being left alone by accident.
	switch kind {
	case KindVideo:
		return ClassVideo
	default:
		return ClassOther
	}
}

var heicExt = set("heic", "heif", "hif", "avci")

// rawExt is deliberately broad. This list decides only what is never
// converted, so an extension that does not belong here costs nothing, while a
// raw format missing from it would be transcoded away.
var rawExt = set(
	"dng",               // Apple ProRAW, Adobe, Leica, Ricoh
	"arw", "srf", "sr2", // Sony
	"cr2", "cr3", "crw", // Canon
	"nef", "nrw", // Nikon
	"orf", // Olympus
	"raf", // Fujifilm
	"rw2", // Panasonic
	"pef", // Pentax
	"srw", // Samsung
	"3fr", // Hasselblad
	"iiq", // Phase One
	"erf", // Epson
	"mef", // Mamiya
	"mos", // Leaf
	"x3f", // Sigma
	"raw",
)

var videoExt = set("mov", "mp4", "m4v", "hevc", "avi", "mkv", "mpg", "mpeg", "3gp", "webm")

func set(values ...string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}

func sortedClasses(m map[Class]Action) []Class {
	out := make([]Class, 0, len(m))
	for class := range m {
		out = append(out, class)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Settings keys. They share core's namespace with the naming template so that
// gedad and the desktop app, reading the same ledger, cannot disagree about
// what a receiver does with its files.
const (
	SettingPreset = "output_preset"
	SettingMatrix = "output_matrix"
)

// Marshal encodes the custom matrix for the ledger.
func (p Policy) Marshal() (preset string, matrix string, err error) {
	preset = p.Preset
	if preset == "" {
		preset = PresetOriginal
	}
	if p.Preset != PresetCustom || len(p.Matrix) == 0 {
		return preset, "", nil
	}
	raw, err := json.Marshal(p.Matrix)
	if err != nil {
		return "", "", fmt.Errorf("encode the output matrix: %w", err)
	}
	return preset, string(raw), nil
}

// Unmarshal is the inverse, tolerating anything the ledger might hold.
//
// A malformed row yields the default policy rather than an error: the receiver
// has to keep receiving, and "kept your originals because the setting could
// not be read" is a recoverable state, where refusing to start is not.
func Unmarshal(preset, matrix string) Policy {
	p := Policy{Preset: strings.TrimSpace(preset)}
	if p.Preset == "" {
		p.Preset = PresetOriginal
	}
	if p.Preset != PresetCustom || strings.TrimSpace(matrix) == "" {
		return p
	}
	if err := json.Unmarshal([]byte(matrix), &p.Matrix); err != nil {
		return Default()
	}
	if p.Validate() != nil {
		return Default()
	}
	return p
}
