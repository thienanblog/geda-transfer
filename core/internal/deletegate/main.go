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

// Command deletegate drives the P9 gate.
//
//	"deliberate failure injection never deletes an unverified file"
//
// A phone deletes a photograph on the strength of one thing: this receiver
// saying it can still produce the bytes. So the gate is a list of ways to make
// that untrue, and the assertion in every one of them is that the receiver
// refuses.
//
// It runs against a real service over real TLS with a real pairing, and it
// breaks the files the way the world does: deleted, truncated, appended to,
// altered in place at the same length, removed by the space-saving preset,
// asked about by the wrong device.
//
// The half of the gate that is on the phone -- that a refusal, a silence, or
// a half-confirmed Live Photo all keep the asset -- is plain TypeScript and is
// checked by `mobile/src/engine/__tests__/deletion.test.ts`, driven from the
// same script.
//
// It is a test harness, not the product. See scripts/verify-p9.sh.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/geda/geda-transfer/core/client"
	"github.com/geda/geda-transfer/core/formats"
	"github.com/geda/geda-transfer/core/hash"
	"github.com/geda/geda-transfer/core/pairing"
	"github.com/geda/geda-transfer/core/receiver"
	"github.com/geda/geda-transfer/core/service"
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
	// the lines this gate is about.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := checkHonest(ctx, filepath.Join(*dir, "honest")); err != nil {
		return err
	}
	if err := checkInjected(ctx, filepath.Join(*dir, "injected")); err != nil {
		return err
	}
	return checkSpaceSaving(ctx, filepath.Join(*dir, "space-saving"))
}

// ---------------------------------------------------------------------------
// The control
// ---------------------------------------------------------------------------

// checkHonest is the case that must pass. Without it every later refusal
// would be indistinguishable from a receiver that simply refuses everything,
// and a gate that cannot tell those apart proves nothing at all.
func checkHonest(ctx context.Context, dir string) error {
	fmt.Println("==> a file that is still there")

	h, err := start(ctx, dir)
	if err != nil {
		return err
	}
	defer h.stop()

	content := bytes.Repeat([]byte("the photograph-"), 4096)
	stored, err := h.up.send(ctx, "IMG_0042.HEIC", "photo", content, nil)
	if err != nil {
		return err
	}

	res, err := h.confirm(ctx, item{ID: "a", Path: stored, Size: int64(len(content)), SHA256: sha256Hex(content)})
	if err != nil {
		return err
	}
	if !res.Confirmed {
		return fmt.Errorf("the receiver would not vouch for a file it holds: %s", res.Reason)
	}
	pass("the receiver vouches for a file it can still produce")

	// Asked twice, because the answer must come from the disk every time
	// rather than from a verdict it remembered.
	if err := os.WriteFile(h.abs(stored), []byte("something else entirely"), 0o600); err != nil {
		return err
	}
	res, err = h.confirm(ctx, item{ID: "a", Path: stored, Size: int64(len(content)), SHA256: sha256Hex(content)})
	if err != nil {
		return err
	}
	if res.Confirmed {
		return errors.New("the receiver vouched a second time from a remembered verdict")
	}
	pass("and stops vouching the moment the file changes, so nothing is cached")

	return nil
}

// ---------------------------------------------------------------------------
// The injections
// ---------------------------------------------------------------------------

// injection is one way of making the receiver's promise untrue.
type injection struct {
	name string
	// breaks the stored file, and returns the request to make. A zero request
	// means "ask about it honestly", so the case is testing the file rather
	// than the question.
	breaks func(h *harness, stored string, content []byte) (item, error)
	// want is the refusal reason. Empty means any refusal will do.
	want string
}

func checkInjected(ctx context.Context, dir string) error {
	fmt.Println()
	fmt.Println("==> deliberate failure injection")

	injections := []injection{
		{
			name: "the file was deleted from the destination",
			breaks: func(h *harness, stored string, _ []byte) (item, error) {
				return item{}, os.Remove(h.abs(stored))
			},
			want: "missing",
		},
		{
			name: "the file was truncated",
			breaks: func(h *harness, stored string, content []byte) (item, error) {
				return item{}, os.WriteFile(h.abs(stored), content[:len(content)/2], 0o600)
			},
			want: "size_mismatch",
		},
		{
			name: "the file was appended to",
			breaks: func(h *harness, stored string, content []byte) (item, error) {
				return item{}, os.WriteFile(h.abs(stored), append(append([]byte{}, content...), 'x'), 0o600)
			},
			want: "size_mismatch",
		},
		{
			// The one a size check cannot catch and a ledger lookup would
			// sail straight past. Silent corruption looks exactly like this.
			name: "one byte changed and the length did not",
			breaks: func(h *harness, stored string, content []byte) (item, error) {
				altered := append([]byte{}, content...)
				altered[len(altered)/2] ^= 0xFF
				return item{}, os.WriteFile(h.abs(stored), altered, 0o600)
			},
			want: "content_mismatch",
		},
		{
			name: "the file was replaced by an empty one",
			breaks: func(h *harness, stored string, _ []byte) (item, error) {
				return item{}, os.WriteFile(h.abs(stored), nil, 0o600)
			},
			want: "size_mismatch",
		},
		{
			name: "the file was replaced by a directory",
			breaks: func(h *harness, stored string, _ []byte) (item, error) {
				if err := os.Remove(h.abs(stored)); err != nil {
					return item{}, err
				}
				return item{}, os.MkdirAll(h.abs(stored), 0o755)
			},
			want: "missing",
		},
		{
			name: "the phone asks about a path the receiver never stored",
			breaks: func(_ *harness, _ string, content []byte) (item, error) {
				return item{ID: "a", Path: "2026/07/never-existed.HEIC",
					Size: int64(len(content)), SHA256: sha256Hex(content)}, nil
			},
			want: "unknown",
		},
		{
			// The client's path is a lookup key, never a path to open.
			name: "the phone asks about a path that climbs out of the destination",
			breaks: func(_ *harness, _ string, content []byte) (item, error) {
				return item{ID: "a", Path: "../../../etc/passwd",
					Size: int64(len(content)), SHA256: sha256Hex(content)}, nil
			},
			want: "unknown",
		},
		{
			name: "the phone's digest is for different content",
			breaks: func(_ *harness, stored string, content []byte) (item, error) {
				return item{ID: "a", Path: stored, Size: int64(len(content)),
					SHA256: sha256Hex([]byte("a different photograph"))}, nil
			},
			want: "content_mismatch",
		},
		{
			name: "the phone's size is not the stored size",
			breaks: func(_ *harness, stored string, content []byte) (item, error) {
				return item{ID: "a", Path: stored, Size: int64(len(content)) + 1,
					SHA256: sha256Hex(content)}, nil
			},
			want: "size_mismatch",
		},
		{
			name: "the phone offers no digest at all",
			breaks: func(_ *harness, stored string, content []byte) (item, error) {
				return item{ID: "a", Path: stored, Size: int64(len(content))}, nil
			},
			want: "bad_request",
		},
		{
			name: "the phone offers a digest that is not one",
			breaks: func(_ *harness, stored string, content []byte) (item, error) {
				return item{ID: "a", Path: stored, Size: int64(len(content)),
					SHA256: strings.Repeat("q", 64)}, nil
			},
			want: "bad_request",
		},
		{
			// Answered as unknown rather than as forbidden: the difference
			// between "not yours" and "does not exist" is itself information
			// about somebody else's files (docs/PROTOCOL.md §7).
			name: "another paired device asks about it",
			breaks: func(h *harness, stored string, content []byte) (item, error) {
				other, otherToken, err := pair(h.ctx, h.svc, "phone-2", "Someone else's iPhone")
				if err != nil {
					return item{}, err
				}
				h.asOther = &uploader{client: other, token: otherToken}
				return item{ID: "a", Path: stored, Size: int64(len(content)),
					SHA256: sha256Hex(content)}, nil
			},
			want: "unknown",
		},
	}

	for i, inj := range injections {
		if err := checkOne(ctx, filepath.Join(dir, fmt.Sprintf("case-%d", i)), inj); err != nil {
			return fmt.Errorf("%s: %w", inj.name, err)
		}
	}
	return nil
}

func checkOne(ctx context.Context, dir string, inj injection) error {
	h, err := start(ctx, dir)
	if err != nil {
		return err
	}
	defer h.stop()

	content := bytes.Repeat([]byte("the photograph-"), 4096)
	stored, err := h.up.send(ctx, "IMG_0042.HEIC", "photo", content, nil)
	if err != nil {
		return err
	}

	ask, err := inj.breaks(h, stored, content)
	if err != nil {
		return fmt.Errorf("inject the failure: %w", err)
	}
	if ask.ID == "" {
		ask = item{ID: "a", Path: stored, Size: int64(len(content)), SHA256: sha256Hex(content)}
	}

	res, err := h.confirm(ctx, ask)
	if err != nil {
		return err
	}

	if res.Confirmed {
		return fmt.Errorf("THE RECEIVER VOUCHED FOR A FILE IT CANNOT PRODUCE -- "+
			"a phone would have deleted %s", stored)
	}
	if inj.want != "" && res.Reason != inj.want {
		return fmt.Errorf("refused with %q, want %q -- the phone shows this to the user",
			res.Reason, inj.want)
	}
	pass("%s -> refused (%s)", inj.name, res.Reason)
	return nil
}

// ---------------------------------------------------------------------------
// The one the product does to itself
// ---------------------------------------------------------------------------

// checkSpaceSaving covers the failure that is not a bug: the space-saving
// preset deliberately deletes the received original, and from that moment the
// receiver cannot produce the bytes the phone is holding -- whatever
// `files.hash` still says about them (docs/DECISIONS.md).
//
// Every other case in this gate is somebody else breaking a file. This one is
// the product doing it on purpose, which makes it the one most likely to be
// quietly allowed through.
func checkSpaceSaving(ctx context.Context, dir string) error {
	fmt.Println()
	fmt.Println("==> after the space-saving preset removed the original")

	h, err := start(ctx, dir)
	if err != nil {
		return err
	}
	defer h.stop()

	if err := h.svc.DB().SetSetting(ctx, formats.SettingPreset, formats.PresetSpaceSaving); err != nil {
		return err
	}

	content := bytes.Repeat([]byte("h264-video-"), 8192)
	stored, err := h.up.send(ctx, "IMG_0042.MOV", "video", content, nil)
	if err != nil {
		return err
	}

	if _, err := h.svc.Conversions().ConvertPending(ctx); err != nil {
		return fmt.Errorf("run the conversions: %w", err)
	}
	if err := h.waitForDrain(ctx); err != nil {
		return err
	}

	// Whether this particular machine actually removed the original depends
	// on having a converter, so the gate asserts on the ledger rather than on
	// the disk: the rule is "a file marked as removed never confirms", and it
	// has to hold on a machine with no ffmpeg too.
	if err := h.markOriginalRemoved(ctx, stored); err != nil {
		return err
	}

	// The bytes are deliberately left exactly where they are. The point is
	// that the ledger's record of the removal is enough on its own -- a
	// receiver that only looked at the file would confirm this happily.
	res, err := h.confirm(ctx, item{ID: "a", Path: stored,
		Size: int64(len(content)), SHA256: sha256Hex(content)})
	if err != nil {
		return err
	}
	if res.Confirmed {
		return errors.New("THE RECEIVER VOUCHED FOR AN ORIGINAL IT DELETED ITSELF -- " +
			"the phone's copy would have been the last one")
	}
	if res.Reason != "original_removed" {
		return fmt.Errorf("refused with %q, want %q", res.Reason, "original_removed")
	}
	pass("a file the receiver removed itself never authorises a deletion (%s)", res.Reason)

	return nil
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type harness struct {
	ctx     context.Context
	svc     *service.Service
	dest    string
	up      *uploader
	asOther *uploader
	stop    func()
}

func start(parent context.Context, dir string) (*harness, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(parent)

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
		cancel()
		return nil, err
	}

	serving := make(chan error, 1)
	go func() { serving <- svc.Run(ctx) }()

	phone, token, err := pair(ctx, svc, "phone-1", "Gate iPhone")
	if err != nil {
		cancel()
		<-serving
		svc.Close()
		return nil, err
	}

	return &harness{
		ctx:  ctx,
		svc:  svc,
		dest: dest,
		up:   &uploader{client: phone, token: token},
		stop: func() {
			cancel()
			<-serving
			svc.Close()
		},
	}, nil
}

func (h *harness) abs(stored string) string {
	return filepath.Join(h.dest, filepath.FromSlash(stored))
}

// item is one file in a confirmation request (docs/PROTOCOL.md §5.4).
type item struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type result struct {
	ID        string `json:"id"`
	Confirmed bool   `json:"confirmed"`
	Reason    string `json:"reason"`
}

// confirm asks the receiver to vouch for one file, as the phone does.
func (h *harness) confirm(ctx context.Context, ask item) (result, error) {
	who := h.up
	if h.asOther != nil {
		who = h.asOther
	}

	body, err := json.Marshal(map[string]any{"items": []item{ask}})
	if err != nil {
		return result{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		who.client.URL("/v1/confirm"), bytes.NewReader(body))
	if err != nil {
		return result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+who.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := who.client.HTTP().Do(req)
	if err != nil {
		return result{}, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return result{}, fmt.Errorf("confirm: status %d: %s", resp.StatusCode, raw)
	}

	var out struct {
		Results []result `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return result{}, fmt.Errorf("decode the answer: %w", err)
	}
	if len(out.Results) != 1 {
		return result{}, fmt.Errorf("%d answers for one question", len(out.Results))
	}
	return out.Results[0], nil
}

// markOriginalRemoved records what a space-saving conversion does, so the rule
// is checked on machines with no converter installed as well.
func (h *harness) markOriginalRemoved(ctx context.Context, stored string) error {
	res, err := h.svc.DB().SQL().ExecContext(ctx,
		`UPDATE files SET original_removed_at = ? WHERE stored_path = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), stored)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("marked %d rows as removed, want 1 -- the ledger does not hold %s", n, stored)
	}
	return nil
}

func (h *harness) waitForDrain(ctx context.Context) error {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		pending, err := h.svc.Conversions().Pending(ctx)
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

// uploader sends one file through tus, the way the phone does.
type uploader struct {
	client *client.Client
	token  string
}

func (u *uploader) send(
	ctx context.Context, name, kind string, content []byte, extra map[string]string,
) (string, error) {
	digest, err := hash.Reader(ctx, bytes.NewReader(content))
	if err != nil {
		return "", err
	}

	metadata := map[string]string{
		"filename":    name,
		"kind":        kind,
		"hash":        digest.Full,
		"captured_at": time.Date(2026, 7, 4, 15, 9, 3, 0, time.UTC).Format(time.RFC3339),
	}
	for k, v := range extra {
		metadata[k] = v
	}

	create, err := http.NewRequestWithContext(ctx, http.MethodPost,
		u.client.URL(receiver.UploadPath), nil)
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

	patch, err := http.NewRequestWithContext(ctx, http.MethodPatch, location, bytes.NewReader(content))
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

	raw, err := base64.StdEncoding.DecodeString(resp.Header.Get("Geda-Stored-Path"))
	if err != nil {
		return "", fmt.Errorf("decode the stored path: %w", err)
	}
	if len(raw) == 0 {
		return "", errors.New("the receiver did not say where the file went")
	}
	return string(raw), nil
}

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

func pair(ctx context.Context, svc *service.Service, id, name string) (*client.Client, string, error) {
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

	c, res, err := client.PairWith(ctx, payload,
		client.Device{ID: id, Name: name, Platform: "ios"},
		client.Config{})
	if err != nil {
		return nil, "", err
	}
	return c, res.Token, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func pass(format string, args ...any) {
	fmt.Printf("  ok: "+format+"\n", args...)
}
