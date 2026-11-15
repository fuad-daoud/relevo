package syncworker

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

const tableShared = "shared"

// errHeadMoved reports that an origin's heads row moved twice during one export,
// meaning concurrent writers keep racing it.
var errHeadMoved = errors.New("origin head moved twice during export")

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// readHead reads the current sequence for origin from heads. A missing row is
// not an error: an origin that has not yet exported starts at 0.
func readHead(ctx context.Context, q rowQuerier, origin string) (seq int, have bool, err error) {
	err = q.QueryRowContext(ctx, "SELECT seq FROM heads WHERE origin = ?", origin).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("syncworker: read head: %w", err)
	}
	return seq, true, nil
}

// Append writes one batch of this origin's entries as a single replica
// transaction and pushes it.
func (b *TursoBackend) Append(entries []Entry) ([]Entry, error) {
	return b.AppendSigned(entries, Boundary{})
}

// AppendSigned writes one batch of this origin's entries as a segment, moving
// heads in the same replica transaction, and pushes it.
func (b *TursoBackend) AppendSigned(entries []Entry, boundary Boundary) ([]Entry, error) {
	if err := b.ready(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return []Entry{}, nil
	}
	for _, e := range entries {
		if e.Origin != b.spec.Origin {
			return nil, fmt.Errorf("syncworker: append %s %s as %s: %w", e.Tbl, e.PK, e.Origin, ErrForeignOrigin)
		}
	}
	if b.blobs == nil {
		return nil, ErrNoR2
	}
	if len(entries) > SegmentMaxEntries {
		return nil, fmt.Errorf("%w: %d entries, over the %d limit", ErrProtocol, len(entries), SegmentMaxEntries)
	}
	ctx := context.Background()
	written, err := b.appendPasses(ctx, entries, boundary)
	if err != nil {
		return nil, err
	}
	// The push comes before the reply: the daemon clears the outbox only once
	// it has the numbered entries back, and a reply sent before the push would
	// let it drop changes the log never received.
	if err := b.transfer(ctx, b.driver.Push); err != nil {
		return nil, fmt.Errorf("syncworker: push: %w", markTransferRefusal(err))
	}
	return written, nil
}

func (b *TursoBackend) appendPasses(ctx context.Context, entries []Entry, boundary Boundary) ([]Entry, error) {
	lostCount := 0
	for {
		headSeq, have, err := readHead(ctx, b.db, b.spec.Origin)
		if err != nil {
			return nil, err
		}
		numbered, err := numberEntries(b.spec.Origin, headSeq, entries)
		if err != nil {
			return nil, err
		}
		data, header, err := EncodeSegment(b.spec.Origin, numbered)
		if err != nil {
			return nil, err
		}
		key := blobstore.SegmentKey(b.spec.Origin, headSeq+1)
		created, err := b.blobs.PutIfAbsent(ctx, key, bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, fmt.Errorf("syncworker: put segment %s: %w", key, err)
		}
		if created {
			b.bytes.R2Put += int64(len(data))
			moved, err := b.commitSegment(ctx, headSeq, have, key, header, numbered, len(data), boundary)
			if err != nil {
				return nil, err
			}
			if moved {
				return numbered, nil
			}
			if err := b.casLost(ctx, &lostCount); err != nil {
				return nil, err
			}
			continue
		}
		moved, err := b.adoptOrphan(ctx, key, headSeq, have, boundary)
		if err != nil {
			return nil, err
		}
		if moved {
			continue
		}
		if err := b.casLost(ctx, &lostCount); err != nil {
			return nil, err
		}
	}
}

func numberEntries(origin string, headSeq int, entries []Entry) ([]Entry, error) {
	numbered := make([]Entry, len(entries))
	for i, e := range entries {
		e.Origin = origin
		e.Seq = headSeq + 1 + i
		e.Batch = headSeq + 1
		if e.Op == opUpsert {
			if _, err := bodyHash(e.Body); err != nil {
				return nil, fmt.Errorf("syncworker: hash %s %s: %w", e.Tbl, e.PK, err)
			}
		}
		numbered[i] = e
	}
	return numbered, nil
}

func (b *TursoBackend) adoptOrphan(ctx context.Context, key string, headSeq int, have bool, boundary Boundary) (bool, error) {
	var buf bytes.Buffer
	got, err := b.blobs.Get(ctx, key, &buf)
	if err != nil {
		return false, blobFetchError(key, err)
	}
	b.bytes.R2Get += got
	orphanHeader, orphanEntries, err := DecodeSegment(key, buf.Bytes())
	if err != nil {
		return false, MarkRefusal(CodeRemote, err)
	}
	return b.commitSegment(ctx, headSeq, have, key, orphanHeader, orphanEntries, buf.Len(), boundary)
}

func (b *TursoBackend) casLost(ctx context.Context, lostCount *int) error {
	*lostCount++
	if *lostCount > 1 {
		return errHeadMoved
	}
	if err := b.transfer(ctx, b.driver.Pull); err != nil {
		return fmt.Errorf("syncworker: pull: %w", markTransferRefusal(err))
	}
	return nil
}

func (b *TursoBackend) commitSegment(ctx context.Context, head int, have bool, key string, header SegmentHeader, entries []Entry, size int, boundary Boundary) (bool, error) {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("syncworker: append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if have {
		res, err := tx.ExecContext(ctx,
			"UPDATE heads SET seq = ?, key = ?, entries = entries + ?, bytes = bytes + ?, at = ? WHERE origin = ? AND seq = ?",
			header.Last, key, header.Count, size, now, b.spec.Origin, head)
		if err != nil {
			return false, fmt.Errorf("syncworker: update heads: %w", err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("syncworker: heads rows affected: %w", err)
		}
		if rows == 0 {
			return false, nil
		}
	} else {
		res, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO heads (origin, seq, key, entries, bytes, at) VALUES (?, ?, ?, ?, ?, ?)",
			b.spec.Origin, header.Last, key, header.Count, size, now)
		if err != nil {
			return false, fmt.Errorf("syncworker: insert heads: %w", err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("syncworker: heads rows affected: %w", err)
		}
		if rows == 0 {
			return false, nil
		}
	}

	mirror := make(map[string]struct{}, len(b.spec.MirrorTables))
	for _, tbl := range b.spec.MirrorTables {
		mirror[tbl] = struct{}{}
	}

	for _, e := range entries {
		if _, ok := mirror[e.Tbl]; ok {
			// The shared row is written in the same transaction as the heads
			// move, so the batch lands in both or neither.
			if err := insertRow(ctx, tx, tableShared, e); err != nil {
				return false, err
			}
		}
	}

	if err := writeSignedFrom(ctx, tx, b.spec.Origin, entries, boundary); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("syncworker: append: %w", err)
	}
	return true, nil
}

// insertRow writes one entry's row into tbl. A delete carries no body, so it
// stores NULL rather than an empty string that would read as a body; an
// unsigned entry's MAC stores NULL the same way, which is not a MAC that failed
// to verify.
func insertRow(ctx context.Context, tx *sql.Tx, tbl string, e Entry) error {
	var body any
	if e.Op == opUpsert {
		body = string(e.Body)
	}
	var mac any
	if len(e.MAC) > 0 {
		mac = e.MAC
	}
	var afterOrigin any
	if e.AfterOrigin != "" {
		afterOrigin = e.AfterOrigin
	}
	var afterSeq any
	if e.AfterSeq > 0 {
		afterSeq = e.AfterSeq
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO "+tbl+" (origin, seq, batch, tbl, pk, op, schema_version, body, at, mac, after_origin, after_seq)"+
			" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		e.Origin, e.Seq, e.Batch, e.Tbl, e.PK, e.Op, e.SchemaVersion, body,
		e.At.UTC().Format(time.RFC3339Nano), mac, afterOrigin, afterSeq); err != nil {
		return fmt.Errorf("syncworker: append %s %s: %w", e.Tbl, e.PK, err)
	}
	return nil
}
