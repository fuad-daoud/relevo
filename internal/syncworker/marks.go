package syncworker

// The marks table is the log's one piece of state written by readers rather
// than by the origin that owns the entries: each machine publishes how far it
// has applied of every other origin, and nothing else writes those rows.

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

// createMarksStatement is the marks table: one row per reader per origin, the
// sequence that reader has applied of that origin's log.
//
// The key is the pair rather than the origin alone because every machine
// publishes its own position and none may overwrite another's, and `at` is a
// column rather than something derived from a row because a mark's age is what
// decides whether its reader still counts towards a floor.
const createMarksStatement = "CREATE TABLE marks (" +
	"reader TEXT NOT NULL, origin TEXT NOT NULL, seq INTEGER NOT NULL, " +
	"at TEXT NOT NULL, " +
	"PRIMARY KEY (reader, origin))"

// floorPrefix is what a compaction floor's meta key begins with, so a read of
// the floors finds them without a second table.
const floorPrefix = "floor."

// PublishMarks writes this reader's own rows and pushes them.
//
// The reader is this worker's own origin rather than anything the request
// names, so a caller on this machine cannot move another reader's position: a
// mark is what a floor is computed from, and a caller that could write another
// reader's would be choosing where the log drops what. An origin's own mark is
// not written, because a reader never needs one and the floor ignores it.
//
// A mark only grows, so the write never moves a row backwards. A caller that
// published a lower mark than the row holds is repeating an older fact rather
// than reporting a rollback, and taking it would let a stale publish raise a
// floor above entries a live reader still needs.
func (b *TursoBackend) PublishMarks(marks map[string]int, at time.Time) error {
	if err := b.ready(); err != nil {
		return err
	}
	if len(marks) == 0 {
		return nil
	}
	ctx := context.Background()
	if err := b.writeMarks(ctx, marks, at); err != nil {
		return fmt.Errorf("syncworker: publish marks: %w", markDriverRefusal(err))
	}
	if err := b.transfer(ctx, b.driver.Push); err != nil {
		return fmt.Errorf("syncworker: push marks: %w", markTransferRefusal(err))
	}
	return nil
}

// writeMarks records one row per origin in a single transaction, so a reader's
// own set is all published or none is. A publish is not partial: a reader whose
// half its positions landed would leave the log holding marks that no machine
// published and none would retract.
func (b *TursoBackend) writeMarks(ctx context.Context, marks map[string]int, at time.Time) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for origin, seq := range marks {
		if origin == "" {
			return ErrProtocol
		}
		if err := writeMark(ctx, tx, b.spec.Origin, origin, seq, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// writeMark moves one reader's position for one origin forward, inserting the
// row when there is none.
//
// An update then an insert, rather than one upsert, for the same reason the head
// row takes that shape: on a sync connection the engine can refuse an upsert
// that lands on an existing row as a corrupt record, and a mark is rewritten on
// every publish after the first.
func writeMark(ctx context.Context, tx *sql.Tx, reader, origin string, seq int, at time.Time) error {
	res, err := tx.ExecContext(ctx,
		"UPDATE marks SET seq = ?, at = ? WHERE reader = ? AND origin = ?",
		seq, at.UTC().Format(time.RFC3339Nano), reader, origin)
	if err != nil {
		return fmt.Errorf("syncworker: mark %s at %s: %w", reader, origin, err)
	}
	moved, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("syncworker: mark %s at %s: %w", reader, origin, err)
	}
	if moved > 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO marks (reader, origin, seq, at) VALUES (?, ?, ?, ?)",
		reader, origin, seq, at.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("syncworker: mark %s at %s: %w", reader, origin, err)
	}
	return nil
}

// ReadMarks answers with every reader's published position, the floors recorded
// so far, and each origin's head and entry count.
//
// The counts are what a compaction's trigger is measured against and the heads
// are what a floor is clamped to, so they are read here rather than left to the
// caller to derive: a caller that recomputed a head from a log it had not
// pulled would floor an origin at a sequence the log never reached.
//
// The replica is not pulled first. A read of marks is a read of what this
// replica already holds, and a caller that wanted the remote's latest marks
// asks for it over the exchange the marks are published along.
func (b *TursoBackend) ReadMarks() (MarkState, error) {
	if err := b.ready(); err != nil {
		return MarkState{}, err
	}
	ctx := context.Background()
	var out MarkState
	if err := b.readMarkRows(ctx, &out); err != nil {
		return MarkState{}, markDriverRefusal(err)
	}
	if err := b.readFloors(ctx, &out); err != nil {
		return MarkState{}, markDriverRefusal(err)
	}
	if err := b.readSizes(ctx, &out); err != nil {
		return MarkState{}, markDriverRefusal(err)
	}
	if err := b.readSignedFrom(ctx, &out); err != nil {
		return MarkState{}, markDriverRefusal(err)
	}
	if err := b.readEpochCuts(ctx, &out); err != nil {
		return MarkState{}, markDriverRefusal(err)
	}
	return out, nil
}

// readMarkRows reads every published mark, in reader then origin order so a
// reader walking the answer sees the same row every time.
func (b *TursoBackend) readMarkRows(ctx context.Context, out *MarkState) error {
	rows, err := b.db.QueryContext(ctx,
		"SELECT reader, origin, seq, at FROM marks ORDER BY reader, origin")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	// Every column is read as text and a row that does not parse is skipped:
	// the table is written by other machines, and one malformed row must not
	// stop every reader from reading the rest.
	for rows.Next() {
		var reader, origin, seq, at sql.NullString
		if err := rows.Scan(&reader, &origin, &seq, &at); err != nil {
			return err
		}
		m, ok := parseMark(reader.String, origin.String, seq.String, at.String)
		if ok {
			out.Marks = append(out.Marks, m)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

// readFloors reads the floor a compaction recorded for each origin.
//
// Nothing writes them yet, so the map comes back empty until compaction runs;
// it is read here because the meta rows are the remote's own record of what it
// has already dropped, and a caller that recomputed one would floor an origin
// below what the remote has in fact discarded.
func (b *TursoBackend) readFloors(ctx context.Context, out *MarkState) error {
	return b.readMetaPositions(ctx, floorPrefix, &out.Floors)
}

// publishMarks records this worker's own applied positions.
//
// The reader is taken from hello rather than from the request, so the reply is
// empty whatever the request named: a caller that could publish another
// reader's mark would be choosing what the log may drop, on a machine that
// does not hold the entries those marks speak for.
func (s *server) publishMarks(req Request) Response {
	if req.At == nil {
		return refuse(req, fmt.Errorf("%w: publish_marks with no time", ErrProtocol))
	}
	marks := make(map[string]int, len(req.Publish))
	for _, m := range req.Publish {
		if m.Origin == "" {
			return refuse(req, fmt.Errorf("%w: a mark with no origin", ErrProtocol))
		}
		if m.Origin == s.spec.Origin {
			return refuse(req, fmt.Errorf("%w: a mark of this reader's own origin", ErrProtocol))
		}
		marks[m.Origin] = m.Seq
	}
	if err := s.backend.PublishMarks(marks, *req.At); err != nil {
		return refuse(req, err)
	}
	return Response{ID: req.ID, OK: true}
}

// readMarks answers with every published mark and the compaction inputs beside
// them.
func (s *server) readMarks(req Request) Response {
	state, err := s.backend.ReadMarks()
	if err != nil {
		return refuse(req, err)
	}
	return Response{ID: req.ID, OK: true, Marks: &state}
}

// readSizes reads each origin's highest sequence and how many entries it holds.
//
// One query rather than two: the two are read from the same rows, and a second
// pass over the log would be a second scan of the table compaction is about to
// make smaller.
func (b *TursoBackend) readSizes(ctx context.Context, out *MarkState) error {
	rows, err := b.db.QueryContext(ctx,
		"SELECT origin, COALESCE(MAX(seq), 0), COUNT(*), "+
			"COALESCE(SUM(LENGTH(body)), 0) + "+strconv.Itoa(logRowOverhead)+"*COUNT(*) FROM log "+
			"WHERE typeof(seq) = 'integer' GROUP BY origin")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var origin string
		var head, entries int
		var size int64
		if err := rows.Scan(&origin, &head, &entries, &size); err != nil {
			return err
		}
		if out.Heads == nil {
			out.Heads = make(map[string]int)
			out.Entries = make(map[string]int)
			out.Sizes = make(map[string]int64)
		}
		out.Heads[origin] = head
		out.Entries[origin] = entries
		out.Sizes[origin] = size
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

// parseMark reads one marks row, and reports false for a row that is not a
// mark: an empty reader or origin, a sequence that is not a non-negative
// number, or a time that is not RFC 3339.
func parseMark(reader, origin, seq, at string) (PublishedMark, bool) {
	n, err := strconv.Atoi(seq)
	if err != nil || n < 0 || reader == "" || origin == "" {
		return PublishedMark{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return PublishedMark{}, false
	}
	return PublishedMark{Reader: reader, Origin: origin, Seq: n, At: parsed}, true
}
