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

package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/geda/geda-transfer/core/outbox"
)

// QueuedFile is one file waiting for a phone to collect it.
//
// It is outbox.Item without the source path. A front end has no use for where
// the file sits on disk, and a control socket that reported it would be
// answering a question nobody asked with a filesystem layout.
type QueuedFile struct {
	ID          string     `json:"id"`
	DeviceID    string     `json:"device_id"`
	Filename    string     `json:"filename"`
	Size        int64      `json:"size"`
	Kind        string     `json:"kind"`
	State       string     `json:"state"`
	Error       string     `json:"error,omitempty"`
	QueuedAt    time.Time  `json:"queued_at"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	SourcePath  string     `json:"source_path,omitempty"`
}

// Send queues files for a paired device to collect.
//
// Nothing is sent by this call and nothing can be: a suspended iPhone cannot
// be pushed to (AGENTS.md §3.7). The files are hashed in the background and
// offered the next time that phone asks what is waiting for it.
func (s *Service) Send(ctx context.Context, deviceID string, paths []string) ([]QueuedFile, error) {
	if len(paths) == 0 {
		return nil, errors.New("no files to send")
	}

	items, err := s.srv.Outbox().Add(ctx, deviceID, paths)
	if err != nil {
		return nil, err
	}

	out := make([]QueuedFile, 0, len(items))
	for _, item := range items {
		out = append(out, queuedFile(item, true))
	}
	return out, nil
}

// Outbox lists what is queued for a device, newest first.
func (s *Service) Outbox(ctx context.Context, deviceID string) ([]QueuedFile, error) {
	items, err := s.srv.Outbox().List(ctx, deviceID)
	if err != nil {
		return nil, err
	}

	out := make([]QueuedFile, 0, len(items))
	for _, item := range items {
		out = append(out, queuedFile(item, false))
	}
	return out, nil
}

// CancelSend drops one queued file. It has no effect on a file the phone has
// already collected -- those bytes are on the phone and this is not a sync
// product (AGENTS.md §1).
func (s *Service) CancelSend(ctx context.Context, deviceID, id string) error {
	err := s.srv.Outbox().Remove(ctx, deviceID, id)
	if errors.Is(err, outbox.ErrNotFound) {
		return fmt.Errorf("no queued file %q for that device", id)
	}
	return err
}

// ClearSent forgets everything that has been delivered or failed, leaving what
// is still waiting.
func (s *Service) ClearSent(ctx context.Context, deviceID string) (int, error) {
	return s.srv.Outbox().Clear(ctx, deviceID)
}

func queuedFile(item outbox.Item, withSource bool) QueuedFile {
	q := QueuedFile{
		ID:          item.ID,
		DeviceID:    item.DeviceID,
		Filename:    item.Filename,
		Size:        item.Size,
		Kind:        item.Kind,
		State:       string(item.State),
		Error:       item.Error,
		QueuedAt:    item.QueuedAt,
		DeliveredAt: item.DeliveredAt,
	}
	// Echoed straight back to the caller that supplied it, so `gedad send`
	// can report what it queued. It is never in a listing, and never leaves
	// the machine.
	if withSource {
		q.SourcePath = item.SourcePath
	}
	return q
}
