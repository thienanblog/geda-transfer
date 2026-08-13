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
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/geda/geda-transfer/core/events"
	"github.com/geda/geda-transfer/core/outbox"
)

// OutboxPath is where a phone collects what has been queued for it.
const OutboxPath = "/v1/outbox"

// Outbox is the queue of files waiting for phones to collect. Front ends fill
// it; this server hands it out (docs/PROTOCOL.md §6).
func (s *Server) Outbox() *outbox.Queue { return s.outbox }

// outboxListResponse is the "anything for me?" answer.
type outboxListResponse struct {
	Items []outboxItem `json:"items"`
}

type outboxItem struct {
	ID string `json:"id"`

	// Filename is the name on the receiver. On the phone this is untrusted
	// input: it has crossed a network, and a name is not a path until it has
	// been sanitised.
	Filename string `json:"filename"`

	Size int64 `json:"size"`

	// SHA256 and not the BLAKE3 used for uploads. See docs/DECISIONS.md.
	SHA256 string `json:"sha256"`

	Kind       string `json:"kind"`
	CapturedAt string `json:"captured_at,omitempty"`
	URL        string `json:"url"`
}

func (s *Server) handleOutboxList(w http.ResponseWriter, r *http.Request) {
	deviceID, _, _ := DeviceFrom(r.Context())

	items, err := s.outbox.Offer(r.Context(), deviceID)
	if err != nil {
		s.log.Error("could not list the outbox", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not read the queue", true)
		return
	}

	out := outboxListResponse{Items: make([]outboxItem, 0, len(items))}
	for _, item := range items {
		entry := outboxItem{
			ID:       item.ID,
			Filename: item.Filename,
			Size:     item.Size,
			SHA256:   item.SHA256,
			Kind:     item.Kind,
			URL:      OutboxPath + "/" + item.ID,
		}
		if item.CapturedAt != nil {
			entry.CapturedAt = item.CapturedAt.Format(time.RFC3339)
		}
		out.Items = append(out.Items, entry)
	}

	writeJSON(w, http.StatusOK, out)
}

// handleOutboxFetch serves the bytes of one item.
//
// http.ServeContent does the work, which is not laziness: it implements Range
// and If-Range correctly, and a background URLSession resumes an interrupted
// download with exactly those headers. Hand-rolling them is how a 2 GB
// download ends up starting again from zero on a train.
func (s *Server) handleOutboxFetch(w http.ResponseWriter, r *http.Request) {
	deviceID, deviceName, _ := DeviceFrom(r.Context())
	id := r.PathValue("id")

	f, item, err := s.outbox.Open(r.Context(), deviceID, id)
	switch {
	case errors.Is(err, outbox.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such item", false)
		return
	case errors.Is(err, outbox.ErrNotReady):
		// Retryable: the receiver is still hashing it, and will not be for
		// long. The phone comes back rather than treating this as a failure.
		writeError(w, http.StatusConflict, "not_ready",
			"that file is still being prepared", true)
		return
	case errors.Is(err, outbox.ErrSourceChanged):
		writeError(w, http.StatusGone, "source_gone",
			"that file changed on the sending computer after it was queued", false)
		return
	case err != nil:
		s.log.Error("could not open a queued file", "error", err, "item", id)
		writeError(w, http.StatusInternalServerError, "internal", "could not read the file", true)
		return
	}
	defer f.Close()

	if err := s.outbox.Claim(r.Context(), deviceID, id); err != nil {
		s.log.Warn("could not record a claim", "error", err, "item", id)
	}

	// The digest is a perfect validator: it changes exactly when the content
	// does, which is what makes an interrupted download safe to resume.
	w.Header().Set("ETag", strconv.Quote(item.SHA256))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if r.Method == http.MethodHead {
		http.ServeContent(w, r, item.Filename, item.QueuedAt, f)
		return
	}

	base := events.Event{
		Direction:  events.DirectionOutbound,
		UploadID:   outboxEventID(item.ID),
		DeviceID:   item.DeviceID,
		DeviceName: deviceName,
		Name:       item.Filename,
		AssetKind:  item.Kind,
		Size:       item.Size,
	}

	from := rangeStart(r.Header.Get("Range"))
	started := base
	started.Kind = events.KindStarted
	started.At = time.Now()
	started.Offset = from
	s.cfg.Events.Publish(started)

	progress := events.NewProgress(s.cfg.Events, base, from)
	counted := &countingWriter{ResponseWriter: w, progress: progress}

	http.ServeContent(counted, r, item.Filename, item.QueuedAt, f)
	progress.Flush()

	// A download that stopped short is not a failure: the phone's background
	// session will resume it by Range, possibly days later. Only reaching the
	// end of the file is worth reporting, and even that is not delivery --
	// the phone says that, after it has recomputed the digest.
	if progress.Offset() >= item.Size {
		done := base
		done.Kind = events.KindFinished
		done.At = time.Now()
		done.Offset = item.Size
		s.cfg.Events.Publish(done)
	}
}

// handleOutboxAck records that the phone has the file and verified it.
func (s *Server) handleOutboxAck(w http.ResponseWriter, r *http.Request) {
	deviceID, _, _ := DeviceFrom(r.Context())
	id := r.PathValue("id")

	err := s.outbox.Deliver(r.Context(), deviceID, id)
	if errors.Is(err, outbox.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "no such item", false)
		return
	}
	if err != nil {
		s.log.Error("could not record a delivery", "error", err, "item", id)
		writeError(w, http.StatusInternalServerError, "internal", "could not record it", true)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// outboxEventID namespaces an outbox item in the event stream, which is shared
// with uploads and keyed by one identifier.
func outboxEventID(id string) string { return "outbox:" + id }

// countingWriter reports the body bytes ServeContent writes.
//
// It deliberately implements nothing but http.ResponseWriter. ServeContent
// needs no more than that, and a wrapper that claimed to be a Flusher or a
// Hijacker without being one would be a worse bug than the feature is worth.
type countingWriter struct {
	http.ResponseWriter
	progress *events.Progress
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	if n > 0 {
		_, _ = c.progress.Write(p[:n])
	}
	return n, err
}

// rangeStart reads the first byte position out of a Range header, for the
// benefit of the progress figure only. Anything it cannot parse is reported as
// zero and left to ServeContent, which is the component that actually decides
// what to send.
func rangeStart(header string) int64 {
	spec, ok := strings.CutPrefix(strings.TrimSpace(header), "bytes=")
	if !ok {
		return 0
	}
	first, _, _ := strings.Cut(spec, ",")
	start, _, ok := strings.Cut(first, "-")
	if !ok {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(start), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
