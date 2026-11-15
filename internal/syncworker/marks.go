package syncworker

// Reading the log's shape from the heads table: each origin's head, entry
// count, size and the floor it has recorded. The signed-from boundaries and the
// epoch cuts are read from meta beside them.

import (
	"context"
)

// ReadMarks answers with each origin's head, entry count and size, and the
// floor it has recorded. The heads are read as recorded rather than recomputed:
// a caller recounting from entries it does not hold would floor an origin at a
// sequence the log never reached. The replica is read as it stands, without a
// transfer first.
func (b *TursoBackend) ReadMarks() (MarkState, error) {
	if err := b.ready(); err != nil {
		return MarkState{}, err
	}
	ctx := context.Background()
	var out MarkState
	if err := b.readHeads(ctx, &out); err != nil {
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

// readHeads reads each origin's head, entry count, size and recorded floor in
// one query rather than several: a second pass would rescan the table. The maps
// are created on the first row, so a replica with no heads rows answers with nil
// maps, and an origin whose floor is zero is absent from Floors.
func (b *TursoBackend) readHeads(ctx context.Context, out *MarkState) error {
	rows, err := b.db.QueryContext(ctx,
		"SELECT origin, seq, entries, bytes, floor FROM heads")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var origin string
		var head, entries, floor int
		var size int64
		if err := rows.Scan(&origin, &head, &entries, &size, &floor); err != nil {
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
		if floor > 0 {
			if out.Floors == nil {
				out.Floors = make(map[string]int)
			}
			out.Floors[origin] = floor
		}
	}
	return rows.Err()
}

// readMarks answers with the log's floors, heads, entry counts and sizes.
func (s *server) readMarks(req Request) Response {
	state, err := s.backend.ReadMarks()
	if err != nil {
		return refuse(req, err)
	}
	return Response{ID: req.ID, OK: true, Marks: &state}
}
