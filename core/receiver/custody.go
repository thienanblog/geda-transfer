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

package receiver

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/geda/geda-transfer/core/storage"
)

// The custody endpoint (docs/PROTOCOL.md §5.4).
//
// A sending device asks: can you still produce these exact bytes? It is asked
// by one feature only -- delete-after-transfer -- and it is the last thing
// standing between a bug and somebody's photographs, so it answers by reading
// the files rather than by consulting a column.
//
// It is deliberately not folded into the dedup probe of §4. That probe
// answers "probably already have it" from size, capture date and a head hash,
// which is the right trade for skipping an upload and nowhere near enough to
// authorise a deletion. Two questions with different consequences get two
// endpoints, so that no future change can quietly widen the cheap answer into
// the expensive one.

// confirmRequest is a batch of files a device is asking about.
type confirmRequest struct {
	Items []confirmItem `json:"items"`
}

type confirmItem struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type confirmResponse struct {
	Results []confirmResult `json:"results"`
}

type confirmResult struct {
	ID        string `json:"id"`
	Confirmed bool   `json:"confirmed"`
	Reason    string `json:"reason,omitempty"`
}

// maxConfirmItems bounds one batch.
//
// Much smaller than the dedup probe's thousand, and for a different reason:
// every item here is a full read of a file. A phone confirming a whole library
// sends several of these, which is the correct shape -- it gets answers it can
// act on while the rest are still being read, rather than one request that
// occupies the receiver for minutes.
const maxConfirmItems = 200

func (s *Server) handleConfirm(w http.ResponseWriter, r *http.Request) {
	deviceID, _, _ := DeviceFrom(r.Context())

	var req confirmRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "body must be JSON", false)
		return
	}
	if len(req.Items) > maxConfirmItems {
		writeError(w, http.StatusBadRequest, "too_many_items",
			fmt.Sprintf("at most %d items per confirmation", maxConfirmItems), false)
		return
	}

	reqs := make([]storage.CustodyRequest, 0, len(req.Items))
	for _, item := range req.Items {
		reqs = append(reqs, storage.CustodyRequest{
			ID:     item.ID,
			Path:   item.Path,
			Size:   item.Size,
			SHA256: item.SHA256,
		})
	}

	results, err := s.cfg.Files.Confirm(r.Context(), deviceID, reqs)
	if err != nil {
		s.log.Error("custody confirmation failed", "error", err)
		// Retryable, and the client's own rule does the rest: an answer it
		// did not get is not a confirmation, so nothing is deleted.
		writeError(w, http.StatusInternalServerError, "internal", "confirmation failed", true)
		return
	}

	out := make([]confirmResult, 0, len(results))
	for _, res := range results {
		out = append(out, confirmResult{ID: res.ID, Confirmed: res.Confirmed, Reason: res.Reason})
	}
	writeJSON(w, http.StatusOK, confirmResponse{Results: out})
}
