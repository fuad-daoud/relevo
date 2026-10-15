package db

import (
	"context"
	"database/sql"
	"fmt"
)

// The read seam of the two history tables, the mirror of the write and prune
// seam in history_rows.go.
//
// A read takes the cutoff rather than a clock, for the reason the prune takes
// one: the window is decided once, by the caller, and every reader agrees on
// where it ends because they are all handed the same number.
//
// Neither read scopes to an origin. A machine holds a peer's history as soon as
// it imports it, and what it shows is the window across the fleet rather than
// this machine's slice of it, so both take every row the cutoff admits. What
// must stay on this machine's own rows is a behaviour decision, and no
// behaviour-deciding read comes here: those read the ledger (see ResolveOwner
// and the gates projection), which is why combining origins here cannot gate a
// pick.

// AvailabilityEvents returns every availability_event row no older than
// cutoffMS, for every origin, oldest first and the id breaking a tie so two
// machines reading the same file agree on the order.
func (d *DB) AvailabilityEvents(cutoffMS int64) ([]AvailabilityEvent, error) {
	return availabilityEvents(context.Background(), d.sqlDB, cutoffMS)
}

func availabilityEvents(ctx context.Context, q queryer, cutoffMS int64) ([]AvailabilityEvent, error) {
	const query = `SELECT id, at_ms, kind, provider, account, token, source, binding, note, since_ms
		FROM availability_event WHERE at_ms >= ? ORDER BY at_ms, id`
	rows, err := q.QueryContext(ctx, query, cutoffMS)
	if isMissingTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: read availability_event: %w", mapBusy(err))
	}
	defer func() { _ = rows.Close() }()

	var out []AvailabilityEvent
	for rows.Next() {
		var e AvailabilityEvent
		var since sql.NullInt64
		if err := rows.Scan(&e.ID, &e.AtMS, &e.Kind, &e.Provider, &e.Account, &e.Token,
			&e.Source, &e.Binding, &e.Note, &since); err != nil {
			return nil, fmt.Errorf("db: read availability_event: %w", err)
		}
		if since.Valid {
			ms := since.Int64
			e.SinceMS = &ms
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: read availability_event: %w", err)
	}
	return out, nil
}

// LatencySamples returns every latency_sample row no older than cutoffMS, for
// every origin, oldest first.
func (d *DB) LatencySamples(cutoffMS int64) ([]LatencySample, error) {
	return latencySamples(context.Background(), d.sqlDB, cutoffMS)
}

func latencySamples(ctx context.Context, q queryer, cutoffMS int64) ([]LatencySample, error) {
	const query = `SELECT id, at_ms, token, host, ttft_ms, total_ms, err
		FROM latency_sample WHERE at_ms >= ? ORDER BY at_ms, id`
	rows, err := q.QueryContext(ctx, query, cutoffMS)
	if isMissingTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: read latency_sample: %w", mapBusy(err))
	}
	defer func() { _ = rows.Close() }()

	var out []LatencySample
	for rows.Next() {
		var s LatencySample
		if err := rows.Scan(&s.ID, &s.AtMS, &s.Token, &s.Host, &s.TTFTMS, &s.TotalMS, &s.Err); err != nil {
			return nil, fmt.Errorf("db: read latency_sample: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: read latency_sample: %w", err)
	}
	return out, nil
}

// CountHistoryRows is how many rows of each history table the file holds, for
// every origin. The migration's own check and `relevo doctor`'s are better off
// counting than reading thirty days of rows to learn a file's size.
func (d *DB) CountHistoryRows() (HistoryPruned, error) {
	ctx := context.Background()
	var out HistoryPruned
	var err error
	if out.Events, err = countHistoryRows(ctx, d.sqlDB, AvailabilityEventTable); err != nil {
		return HistoryPruned{}, err
	}
	out.Samples, err = countHistoryRows(ctx, d.sqlDB, LatencySampleTable)
	return out, err
}

func countHistoryRows(ctx context.Context, q queryer, tbl string) (int64, error) {
	var n int64
	query := "SELECT COUNT(*) FROM " + quoteIdent(tbl)
	if err := q.QueryRowContext(ctx, query).Scan(&n); err != nil {
		if isMissingTable(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("db: count %s: %w", tbl, mapBusy(err))
	}
	return n, nil
}
