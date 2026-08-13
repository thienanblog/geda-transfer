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

package outbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"
)

// columns is the select list every scan expects, in order.
const columns = `id, device_id, source_path, filename, size, source_mtime,
	sha256, kind, captured_at, state, error, queued_at, delivered_at`

// maxList bounds a listing. A user who has queued more than this has a
// backlog, not a screen, and the phone works through it in batches anyway.
const maxList = 500

// List returns everything queued for a device, newest first. It is what the
// window shows.
func (q *Queue) List(ctx context.Context, deviceID string) ([]Item, error) {
	return q.scanMany(ctx, `SELECT `+columns+` FROM outbox
		WHERE device_id = ? ORDER BY queued_at DESC, id DESC LIMIT ?`,
		deviceID, maxList)
}

// Waiting returns everything queued for a device that has not been delivered,
// oldest first. It is what a window summarises as "waiting for the phone".
func (q *Queue) Waiting(ctx context.Context, deviceID string) ([]Item, error) {
	return q.scanMany(ctx, `SELECT `+columns+` FROM outbox
		WHERE device_id = ? AND state IN ('pending', 'ready', 'claimed')
		ORDER BY queued_at, id LIMIT ?`,
		deviceID, maxList)
}

// Offer returns what the phone should be told about: hashed, not yet
// delivered, oldest first.
//
// Items still being hashed are deliberately absent rather than listed without
// a digest. A phone that downloaded one could not verify it, and the whole
// point of the digest is that nothing is saved to a photo library unverified.
func (q *Queue) Offer(ctx context.Context, deviceID string) ([]Item, error) {
	return q.scanMany(ctx, `SELECT `+columns+` FROM outbox
		WHERE device_id = ? AND state IN ('ready', 'claimed') AND sha256 != ''
		ORDER BY queued_at, id LIMIT ?`,
		deviceID, maxList)
}

// Item looks one up, scoped to the device that asked.
//
// A wrong device gets ErrNotFound rather than a permission error, so that one
// phone cannot use the difference to discover what another phone was sent.
func (q *Queue) Item(ctx context.Context, deviceID, id string) (Item, error) {
	return q.scanOne(ctx, `SELECT `+columns+` FROM outbox
		WHERE id = ? AND device_id = ?`, id, deviceID)
}

// Open returns the file behind an item, ready to be served.
//
// The size and mtime recorded when the item was hashed are re-checked here.
// Serving a file that has changed since would send bytes the digest does not
// describe, and the phone would discard the whole download after paying for
// all of it -- so the item fails now, with a reason a person can act on.
func (q *Queue) Open(ctx context.Context, deviceID, id string) (*os.File, Item, error) {
	item, err := q.Item(ctx, deviceID, id)
	if err != nil {
		return nil, Item{}, err
	}
	if !item.Ready() {
		if item.State == StateFailed {
			return nil, item, fmt.Errorf("%w: %s", ErrNotReady, item.Error)
		}
		return nil, item, ErrNotReady
	}

	f, err := os.Open(item.SourcePath)
	if err != nil {
		// Only a file that is *gone* is a dead item. Everything else -- a
		// network volume that is briefly away, a process at its open-file
		// limit while several phones collect at once -- is very likely to
		// work on the next attempt, and failing the item for it would need a
		// person to notice and queue the file again.
		if errors.Is(err, os.ErrNotExist) {
			_ = q.fail(ctx, item.ID, describeMissing(err))
			return nil, item, fmt.Errorf("%w: %s", ErrSourceChanged, describeMissing(err))
		}
		return nil, item, fmt.Errorf("outbox: open %s: %w", item.SourcePath, err)
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, item, err
	}
	if info.Size() != item.Size || format(info.ModTime().UTC()) != item.sourceMtime {
		f.Close()
		_ = q.fail(ctx, item.ID, "the file was modified after it was queued")
		return nil, item, ErrSourceChanged
	}

	return f, item, nil
}

// Claim records that the bytes have been handed over at least once. It is not
// a promise that they arrived: only the phone's acknowledgement is that.
func (q *Queue) Claim(ctx context.Context, deviceID, id string) error {
	_, err := q.db.SQL().ExecContext(ctx, `
		UPDATE outbox SET state = 'claimed'
		WHERE id = ? AND device_id = ? AND state = 'ready'`, id, deviceID)
	if err != nil {
		return fmt.Errorf("outbox: claim %s: %w", id, err)
	}
	return nil
}

// Deliver records the phone's acknowledgement, which it sends only after
// recomputing the digest and matching it (docs/PROTOCOL.md §6).
//
// It is idempotent. A phone that acknowledges twice -- because the first
// response was lost, or because a background session handed the app the same
// finished download after a relaunch -- must not get an error for being
// careful.
func (q *Queue) Deliver(ctx context.Context, deviceID, id string) error {
	item, err := q.Item(ctx, deviceID, id)
	if err != nil {
		return err
	}
	if item.State == StateDelivered {
		return nil
	}

	_, err = q.db.SQL().ExecContext(ctx, `
		UPDATE outbox SET state = 'delivered', delivered_at = ?, error = ''
		WHERE id = ? AND device_id = ?`, format(time.Now().UTC()), id, deviceID)
	if err != nil {
		return fmt.Errorf("outbox: acknowledge %s: %w", id, err)
	}
	return nil
}

// Remove drops an item. This is the local user cancelling something they
// queued, which is why it is a delete: an item the phone never collected is
// not history worth keeping.
func (q *Queue) Remove(ctx context.Context, deviceID, id string) error {
	result, err := q.db.SQL().ExecContext(ctx,
		`DELETE FROM outbox WHERE id = ? AND device_id = ?`, id, deviceID)
	if err != nil {
		return fmt.Errorf("outbox: remove %s: %w", id, err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Clear drops every item for a device that has already been delivered or has
// failed, leaving what is still waiting. It is the window's "clear finished".
func (q *Queue) Clear(ctx context.Context, deviceID string) (int, error) {
	result, err := q.db.SQL().ExecContext(ctx, `
		DELETE FROM outbox
		WHERE device_id = ? AND state IN ('delivered', 'failed')`, deviceID)
	if err != nil {
		return 0, fmt.Errorf("outbox: clear: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

func (q *Queue) fail(ctx context.Context, id, reason string) error {
	// context.WithoutCancel: a failure discovered while a request is being
	// abandoned is still worth recording, and is exactly the case where the
	// context is already dead.
	_, err := q.db.SQL().ExecContext(context.WithoutCancel(ctx), `
		UPDATE outbox SET state = 'failed', error = ? WHERE id = ?`,
		trimmed(reason), id)
	if err != nil {
		return fmt.Errorf("outbox: record failure: %w", err)
	}
	return nil
}

// describeMissing turns an open error into something a person can act on.
func describeMissing(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "the file is no longer there"
	}
	if errors.Is(err, os.ErrPermission) {
		return "the file cannot be read"
	}
	return err.Error()
}

func (q *Queue) scanOne(ctx context.Context, query string, args ...any) (Item, error) {
	items, err := q.scanMany(ctx, query, args...)
	if err != nil {
		return Item{}, err
	}
	if len(items) == 0 {
		return Item{}, ErrNotFound
	}
	return items[0], nil
}

func (q *Queue) scanMany(ctx context.Context, query string, args ...any) ([]Item, error) {
	rows, err := q.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("outbox: query: %w", err)
	}
	defer rows.Close()

	var items []Item
	for rows.Next() {
		var (
			item       Item
			state      string
			queuedAt   string
			captured   sql.NullString
			delivered  sql.NullString
			sourceTime string
		)
		if err := rows.Scan(&item.ID, &item.DeviceID, &item.SourcePath, &item.Filename,
			&item.Size, &sourceTime, &item.SHA256, &item.Kind, &captured, &state,
			&item.Error, &queuedAt, &delivered); err != nil {
			return nil, fmt.Errorf("outbox: scan: %w", err)
		}

		item.State = State(state)
		item.QueuedAt = parseTime(queuedAt)
		item.CapturedAt = parseTimePtr(captured)
		item.DeliveredAt = parseTimePtr(delivered)
		item.sourceMtime = sourceTime

		items = append(items, item)
	}
	return items, rows.Err()
}
