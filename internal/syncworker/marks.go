package syncworker

// Reading the log's shape: the floors the remote has recorded, and each
// origin's head, entry count and size. The marks table is created but nothing
// writes or reads it; it stays so a format-5 remote and this one agree on the
// schema.

import (
	"context"
	"strconv"
)

// createMarksStatement is the marks table: one row per reader per origin. It is
// created so a format-5 remote and a new one agree, and nothing writes or reads
// it.
const createMarksStatement = "CREATE TABLE marks (" +
	"reader TEXT NOT NULL, origin TEXT NOT NULL, seq INTEGER NOT NULL, " +
	"at TEXT NOT NULL, " +
	"PRIMARY KEY (reader, origin))"

// floorPrefix is what a compaction floor's meta key begins with, so a read of
// the floors finds them without a second table.
const floorPrefix = "floor."

// ReadMarks answers with every floor recorded so far and each origin's head,
// entry count and size. The heads are read here rather than recomputed: a
// caller recounting from a log it had not pulled would floor an origin at a
// sequence the log never reached. The replica is not pulled first: a read of
// the marks reads what this replica already holds.
func (b *TursoBackend) ReadMarks() (MarkState, error) {
	if err := b.ready(); err != nil {
		return MarkState{}, err
	}
	ctx := context.Background()
	var out MarkState
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

// readFloors reads the floor a compaction recorded for each origin. The meta
// rows are the remote's own record of what it has dropped, and a caller that
// recomputed one could floor an origin below what the remote discarded.
func (b *TursoBackend) readFloors(ctx context.Context, out *MarkState) error {
	return b.readMetaPositions(ctx, floorPrefix, &out.Floors)
}

// readMarks answers with the log's floors, heads, entry counts and sizes.
func (s *server) readMarks(req Request) Response {
	state, err := s.backend.ReadMarks()
	if err != nil {
		return refuse(req, err)
	}
	return Response{ID: req.ID, OK: true, Marks: &state}
}

// readSizes reads each origin's highest sequence, how many entries it holds and
// its size, in one query rather than two: a second pass would rescan the table.
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
