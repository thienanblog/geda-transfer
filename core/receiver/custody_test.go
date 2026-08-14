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

package receiver_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The endpoint a phone asks before it deletes anything (docs/PROTOCOL.md §5.4).

type confirmItem struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type confirmResult struct {
	ID        string `json:"id"`
	Confirmed bool   `json:"confirmed"`
	Reason    string `json:"reason"`
}

// send posts a confirmation batch with the given token and returns the raw
// response, so tests can assert on refusals as well as on answers.
func (h *harness) confirmWith(token string, items []confirmItem) *http.Response {
	h.t.Helper()

	body, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		h.t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodPost, h.URL+"/v1/confirm", bytes.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	return h.do(req)
}

func (h *harness) confirm(items []confirmItem) []confirmResult {
	h.t.Helper()

	resp := h.confirmWith(testToken, items)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		h.t.Fatalf("confirm: status %d, body %s", resp.StatusCode, raw)
	}

	var out struct {
		Results []confirmResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatalf("confirm: %v", err)
	}
	return out.Results
}

// store uploads content and returns where it landed.
func (h *harness) store(name string, content []byte) string {
	h.t.Helper()

	loc := h.create(len(content), map[string]string{
		"filename": name,
		"hash":     digestOf(h.t, content).Full,
		"kind":     "photo",
	})
	resp := h.patch(loc, 0, content)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		raw, _ := io.ReadAll(resp.Body)
		h.t.Fatalf("upload %s: status %d, body %s", name, resp.StatusCode, raw)
	}
	return h.storedPath(resp)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestConfirmVouchesForAFileItStillHolds(t *testing.T) {
	h := newHarness(t)
	content := payload(4096, 1)

	path := h.store("IMG_1.HEIC", content)

	results := h.confirm([]confirmItem{
		{ID: "a", Path: path, Size: int64(len(content)), SHA256: sha256Hex(content)},
	})
	if len(results) != 1 {
		t.Fatalf("%d results, want 1", len(results))
	}
	if !results[0].Confirmed {
		t.Fatalf("not confirmed: %s", results[0].Reason)
	}
}

// A file belonging to somebody else is answered as unknown rather than as
// forbidden. The difference between "not yours" and "does not exist" is
// itself information about another person's files (docs/PROTOCOL.md §7).
func TestConfirmWillNotVouchForAnotherDevicesFile(t *testing.T) {
	h := newHarness(t)
	content := payload(2048, 2)

	path := h.store("IMG_1.HEIC", content)
	h.addDevice("dev-2", "Another iPhone", "another-token")

	resp := h.confirmWith("another-token", []confirmItem{
		{ID: "a", Path: path, Size: int64(len(content)), SHA256: sha256Hex(content)},
	})
	defer resp.Body.Close()

	var out struct {
		Results []confirmResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Results[0].Confirmed {
		t.Fatal("vouched for a file belonging to another device")
	}
	if out.Results[0].Reason != "unknown" {
		t.Fatalf("reason %q, want unknown", out.Results[0].Reason)
	}
}

func TestConfirmNeedsAToken(t *testing.T) {
	h := newHarness(t)

	resp := h.confirmWith("", []confirmItem{{ID: "a", Path: "x", Size: 1, SHA256: strings.Repeat("0", 64)}})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
}

// Every item costs a full read of a file, so the batch is bounded far below
// the dedup probe's thousand.
func TestConfirmBoundsTheBatch(t *testing.T) {
	h := newHarness(t)

	items := make([]confirmItem, 201)
	for i := range items {
		items[i] = confirmItem{ID: fmt.Sprint(i), Path: "x", Size: 1, SHA256: strings.Repeat("0", 64)}
	}

	resp := h.confirmWith(testToken, items)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
}

func TestConfirmRejectsABodyThatIsNotJSON(t *testing.T) {
	h := newHarness(t)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.URL+"/v1/confirm",
		strings.NewReader("not json"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)

	resp := h.do(req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
}

// The response the phone acts on: one answer per item, in order, with the
// refusals distinguishable from the confirmations.
func TestConfirmAnswersAMixedBatchInOrder(t *testing.T) {
	h := newHarness(t)

	kept := payload(1024, 3)
	keptPath := h.store("IMG_1.HEIC", kept)

	gone := payload(1024, 4)
	gonePath := h.store("IMG_2.HEIC", gone)
	if err := os.Remove(filepath.Join(h.root, filepath.FromSlash(gonePath))); err != nil {
		t.Fatal(err)
	}

	results := h.confirm([]confirmItem{
		{ID: "kept", Path: keptPath, Size: int64(len(kept)), SHA256: sha256Hex(kept)},
		{ID: "gone", Path: gonePath, Size: int64(len(gone)), SHA256: sha256Hex(gone)},
		{ID: "never", Path: "2026/07/never.HEIC", Size: 10, SHA256: sha256Hex([]byte("x"))},
	})

	if len(results) != 3 {
		t.Fatalf("%d results, want 3", len(results))
	}
	want := []struct {
		id        string
		confirmed bool
	}{{"kept", true}, {"gone", false}, {"never", false}}
	for i, w := range want {
		if results[i].ID != w.id || results[i].Confirmed != w.confirmed {
			t.Fatalf("result %d: %+v, want id %s confirmed %v", i, results[i], w.id, w.confirmed)
		}
	}
	if results[1].Reason == "" || results[2].Reason == "" {
		t.Fatal("a refusal with no reason: the user cannot be told what happened")
	}
}
