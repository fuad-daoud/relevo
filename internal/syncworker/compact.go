package syncworker

// Compaction: an origin drops its own superseded log entries and records the
// floor it dropped up to, in one transaction and one push, so no reader ever sees
// a hole under an old floor.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// compactStatement drops an entry at or below the floor that a later entry for
// the same row, also at or below the floor, supersedes. The later entry must be
// bounded by the floor too: a row whose only newer entry sits above it keeps its
// older one, so a reader applying up to the floor always meets a row's parent.
//
// The subquery is a range on the primary key (origin, seq) per candidate row,
// not a scan of the table.
const compactStatement = "DELETE FROM log WHERE origin = ? AND seq <= ? AND EXISTS (" +
	"SELECT 1 FROM log AS later WHERE later.origin = log.origin AND later.seq > log.seq " +
	"AND later.seq <= ? AND later.tbl = log.tbl AND later.pk = log.pk)"

// Compact drops this worker's own superseded entries up to floor.
//
// The floor has to be a position a reader's mark can have reached: no past the
// log's head and never inside a batch, because a reader applies whole batches and
// a floor inside one would leave it a batch with a hole. The recorded floor is
// the larger of the old and the new, so it only rises.
func (b *TursoBackend) Compact(floor int) (CompactResult, error) {
	if err := b.ready(); err != nil {
		return CompactResult{}, err
	}
	ctx := context.Background()
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return CompactResult{}, fmt.Errorf("syncworker: compact: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := b.checkFloor(ctx, tx, floor); err != nil {
		return CompactResult{}, err
	}
	res, err := tx.ExecContext(ctx, compactStatement, b.spec.Origin, floor, floor)
	if err != nil {
		return CompactResult{}, fmt.Errorf("syncworker: compact: %w", err)
	}
	dropped, err := res.RowsAffected()
	if err != nil {
		return CompactResult{}, fmt.Errorf("syncworker: compact: %w", err)
	}
	written, err := b.writeFloor(ctx, tx, floor)
	if err != nil {
		return CompactResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CompactResult{}, fmt.Errorf("syncworker: compact: %w", err)
	}
	if err := b.transfer(ctx, b.driver.Push); err != nil {
		return CompactResult{}, fmt.Errorf("syncworker: push the compaction: %w", markDriverRefusal(err))
	}
	return CompactResult{Dropped: int(dropped), Floor: written}, nil
}

// checkFloor refuses a floor no reader's mark can have reached.
func (b *TursoBackend) checkFloor(ctx context.Context, tx *sql.Tx, floor int) error {
	var head sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		"SELECT MAX(seq) FROM log WHERE origin = ?", b.spec.Origin).Scan(&head); err != nil {
		return fmt.Errorf("syncworker: compact: read the head: %w", err)
	}
	if floor < 0 || int64(floor) > head.Int64 {
		return fmt.Errorf("%w: a floor of %d past the log's head %d", ErrProtocol, floor, head.Int64)
	}
	var inside int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM log WHERE origin = ? AND batch <= ? AND seq > ?",
		b.spec.Origin, floor, floor).Scan(&inside); err != nil {
		return fmt.Errorf("syncworker: compact: read the batches: %w", err)
	}
	if inside > 0 {
		return fmt.Errorf("%w: a floor of %d inside a batch", ErrProtocol, floor)
	}
	return nil
}

// writeFloor records max(old floor, floor) for this origin and returns it.
//
// An update then an insert, as writeMark does, for the same engine reason.
func (b *TursoBackend) writeFloor(ctx context.Context, tx *sql.Tx, floor int) (int, error) {
	key := floorPrefix + b.spec.Origin
	var old string
	err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", key).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("syncworker: compact: read the floor: %w", err)
	}
	if err == nil {
		prior, perr := strconv.Atoi(old)
		if perr != nil {
			return 0, fmt.Errorf("syncworker: compact: the recorded floor %q: %w", old, perr)
		}
		floor = max(floor, prior)
		if _, err := tx.ExecContext(ctx, "UPDATE meta SET value = ? WHERE key = ?", strconv.Itoa(floor), key); err != nil {
			return 0, fmt.Errorf("syncworker: compact: write the floor: %w", err)
		}
		return floor, nil
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO meta (key, value) VALUES (?, ?)", key, strconv.Itoa(floor)); err != nil {
		return 0, fmt.Errorf("syncworker: compact: write the floor: %w", err)
	}
	return floor, nil
}

// PullRange reads one page of an origin's entries in (after, upto].
func (b *TursoBackend) PullRange(origin string, after, upto int) ([]Entry, error) {
	if err := b.ready(); err != nil {
		return nil, err
	}
	return b.scanEntries(context.Background(),
		"SELECT origin, seq, batch, tbl, pk, op, schema_version, body, at FROM log"+
			" WHERE origin = ? AND seq > ? AND seq <= ? ORDER BY seq LIMIT ?",
		[]any{origin, after, upto, b.page})
}

// pullRange answers with one page of an origin's entries between two sequences.
func (s *server) pullRange(req Request) Response {
	if req.Origin == "" || req.AfterSeq < 0 || req.Upto <= req.AfterSeq {
		return refuse(req, fmt.Errorf("%w: pull_range of %q over (%d, %d]", ErrProtocol, req.Origin, req.AfterSeq, req.Upto))
	}
	entries, err := s.backend.PullRange(req.Origin, req.AfterSeq, req.Upto)
	if err != nil {
		return refuse(req, err)
	}
	return Response{ID: req.ID, OK: true, Entries: entries}
}

// compact drops this worker's own entries. The origin is the worker's own from
// hello and never a field of the request, for the reason publish_marks has: only
// the machine that wrote a log may drop from it.
func (s *server) compact(req Request) Response {
	res, err := s.backend.Compact(req.Floor)
	if err != nil {
		return refuse(req, err)
	}
	return Response{ID: req.ID, OK: true, Dropped: res.Dropped, Floor: res.Floor}
}
