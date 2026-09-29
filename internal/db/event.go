package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Events returns bindingID's events, restricted to round when it is non-zero.
func (d *DB) Events(bindingID string, round int) ([]Event, error) {
	return listEvents(context.Background(), d.sqlDB, bindingID, round)
}

func (t *Tx) Events(bindingID string, round int) ([]Event, error) {
	return listEvents(t.ctx, t.conn, bindingID, round)
}

func listEvents(ctx context.Context, q queryer, bindingID string, round int) ([]Event, error) {
	query := `SELECT id, binding_id, round_id, seq, ts, kind, direction, note, path, delivered_at,
			confirmed, late, flagged, flagged_by, entry_json
		FROM event WHERE binding_id = ?`
	args := []any{bindingID}
	if round != 0 {
		query += ` AND round_id IN (SELECT id FROM round WHERE binding_id = ? AND number = ?)`
		args = append(args, bindingID, round)
	}
	query += ` ORDER BY seq ASC`

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: events: %w", err)
	}
	out, err := collectRows(rows, scanEvent)
	if err != nil {
		return nil, fmt.Errorf("db: events: %w", err)
	}
	return out, nil
}

func scanEvent(s rowScanner) (Event, error) {
	var e Event
	var roundID sql.Null[string]
	var ts string
	var note, path, deliveredAt sql.Null[string]
	var confirmed, late int64
	var flagged sql.Null[int64]
	var flaggedBy sql.Null[string]

	if err := s.Scan(&e.ID, &e.BindingID, &roundID, &e.Seq, &ts, &e.Kind, &e.Direction, &note, &path, &deliveredAt,
		&confirmed, &late, &flagged, &flaggedBy, &e.EntryJSON); err != nil {
		return Event{}, fmt.Errorf("scan: %w", err)
	}

	t, err := parseTime(ts)
	if err != nil {
		return Event{}, fmt.Errorf("parse ts: %w", err)
	}
	e.TS = t
	e.RoundID = ptrIfValid(roundID)
	e.Note = ptrIfValid(note)
	e.Path = ptrIfValid(path)
	if deliveredAt.Valid {
		dt, err := parseTime(deliveredAt.V)
		if err != nil {
			return Event{}, fmt.Errorf("parse delivered_at: %w", err)
		}
		e.DeliveredAt = &dt
	}
	e.Confirmed = confirmed != 0
	e.Late = late != 0
	e.Flagged = intPtr(flagged)
	e.FlaggedBy = ptrIfValid(flaggedBy)

	return e, nil
}

// RecentEvents returns events since `since`, across all bindings, newest
// first; limit <= 0 means no limit.
func (d *DB) RecentEvents(since time.Time, limit int) ([]EventLogRow, error) {
	q := `SELECT event.ts, event.seq, event.kind, event.note, event.entry_json,
			binding.name, event.round_id,
			round.number, round.started_at, round.closed_at,
			round.in_tokens, round.cache_tokens, round.write_tokens, round.out_tokens
		FROM event
		JOIN binding ON event.binding_id = binding.id
		LEFT JOIN round ON event.round_id = round.id
		WHERE event.ts >= ?
		ORDER BY event.ts DESC, event.seq DESC`
	args := []any{formatTime(since)}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := d.sqlDB.QueryContext(context.Background(), q, args...)
	if isMissingTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: recent events: %w", mapBusy(err))
	}
	out, err := collectRows(rows, scanEventLogRow)
	if err != nil {
		return nil, fmt.Errorf("db: recent events: %w", mapBusy(err))
	}
	return out, nil
}

func scanEventLogRow(s rowScanner) (EventLogRow, error) {
	var r EventLogRow
	var tsStr string
	var note, roundID sql.Null[string]
	var roundNum sql.Null[int64]
	var startedAt, closedAt sql.Null[string]
	var inTokens, cacheTokens, writeTokens, outTokens sql.Null[int64]

	if err := s.Scan(&tsStr, &r.Seq, &r.Kind, &note, &r.EntryJSON,
		&r.BindingName, &roundID,
		&roundNum, &startedAt, &closedAt,
		&inTokens, &cacheTokens, &writeTokens, &outTokens); err != nil {
		return EventLogRow{}, err
	}

	parsedTS, err := parseTime(tsStr)
	if err != nil {
		return EventLogRow{}, err
	}
	r.TS = parsedTS
	r.Note = ptrIfValid(note)
	r.RoundID = ptrIfValid(roundID)
	r.Round = intPtr(roundNum)
	if closedAt.Valid && startedAt.Valid {
		st, err := parseTime(startedAt.V)
		if err != nil {
			return EventLogRow{}, err
		}
		ct, err := parseTime(closedAt.V)
		if err != nil {
			return EventLogRow{}, err
		}
		ms := ct.Sub(st).Milliseconds()
		r.DurationMS = &ms
	}
	if total, ok := sumTokens(inTokens, cacheTokens, writeTokens, outTokens); ok {
		r.Tokens = &total
	}
	return r, nil
}

// sumTokens adds the four nullable counters, reporting false when all are NULL.
func sumTokens(vals ...sql.Null[int64]) (int64, bool) {
	var total int64
	found := false
	for _, v := range vals {
		if v.Valid {
			total += v.V
			found = true
		}
	}
	return total, found
}
