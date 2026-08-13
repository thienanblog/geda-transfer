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

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/geda/geda-transfer/core/store"
)

// The conversion queue.
//
// Conversions are a queue and not something the upload path waits for, and
// that is the single most important property in this file. A transfer must
// never be slower, or fail, because a converter is busy or missing: the bytes
// are already on disk and already recorded before anything here runs.

// States a conversion row may be in.
const (
	StatePending = "pending"
	StateRunning = "running"
	StateDone    = "done"
	StateSkipped = "skipped"
	StateFailed  = "failed"
)

// ErrNotFound reports that there is no conversion to do.
var ErrNotFound = errors.New("no conversion found")

// Item is one row of the queue.
type Item struct {
	ID         int64
	FileID     int64
	DeviceID   string
	SourcePath string
	Class      Class
	Action     Action
	State      string
	OutputPath string
	OutputSize int64
	Tool       string
	Note       string
	Error      string
	QueuedAt   time.Time
	FinishedAt *time.Time
}

// Request is what storage hands over when a received file needs converting.
type Request struct {
	FileID     int64
	DeviceID   string
	SourcePath string
	Class      Class
	Action     Action
	Note       string
	CapturedAt time.Time
}

// Queue converts received files in the background.
type Queue struct {
	db        *store.DB
	root      string
	converter *Converter
	log       *slog.Logger
	workers   int

	wake chan struct{}
}

// QueueConfig describes a queue to build.
type QueueConfig struct {
	// DB is the ledger. Required.
	DB *store.DB

	// Root is the absolute destination directory. Required: rows hold
	// destination-relative paths, exactly as files.stored_path does.
	Root string

	// Converter runs the tools. Required.
	Converter *Converter

	// Workers is how many conversions run at once. Zero picks a number from
	// the machine.
	Workers int

	Logger *slog.Logger
}

// NewQueue builds a conversion queue.
func NewQueue(cfg QueueConfig) (*Queue, error) {
	if cfg.DB == nil {
		return nil, errors.New("formats: a ledger is required")
	}
	if cfg.Converter == nil {
		return nil, errors.New("formats: a converter is required")
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve destination %s: %w", cfg.Root, err)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Workers <= 0 {
		cfg.Workers = defaultWorkers()
	}

	return &Queue{
		db:        cfg.DB,
		root:      root,
		converter: cfg.Converter,
		log:       cfg.Logger,
		workers:   cfg.Workers,
		wake:      make(chan struct{}, 1),
	}, nil
}

// defaultWorkers leaves the machine usable while it converts.
//
// ffmpeg and heif-convert are already multi-threaded, so running one per core
// does not go faster -- it only makes the desktop unresponsive and the fans
// loud on a laptop whose owner is trying to work.
func defaultWorkers() int {
	n := runtime.NumCPU() / 4
	if n < 1 {
		return 1
	}
	if n > 4 {
		return 4
	}
	return n
}

// Enqueue records a conversion to be done later.
//
// It is called after the received file is in its final place, never before: a
// worker that found a row pointing at a name still being renamed into would
// convert a zero-byte file. The cost of that ordering is that a crash in the
// window between the two loses the conversion, which leaves the original
// stored and unconverted -- the safe direction, and one a later re-run fixes.
func Enqueue(ctx context.Context, db *store.DB, r Request) error {
	if r.FileID == 0 {
		return errors.New("enqueue: a file id is required")
	}
	switch r.Action {
	case ActionSidecar, ActionReplace:
	default:
		return fmt.Errorf("enqueue: %q is not work", r.Action)
	}

	_, err := db.SQL().ExecContext(ctx, `
		INSERT OR IGNORE INTO conversions
			(file_id, device_id, source_path, class, action, state, note, queued_at)
		VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)`,
		r.FileID, r.DeviceID, r.SourcePath, string(r.Class), string(r.Action), r.Note,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("queue a conversion: %w", err)
	}
	return nil
}

// Wake asks the queue to look for work now. It never blocks.
func (q *Queue) Wake() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Run works the queue until ctx is cancelled.
func (q *Queue) Run(ctx context.Context) {
	// A receiver killed mid-conversion leaves rows claimed by a process that
	// no longer exists. Nothing else would ever pick them up.
	if n, err := q.recover(ctx); err != nil && ctx.Err() == nil {
		q.log.Warn("could not requeue interrupted conversions", "error", err)
	} else if n > 0 {
		q.log.Info("requeued conversions interrupted by a restart", "count", n)
	}

	for {
		if _, err := q.ConvertPending(ctx); err != nil && ctx.Err() == nil {
			q.log.Warn("could not convert received files", "error", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-q.wake:
		}
	}
}

// ConvertPending works the queue empty and reports how many rows it finished.
//
// Exported because it is the whole of the worker's behaviour, and a test that
// has to race a goroutine to observe it is a test that will flake.
func (q *Queue) ConvertPending(ctx context.Context) (int, error) {
	g, ctx := errgroup.WithContext(ctx)

	var done counter
	for range q.workers {
		g.Go(func() error {
			for {
				if err := ctx.Err(); err != nil {
					return err
				}

				item, err := q.claim(ctx)
				if errors.Is(err, ErrNotFound) {
					return nil
				}
				if err != nil {
					return err
				}

				if err := q.convert(ctx, item); err != nil {
					return err
				}
				done.add(1)
			}
		})
	}

	err := g.Wait()
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return done.get(), err
	}
	return done.get(), err
}

// claim takes the oldest pending row for this worker.
//
// The UPDATE is the claim: two workers that both read the same row have one
// of them affect zero rows and go round again, so no lock is held across a
// conversion that may run for an hour.
func (q *Queue) claim(ctx context.Context) (Item, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Item{}, err
		}

		item, err := q.scanOne(ctx, `SELECT `+columns+` FROM conversions
			WHERE state = 'pending' ORDER BY queued_at, id LIMIT 1`)
		if err != nil {
			return Item{}, err
		}

		res, err := q.db.SQL().ExecContext(ctx,
			`UPDATE conversions SET state = 'running' WHERE id = ? AND state = 'pending'`,
			item.ID)
		if err != nil {
			return Item{}, fmt.Errorf("claim a conversion: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil && n == 1 {
			item.State = StateRunning
			return item, nil
		}
	}
}

// convert does one row's work.
//
// Every outcome is a state, never an error return: a file the tools refuse
// must show up in the window saying so, and must not stop the queue behind
// it. Only a cancelled context and a broken ledger come back as errors.
func (q *Queue) convert(ctx context.Context, item Item) error {
	source := filepath.Join(q.root, filepath.FromSlash(item.SourcePath))

	info, err := os.Stat(source)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return q.finish(ctx, item, StateFailed, Result{}, "",
			fmt.Sprintf("the received file is no longer there: %v", err))
	}

	result, err := q.converter.Convert(ctx, Job{
		Source:  source,
		Class:   item.Class,
		Dest:    source,
		ModTime: info.ModTime(),
	})
	switch {
	case errors.Is(err, ErrNotNeeded):
		return q.finish(ctx, item, StateSkipped, Result{},
			"already in a format everything can open, so it was left alone", "")
	case errors.Is(err, ErrNoTool):
		// Not a failure of this file. The machine cannot do the job at all,
		// and saying so on every row is how the user finds out why nothing
		// is being converted.
		return q.finish(ctx, item, StateSkipped, Result{}, err.Error(), "")
	case err != nil:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return q.finish(ctx, item, StateFailed, Result{}, "", err.Error())
	}

	note := item.Note
	if item.Action == ActionReplace {
		if err := q.removeOriginal(ctx, item, source); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// The converted file is good; only the deletion failed. Keeping
			// both is the harmless outcome, so it is recorded and not raised.
			note = appendNote(note, fmt.Sprintf("kept the original: it could not be removed (%v)", err))
		}
	}

	return q.finish(ctx, item, StateDone, result, note, "")
}

// removeOriginal deletes the received file and marks the ledger row.
//
// The two happen in that order and the mark is what matters: files.hash still
// describes bytes this machine no longer holds, and a receiver that cannot
// produce those bytes must never authorise a phone to delete its copy.
func (q *Queue) removeOriginal(ctx context.Context, item Item, source string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := q.db.SQL().ExecContext(ctx,
		`UPDATE files SET original_removed_at = ? WHERE id = ?`, now, item.FileID); err != nil {
		return fmt.Errorf("record the removal: %w", err)
	}
	if err := os.Remove(source); err != nil && !os.IsNotExist(err) {
		// Undo the mark: the file is still there, so the receiver can still
		// prove it holds those bytes.
		_, _ = q.db.SQL().ExecContext(context.WithoutCancel(ctx),
			`UPDATE files SET original_removed_at = NULL WHERE id = ?`, item.FileID)
		return err
	}
	return nil
}

func (q *Queue) finish(ctx context.Context, item Item, state string, result Result, note, failure string) error {
	output := ""
	if result.Output != "" {
		rel, err := filepath.Rel(q.root, result.Output)
		if err != nil {
			return fmt.Errorf("locate the converted file: %w", err)
		}
		output = filepath.ToSlash(rel)
	}

	_, err := q.db.SQL().ExecContext(ctx, `
		UPDATE conversions
		SET state = ?, output_path = ?, output_size = ?, tool = ?, note = ?, error = ?,
		    finished_at = ?
		WHERE id = ?`,
		state, output, result.Size, result.Tool, note, failure,
		time.Now().UTC().Format(time.RFC3339Nano), item.ID)
	if err != nil {
		return fmt.Errorf("record a conversion: %w", err)
	}
	return nil
}

// recover returns rows a previous process was working on to the queue.
func (q *Queue) recover(ctx context.Context) (int64, error) {
	res, err := q.db.SQL().ExecContext(ctx,
		`UPDATE conversions SET state = 'pending' WHERE state = 'running'`)
	if err != nil {
		return 0, fmt.Errorf("requeue interrupted conversions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return n, nil
}

// Pending is how many conversions are waiting or running.
func (q *Queue) Pending(ctx context.Context) (int, error) {
	var n int
	err := q.db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM conversions WHERE state IN ('pending', 'running')`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count conversions: %w", err)
	}
	return n, nil
}

// Recent lists the most recently queued conversions, newest first.
func (q *Queue) Recent(ctx context.Context, limit int) ([]Item, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := q.db.SQL().QueryContext(ctx,
		`SELECT `+columns+` FROM conversions ORDER BY queued_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read conversions: %w", err)
	}
	defer rows.Close()

	var out []Item
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

const columns = `id, file_id, device_id, source_path, class, action, state,
	output_path, output_size, tool, note, error, queued_at, finished_at`

func (q *Queue) scanOne(ctx context.Context, query string, args ...any) (Item, error) {
	rows, err := q.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return Item{}, fmt.Errorf("read a conversion: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Item{}, fmt.Errorf("read a conversion: %w", err)
		}
		return Item{}, ErrNotFound
	}
	return scan(rows)
}

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (Item, error) {
	var (
		item       Item
		class      string
		action     string
		queuedAt   string
		finishedAt sql.NullString
	)
	err := row.Scan(&item.ID, &item.FileID, &item.DeviceID, &item.SourcePath,
		&class, &action, &item.State, &item.OutputPath, &item.OutputSize,
		&item.Tool, &item.Note, &item.Error, &queuedAt, &finishedAt)
	if err != nil {
		return Item{}, fmt.Errorf("read a conversion: %w", err)
	}

	item.Class = Class(class)
	item.Action = Action(action)
	if t, err := time.Parse(time.RFC3339Nano, queuedAt); err == nil {
		item.QueuedAt = t
	}
	if finishedAt.Valid {
		if t, err := time.Parse(time.RFC3339Nano, finishedAt.String); err == nil {
			item.FinishedAt = &t
		}
	}
	return item, nil
}

func appendNote(existing, addition string) string {
	if strings.TrimSpace(existing) == "" {
		return addition
	}
	return existing + "; " + addition
}
