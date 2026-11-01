package syncworker

// Each machine's month of sync traffic, published into the shared meta table
// so a reader can tell what the account spent rather than only what one machine
// spent. The rows live beside the floors and the key material: one small fact
// per origin, kept in meta rather than in a table of its own.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// VerbPublishTraffic writes this worker's own traffic row and answers with
// every origin's row as the replica holds it after the write.
const VerbPublishTraffic Verb = "publish_traffic"

// trafficPrefix is what a traffic row's meta key begins with. Older builds
// reach meta only by exact keys or their own prefixes -- floor.,
// signed_from., epoch_cut., key_wrapped., claim:, and the format row -- so a
// traffic row is invisible to them and a mixed-version fleet keeps syncing
// without a log-format bump.
const trafficPrefix = "traffic."

// TrafficRow is one installation's published month as the pipe carries it and
// as its meta row stores it: the UTC month the counts cover, the Turso bytes
// pushed and pulled in it, and when the publisher read its counters.
type TrafficRow struct {
	Month string    `json:"month"`
	Push  int64     `json:"push"`
	Pull  int64     `json:"pull"`
	At    time.Time `json:"at"`
}

// trafficBackend is a backend that carries traffic. It is an optional assertion
// rather than a Backend method so the worker's test fakes of Backend stay
// whole.
type trafficBackend interface {
	PublishTraffic(row TrafficRow) (map[string]TrafficRow, error)
}

// PublishTraffic writes this worker's own row, pushes it, and answers with
// every origin's row as this replica holds it after the write. The key is the
// origin from hello rather than a call field: no caller may write another
// machine's row.
func (b *TursoBackend) PublishTraffic(row TrafficRow) (map[string]TrafficRow, error) {
	if err := b.ready(); err != nil {
		return nil, err
	}
	if err := checkTrafficRow(row); err != nil {
		return nil, err
	}
	ctx := context.Background()
	body, err := json.Marshal(row)
	if err != nil {
		return nil, fmt.Errorf("syncworker: encode the traffic row: %w", err)
	}
	if err := b.writeTraffic(ctx, trafficPrefix+b.spec.Origin, string(body)); err != nil {
		return nil, fmt.Errorf("syncworker: publish traffic: %w", markDriverRefusal(err))
	}
	if err := b.transfer(ctx, b.driver.Push); err != nil {
		return nil, fmt.Errorf("syncworker: push traffic: %w", markTransferRefusal(err))
	}
	rows, err := b.readTrafficRows(ctx)
	if err != nil {
		return nil, fmt.Errorf("syncworker: read the traffic rows: %w", markDriverRefusal(err))
	}
	return rows, nil
}

// checkTrafficRow refuses a row no reader could bill a month for: a month that
// is not "2006-01", or a count below zero.
func checkTrafficRow(row TrafficRow) error {
	if _, err := time.Parse("2006-01", row.Month); err != nil {
		return fmt.Errorf("%w: traffic month %q", ErrProtocol, row.Month)
	}
	if row.Push < 0 || row.Pull < 0 {
		return fmt.Errorf("%w: traffic counts below zero", ErrProtocol)
	}
	return nil
}

// writeTraffic records one meta row as an UPDATE then an INSERT in a single
// transaction: not an upsert, for the reason writeMark gives.
func (b *TursoBackend) writeTraffic(ctx context.Context, key, value string) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		"UPDATE meta SET value = ? WHERE key = ?", value, key)
	if err != nil {
		return fmt.Errorf("syncworker: traffic %s: %w", key, err)
	}
	moved, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("syncworker: traffic %s: %w", key, err)
	}
	if moved > 0 {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO meta (key, value) VALUES (?, ?)", key, value); err != nil {
		return fmt.Errorf("syncworker: traffic %s: %w", key, err)
	}
	return tx.Commit()
}

// readTrafficRows answers with every origin's traffic row as this replica
// holds it. A row that does not parse is skipped: the rows are written by other
// machines, and one malformed row must not stop every reader learning the
// total.
func (b *TursoBackend) readTrafficRows(ctx context.Context) (map[string]TrafficRow, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT key, value FROM meta WHERE key LIKE ?", trafficPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]TrafficRow)
	for rows.Next() {
		var key, value sql.NullString
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		origin := strings.TrimPrefix(key.String, trafficPrefix)
		row, ok := parseTrafficRow(origin, value.String)
		if ok {
			out[origin] = row
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// parseTrafficRow reads one traffic meta row, and reports false for a row
// that is not one: an empty origin, a value that is not this shape, a month
// that is not "2006-01", a negative count, or a time that is not RFC 3339.
func parseTrafficRow(origin, value string) (TrafficRow, bool) {
	var body struct {
		Month string `json:"month"`
		Push  int64  `json:"push"`
		Pull  int64  `json:"pull"`
		At    string `json:"at"`
	}
	if err := json.Unmarshal([]byte(value), &body); err != nil {
		return TrafficRow{}, false
	}
	if origin == "" || body.Push < 0 || body.Pull < 0 {
		return TrafficRow{}, false
	}
	if _, err := time.Parse("2006-01", body.Month); err != nil {
		return TrafficRow{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, body.At)
	if err != nil {
		return TrafficRow{}, false
	}
	return TrafficRow{Month: body.Month, Push: body.Push, Pull: body.Pull, At: at}, true
}

// publishTraffic records this worker's own traffic row and answers with every
// origin's row the replica holds. The row carries no origin because the origin
// is the worker's own from hello: naming one would let a caller write another
// machine's row.
func (s *server) publishTraffic(req Request) Response {
	if req.Traffic == nil {
		return refuse(req, fmt.Errorf("%w: publish_traffic with no row", ErrProtocol))
	}
	tb, ok := s.backend.(trafficBackend)
	if !ok {
		return refuse(req, fmt.Errorf("%w: this worker's backend publishes no traffic", ErrProtocol))
	}
	rows, err := tb.PublishTraffic(*req.Traffic)
	if err != nil {
		return refuse(req, err)
	}
	return Response{ID: req.ID, OK: true, Traffic: rows}
}
