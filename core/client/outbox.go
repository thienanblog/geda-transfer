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

package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// OutboxItem is one file a receiver is holding for this device
// (docs/PROTOCOL.md §6).
type OutboxItem struct {
	ID string `json:"id"`

	// Filename is the name on the sending computer. It has crossed a network:
	// on the receiving side it is untrusted text, not a path.
	Filename string `json:"filename"`

	Size int64 `json:"size"`

	// SHA256 is what the downloaded bytes must hash to before anything is
	// saved. Not BLAKE3; see docs/DECISIONS.md.
	SHA256     string `json:"sha256"`
	Kind       string `json:"kind"`
	CapturedAt string `json:"captured_at,omitempty"`
	URL        string `json:"url"`
}

// Outbox asks the receiver what is waiting for this device.
func (c *Client) Outbox(ctx context.Context) ([]OutboxItem, error) {
	var out struct {
		Items []OutboxItem `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/outbox", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Fetch streams one item into w, starting at from.
//
// It deliberately does not use the control-request timeout: this is the one
// call whose duration is a property of the file rather than of the receiver,
// and a 2 GB archive on a slow link is not a hung request. Cancellation is
// ctx's job.
//
// Nothing is verified here. The caller hashes what it writes and compares the
// result to the item's digest, because only the caller knows whether w already
// holds the bytes before from.
func (c *Client) Fetch(ctx context.Context, item OutboxItem, w io.Writer, from int64) (int64, error) {
	path := item.URL
	if path == "" {
		path = "/v1/outbox/" + item.ID
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL(path), nil)
	if err != nil {
		return 0, fmt.Errorf("client: build request: %w", err)
	}

	c.mu.Lock()
	token := c.token
	c.mu.Unlock()
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	if from > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(from, 10)+"-")
		// If-Range makes the resume safe: a receiver whose file has changed
		// answers with the whole of the new one instead of a tail that would
		// be spliced onto bytes it no longer follows. The caller sees 200
		// rather than 206 and knows to start again.
		if item.SHA256 != "" {
			req.Header.Set("If-Range", strconv.Quote(item.SHA256))
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("client: fetch %s: %w", item.ID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return 0, decodeError(resp)
	}
	if from > 0 && resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("%w: the receiver sent the whole file (HTTP %d)",
			ErrRestartRequired, resp.StatusCode)
	}

	n, err := io.Copy(w, resp.Body)
	if err != nil {
		return n, fmt.Errorf("client: fetch %s: %w", item.ID, err)
	}
	return n, nil
}

// AckOutbox tells the receiver the file arrived and its digest matched.
//
// It is sent only after that check. Acknowledging first and verifying later
// would let a corrupted download retire the only copy on the sending machine's
// queue, which is the one mistake this direction cannot afford.
func (c *Client) AckOutbox(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/outbox/"+id, nil, nil)
}
