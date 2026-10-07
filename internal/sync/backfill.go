package sync

// The change-set backfill: the rows a machine wrote before capture was turned on
// are in its database and in no change set, so no push can carry them and every
// push still reports success. This walks the file's own tables once at the moment
// it becomes a member and puts those rows into the change set.
//
// The walk is where the driver's own capture surface does the work. The driver
// offers no backfill call -- its Go bindings and the library it links export no
// capture, cdc or backfill symbol at all -- and the only control it has is the
// per-connection capture pragma. So the pass rewrites each row as itself over a
// connection carrying that pragma, and the engine records the rewrite itself:
// the change id, the change type and the payload are the engine's, not rows this
// package wrote into turso_cdc by hand.
//
// A rewrite of an identical row is idempotent against the remote, so the walk is
// resumable from a stored offset without a double-count risk: at worst a batch
// runs twice and the remote ends up holding the same row twice over.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// KeyBackfill is the marker recording how far the backfill got. It lives in the
// machine-local file's `sync` namespace, so it never leaves the machine and never
// rides the change set it is describing.
const KeyBackfill = "sync.backfill"

// The bounds one backfill puts on itself. The batch is how many rows one
// transaction records; the budget is how many rows the whole pass will record in
// one run. Both exist so a large database cannot turn the moment a file becomes a
// member into an unbounded write: what the budget leaves undone is what the next
// attempt's marker resumes from.
const (
	// BackfillBatch is how many rows one backfill transaction records.
	BackfillBatch = 256
	// BackfillBudget is how many rows one enable's backfill records before it
	// stops and leaves the rest to the next attempt.
	BackfillBudget = 4096
	// BackfillTimeout bounds the whole pass, so an enable spends a bounded time
	// on it whatever the database holds.
	BackfillTimeout = 30 * time.Second
)

// backfillProgress is where the walk got to, which is the whole of the marker.
type backfillProgress struct {
	// At is the rowid the next batch starts at, -1 for a table not started.
	At int64 `json:"at"`
	// Done is whether the table was walked to its end.
	Done bool `json:"done"`
}

// backfillMarker is the marker's whole body: the tables walked, how far each got
// and whether the whole file is done.
type backfillMarker struct {
	// Tables is the progress per table name.
	Tables map[string]backfillProgress `json:"tables"`
	// Rows is how many rows the pass has recorded so far, over every attempt.
	Rows int64 `json:"rows"`
	// Complete says the walk reached the end of every table. It is kept rather
	// than the marker being deleted, because "this file has been walked" is the
	// fact a later attempt needs: a walk that starts again over a finished file
	// re-records every row the first one recorded.
	Complete bool `json:"complete"`
}

// ReadBackfill reads the marker, and reports whether one was there at all.
func ReadBackfill(kv db.KV) (backfillMarker, bool, error) {
	body, ok, err := kv.KVGet(KeyBackfill)
	if err != nil {
		return backfillMarker{}, false, fmt.Errorf("sync: marker %s: %w", KeyBackfill, err)
	}
	if !ok {
		return backfillMarker{}, false, nil
	}
	var marker backfillMarker
	if err := json.Unmarshal(body, &marker); err != nil {
		// A marker this build cannot read is a marker it cannot resume from, so
		// it is treated as absent and the walk starts again rather than refusing.
		return backfillMarker{}, false, nil
	}
	if marker.Tables == nil {
		marker.Tables = map[string]backfillProgress{}
	}
	return marker, true, nil
}

// WriteBackfill stores the marker after a batch has committed, so a crash costs
// at most the batch in flight and never a row that was already recorded.
func WriteBackfill(kv db.KV, marker backfillMarker) error {
	body, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("sync: encode the %s marker: %w", KeyBackfill, err)
	}
	if err := kv.KVPut(KeyBackfill, body); err != nil {
		return fmt.Errorf("sync: write the %s marker: %w", KeyBackfill, err)
	}
	return nil
}

// BackfillResult is what one pass put into the change set.
type BackfillResult struct {
	// Rows is how many rows this pass recorded, not the marker's running total.
	Rows int64
	// Tables is how many tables this pass walked to the end.
	Tables int
	// Exhausted is whether the budget ran out with tables still to walk, which is
	// the case that wants another attempt rather than a clear marker.
	Exhausted bool
}

// Backfill records the rows this machine wrote before capture was on into the
// change set, one bounded batch at a time, resuming from the marker.
//
// It is called at the moment the file becomes a member: after the seed decision,
// before the first push. A row recorded here is carried by the push that follows
// it, which is the difference between a row that reaches the remote and a row
// that sits in the database while every push reports success.
func Backfill(ctx context.Context, kv db.KV, path string) (BackfillResult, error) {
	var out BackfillResult
	if kv == nil || path == "" {
		return out, nil
	}
	marker, _, err := ReadBackfill(kv)
	if err != nil {
		return out, err
	}
	if marker.Tables == nil {
		marker.Tables = map[string]backfillProgress{}
	}
	if marker.Complete {
		slog.Info("sync: this file's rows were already recorded into the change set, so there is nothing left to backfill",
			"path", path, "rows", marker.Rows)
		return out, nil
	}

	ctx, cancel := context.WithTimeout(ctx, BackfillTimeout)
	defer cancel()

	capture, err := db.OpenCapture(ctx, path, captureBusyMS)
	if err != nil {
		// A busy database is reported as itself rather than folded into a generic
		// failure: the pass wrote nothing, a retry answers it, and the sentinel is
		// what lets a caller classify it without reading the message.
		return out, fmt.Errorf("sync: backfill %s: %w", path, err)
	}
	defer func() { _ = capture.Close() }()

	tables, err := capture.Tables(ctx)
	if err != nil {
		return out, fmt.Errorf("sync: backfill %s: %w", path, err)
	}
	for _, table := range tables {
		if err := ctx.Err(); err != nil {
			return out, fmt.Errorf("sync: backfill %s: %w", path, err)
		}
		rows, done, err := backfillTable(ctx, kv, capture, &marker, table, BackfillBudget-out.Rows)
		if err != nil {
			return out, err
		}
		out.Rows += rows
		if done {
			out.Tables++
			continue
		}
		out.Exhausted = true
		slog.Info("sync: the change-set backfill stopped on its budget; the rest is left to the next attempt",
			"path", path, "rows", out.Rows, "table", table, "budget", BackfillBudget)
		return out, WriteBackfill(kv, marker)
	}
	marker.Complete = true
	if err := WriteBackfill(kv, marker); err != nil {
		return out, err
	}
	slog.Info("sync: the change-set backfill recorded this machine's pre-capture rows",
		"path", path, "rows", marker.Rows, "tables", len(tables))
	return out, nil
}

// backfillTable walks one table's rows into the change set, one batch at a time,
// and reports how many it recorded and whether it reached the end.
func backfillTable(ctx context.Context, kv db.KV, capture *db.CaptureConn, marker *backfillMarker,
	table string, budget int64) (int64, bool, error) {
	progress := marker.Tables[table]
	if progress.Done {
		return 0, true, nil
	}
	var rows int64
	for {
		if budget <= 0 {
			marker.Tables[table] = backfillProgress{At: progress.At, Done: false}
			return rows, false, nil
		}
		batch := int64(BackfillBatch)
		if budget < batch {
			batch = budget
		}
		res, err := capture.Backfill(ctx, table, progress.At, int(batch))
		if err != nil {
			return rows, false, fmt.Errorf("sync: backfill %s: %w", table, err)
		}
		rows += res.Rows
		marker.Rows += res.Rows
		progress.At = res.NextRowID
		if res.Rows < batch {
			progress.Done = true
			marker.Tables[table] = progress
			return rows, true, WriteBackfill(kv, *marker)
		}
		budget -= res.Rows
		// The marker is written per batch rather than per table so a crash costs
		// one repeated batch, which the remote absorbs as the same row twice.
		if err := WriteBackfill(kv, *marker); err != nil {
			return rows, false, err
		}
	}
}

// captureBusyMS is the busy timeout the capture connection opens with, so a
// contended write slot costs one bounded wait rather than an open that never
// returns.
const captureBusyMS = 5000
