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

// Command formatsgate drives the P8 gate.
//
//	"Live Photo round-trips as a linked pair; ProRAW keeps its DNG"
//
// Both halves are properties of this repository and both are checked here: a
// real receiver over real TLS, a real pairing, a HEIC and a MOV uploaded as
// one pair, and a DNG uploaded beside them -- under every output preset,
// including the destructive one.
//
// What it asserts is what the sentence says.
//
//   - The pair shares one basename and differs only by extension, so Photos
//     and every file browser see one Live Photo rather than two files.
//   - The DNG comes back byte for byte, under its own extension, with no
//     conversion queued against it. No preset, no matrix, and no hand-edited
//     ledger row can change that.
//
// It is a test harness, not the product. See scripts/verify-p8.sh.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/geda/geda-transfer/core/client"
	"github.com/geda/geda-transfer/core/formats"
	"github.com/geda/geda-transfer/core/hash"
	"github.com/geda/geda-transfer/core/pairing"
	"github.com/geda/geda-transfer/core/service"
	"github.com/geda/geda-transfer/core/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func run() error {
	dir := flag.String("dir", "", "working directory (required)")
	flag.Parse()

	if *dir == "" {
		return errors.New("-dir is required")
	}

	// tusd logs every request through the default logger, which would bury
	// the three lines this gate is actually about.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Every preset, including the one whose whole purpose is to delete
	// originals, and two hand-written matrices. A gate that only checked the
	// default would prove nothing: the rules this phase is about are the ones
	// that hold when the user has asked for conversion.
	cases := []scenario{
		{
			name:   `preset "original"`,
			preset: formats.PresetOriginal,
			// The default converts nothing, so the queue stays empty on every
			// installation that has not asked for anything.
			wantQueued: 0,
		},
		{
			name:       `preset "compatible"`,
			preset:     formats.PresetCompatible,
			wantQueued: 2,
		},
		{
			name:       `preset "space-saving"`,
			preset:     formats.PresetSpaceSaving,
			wantQueued: 2,
		},
		{
			// The destructive matrix, spelled out. Both pair members are
			// converted and neither is replaced.
			name:       `a custom matrix that replaces photos and videos`,
			preset:     formats.PresetCustom,
			matrix:     `{"heic":"replace","video":"replace"}`,
			wantQueued: 2,
		},
		{
			// A matrix nothing validated on the way in -- a hand-edited row,
			// or one written by a version that allowed something this one
			// does not. It falls back whole, so even the HEIC it also named
			// is left alone.
			name:       `a custom matrix that asks for raw to be replaced`,
			preset:     formats.PresetCustom,
			matrix:     `{"heic":"replace","raw":"replace"}`,
			wantQueued: 0,
		},
	}

	for i, sc := range cases {
		fmt.Printf("==> %s\n", sc.name)
		if err := check(ctx, filepath.Join(*dir, fmt.Sprintf("case-%d", i)), sc); err != nil {
			return fmt.Errorf("%s: %w", sc.name, err)
		}
	}

	return nil
}

// scenario is one output policy to put the three files through.
type scenario struct {
	name       string
	preset     string
	matrix     string
	wantQueued int
}

// The three files. A Live Photo is one asset the phone sends as two uploads
// sharing a pair_id; the ProRAW shot is one DNG.
const (
	stillName  = "IMG_0042.HEIC"
	motionName = "IMG_0042.MOV"
	rawName    = "IMG_0099.DNG"
	pairID     = "live-0042"
)

func check(parent context.Context, dir string, sc scenario) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	ctx, stop := context.WithCancel(parent)
	defer stop()

	dest := filepath.Join(dir, "Received")
	svc, err := service.Open(ctx, service.Config{
		Name:     "Gate Mac",
		StateDir: filepath.Join(dir, "state"),
		Dest:     dest,
		Listen:   "127.0.0.1:0",
		// A gate must not need the product's fixed UDP port, or it cannot run
		// on a machine that is already receiving.
		Discovery: false,
		MDNS:      false,
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if err != nil {
		return err
	}
	defer svc.Close()

	// Written straight into the ledger rather than through service.Config,
	// which validates. The custom case is deliberately a policy nothing
	// checked on the way in -- a hand-edited row, or one written by a version
	// that allowed something this one does not -- and going through the front
	// door would turn that case into a test of the front door.
	if err := setPolicy(ctx, svc, sc.preset, sc.matrix); err != nil {
		return err
	}

	serving := make(chan error, 1)
	go func() { serving <- svc.Run(ctx) }()
	defer func() {
		stop()
		<-serving
	}()

	phone, token, err := pair(ctx, svc)
	if err != nil {
		return err
	}

	still := bytes.Repeat([]byte("heic-still-"), 4096)
	motion := bytes.Repeat([]byte("live-photo-motion-"), 8192)
	negative := bytes.Repeat([]byte("proraw-negative-"), 16384)

	up := &uploader{client: phone, token: token}

	// Deliberately motion first. A background URLSession schedules tasks when
	// it pleases, so the receiver must not require the still to arrive first
	// (docs/PROTOCOL.md §5.1).
	motionPath, err := up.send(ctx, motionName, "video", motion, map[string]string{
		"pair_id": pairID, "pair_role": storage.RoleSecondary,
	})
	if err != nil {
		return fmt.Errorf("upload the Live Photo's video: %w", err)
	}
	stillPath, err := up.send(ctx, stillName, "photo", still, map[string]string{
		"pair_id": pairID, "pair_role": storage.RolePrimary,
	})
	if err != nil {
		return fmt.Errorf("upload the Live Photo's still: %w", err)
	}
	rawPath, err := up.send(ctx, rawName, "photo", negative, nil)
	if err != nil {
		return fmt.Errorf("upload the ProRAW negative: %w", err)
	}

	// --- Live Photo round-trips as a linked pair ---------------------------

	if base(stillPath) != base(motionPath) {
		return fmt.Errorf("the pair was split: %q and %q do not share a basename",
			stillPath, motionPath)
	}
	if ext(stillPath) == ext(motionPath) {
		return fmt.Errorf("both halves of the pair are %q; one would have overwritten the other",
			ext(stillPath))
	}
	if !strings.EqualFold(ext(stillPath), "HEIC") || !strings.EqualFold(ext(motionPath), "MOV") {
		return fmt.Errorf("the pair arrived as %q and %q; the extensions did not survive",
			stillPath, motionPath)
	}
	if err := sameBytes(dest, stillPath, still); err != nil {
		return fmt.Errorf("the still: %w", err)
	}
	if err := sameBytes(dest, motionPath, motion); err != nil {
		return fmt.Errorf("the motion: %w", err)
	}
	pass("the Live Photo is one basename with two extensions: %s", base(motionPath))

	// --- ProRAW keeps its DNG ---------------------------------------------

	if !strings.EqualFold(ext(rawPath), "DNG") {
		return fmt.Errorf("the negative arrived as %q; ProRAW did not keep its DNG", rawPath)
	}
	if err := sameBytes(dest, rawPath, negative); err != nil {
		return fmt.Errorf("the negative: %w", err)
	}
	pass("the negative is byte-for-byte the DNG that was sent: %s", rawPath)

	// --- and the converter is not allowed to change either of those --------

	// Everything the policy asked for has to have happened before the ledger
	// is read, or a gate on a fast machine passes and the same code on a slow
	// one does not.
	//
	// The service runs its own worker, woken by each commit, so this races it
	// and then waits for whichever of the two ends up holding the last row.
	if _, err := svc.Conversions().ConvertPending(ctx); err != nil {
		return fmt.Errorf("run the conversions: %w", err)
	}
	if err := waitForDrain(ctx, svc); err != nil {
		return err
	}

	queued, err := svc.Conversions().Recent(ctx, 100)
	if err != nil {
		return err
	}

	if len(queued) != sc.wantQueued {
		return fmt.Errorf("%d conversions queued, want %d: %s", len(queued), sc.wantQueued,
			describe(queued))
	}

	converted := 0
	for _, item := range queued {
		if item.SourcePath == rawPath {
			return fmt.Errorf("a conversion was queued against the negative: %s %s",
				item.Class, item.Action)
		}
		if item.Action == formats.ActionReplace {
			return fmt.Errorf("%s was queued to have its original replaced; "+
				"it is one half of a Live Photo", item.SourcePath)
		}
		if item.State == formats.StateDone {
			converted++
			if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(item.OutputPath))); err != nil {
				return fmt.Errorf("%s is recorded as converted but %s is not there: %w",
					item.SourcePath, item.OutputPath, err)
			}
			// The converted copy shares the pair's basename, so a Live Photo
			// with a JPEG beside it is still one group in a file browser.
			if base(item.OutputPath) != base(item.SourcePath) {
				return fmt.Errorf("the converted copy of %s was filed as %s, "+
					"which is not the same basename", item.SourcePath, item.OutputPath)
			}
		}
	}

	// The files are still there afterwards. This is the assertion the
	// space-saving preset exists to threaten, and it is why the gate runs
	// with a converter installed rather than on a machine with none.
	for _, stored := range []string{stillPath, motionPath, rawPath} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(stored))); err != nil {
			return fmt.Errorf("%s is gone after the converter ran: %w", stored, err)
		}
	}
	pass("%d conversions queued (%d ran), none against the negative, "+
		"none replacing a pair member, every original still on disk",
		len(queued), converted)

	return nil
}

// waitForDrain blocks until nothing is queued or running.
func waitForDrain(ctx context.Context, svc *service.Service) error {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		pending, err := svc.Conversions().Pending(ctx)
		if err != nil {
			return fmt.Errorf("count the conversions: %w", err)
		}
		if pending == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d conversions were still running after two minutes", pending)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// describe lists a queue, for a failure message worth reading.
func describe(items []formats.Item) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, fmt.Sprintf("%s %s/%s->%s", item.SourcePath, item.Class,
			item.Action, item.State))
	}
	if len(parts) == 0 {
		return "(nothing)"
	}
	return strings.Join(parts, ", ")
}

// setPolicy writes an output policy into the ledger, validated or not.
func setPolicy(ctx context.Context, svc *service.Service, preset, matrix string) error {
	if err := svc.DB().SetSetting(ctx, formats.SettingPreset, preset); err != nil {
		return err
	}
	return svc.DB().SetSetting(ctx, formats.SettingMatrix, matrix)
}

// uploader sends one file through tus, the way the phone does.
type uploader struct {
	client *client.Client
	token  string
}

// send uploads content and returns where the receiver stored it.
func (u *uploader) send(
	ctx context.Context, name, kind string, content []byte, extra map[string]string,
) (string, error) {
	digest, err := hash.Reader(ctx, bytes.NewReader(content))
	if err != nil {
		return "", err
	}

	metadata := map[string]string{
		"filename": name,
		"kind":     kind,
		"hash":     digest.Full,
		// A capture date, because the receiver files by it and stamps it on
		// the stored file's mtime.
		"captured_at": time.Date(2026, 7, 4, 15, 9, 3, 0, time.UTC).Format(time.RFC3339),
	}
	for k, v := range extra {
		metadata[k] = v
	}

	create, err := http.NewRequestWithContext(ctx, http.MethodPost,
		u.client.URL("/v1/files"), nil)
	if err != nil {
		return "", err
	}
	create.Header.Set("Authorization", "Bearer "+u.token)
	create.Header.Set("Tus-Resumable", "1.0.0")
	create.Header.Set("Upload-Length", strconv.Itoa(len(content)))
	create.Header.Set("Upload-Metadata", encodeMetadata(metadata))

	resp, err := u.client.HTTP().Do(create)
	if err != nil {
		return "", err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("create: status %d: %s", resp.StatusCode, body)
	}
	location := resp.Header.Get("Location")
	if location == "" {
		return "", errors.New("create: no Location header")
	}

	patch, err := http.NewRequestWithContext(ctx, http.MethodPatch, location,
		bytes.NewReader(content))
	if err != nil {
		return "", err
	}
	patch.Header.Set("Authorization", "Bearer "+u.token)
	patch.Header.Set("Tus-Resumable", "1.0.0")
	patch.Header.Set("Upload-Offset", "0")
	patch.Header.Set("Content-Type", "application/offset+octet-stream")

	resp, err = u.client.HTTP().Do(patch)
	if err != nil {
		return "", err
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return "", fmt.Errorf("patch: status %d: %s", resp.StatusCode, body)
	}

	// Base64 of UTF-8: an HTTP header is Latin-1 by specification, so a path
	// with any non-ASCII character in it cannot be sent raw (§5.3).
	raw, err := base64.StdEncoding.DecodeString(resp.Header.Get("Geda-Stored-Path"))
	if err != nil {
		return "", fmt.Errorf("decode the stored path: %w", err)
	}
	if len(raw) == 0 {
		return "", errors.New("the receiver did not say where the file went")
	}
	return string(raw), nil
}

// encodeMetadata builds a tus Upload-Metadata header.
func encodeMetadata(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+" "+base64.StdEncoding.EncodeToString([]byte(values[k])))
	}
	return strings.Join(parts, ",")
}

func pair(ctx context.Context, svc *service.Service) (*client.Client, string, error) {
	offer, err := svc.Pair(ctx, time.Minute)
	if err != nil {
		return nil, "", err
	}
	payload, err := pairing.Decode(offer.URI)
	if err != nil {
		return nil, "", err
	}
	// The offer advertises every interface; the gate listens on loopback.
	payload.Addrs = []string{svc.Addr().String()}

	c, result, err := client.PairWith(ctx, payload,
		client.Device{ID: "phone-1", Name: "Gate iPhone", Platform: "ios"},
		client.Config{})
	if err != nil {
		return nil, "", err
	}
	return c, result.Token, nil
}

// sameBytes checks that the stored file is what was sent.
func sameBytes(root, stored string, want []byte) error {
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(stored)))
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("%s holds %d bytes, not the %d that were sent", stored, len(got), len(want))
	}
	return nil
}

// base is a stored path without its extension.
func base(stored string) string {
	name := path.Base(stored)
	if i := strings.LastIndex(name, "."); i > 0 {
		name = name[:i]
	}
	return path.Join(path.Dir(stored), name)
}

// ext is a stored path's extension, without the dot.
func ext(stored string) string {
	return strings.TrimPrefix(path.Ext(stored), ".")
}

func pass(format string, args ...any) {
	fmt.Printf("  ok: "+format+"\n", args...)
}
