package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// MasterMindBySession returns the mastermind row for one (harness_kind, session_id),
// false when there is none; `relevo mastermind init` reuses that row's id. An
// error here is not a missing mastermind, so the two outcomes are reported apart.
func (d *DB) MasterMindBySession(kind, session string) (MasterMind, bool, error) {
	return mastermindBySession(context.Background(), d.sqlDB, kind, session)
}

// MasterMindBySession is MasterMindBySession on the transaction's own origin: a
// mirror that met a binding naming an id the session no longer answers to looks
// up the row that does hold the session, in the same origin the upsert's own
// uniqueness guard covers.
func (t *Tx) MasterMindBySession(kind, session string) (MasterMind, bool, error) {
	var (
		p         MasterMind
		locator   sql.Null[string]
		firstSeen string
		lastSeen  string
	)
	err := t.queryRow(`SELECT id, harness_kind, session_id, transcript_locator, first_seen, last_seen
		   FROM mastermind WHERE `+originScope+` AND harness_kind = ? AND session_id = ?`,
		t.origin, kind, session).Scan(&p.ID, &p.HarnessKind, &p.SessionID, &locator, &firstSeen, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return MasterMind{}, false, nil
	}
	if err != nil {
		return MasterMind{}, false, fmt.Errorf("db: mastermind by session: %w", err)
	}

	p.TranscriptLocator = ptrIfValid(locator)
	if p.FirstSeen, err = parseTime(firstSeen); err != nil {
		return MasterMind{}, false, fmt.Errorf("db: mastermind by session: parse first_seen: %w", err)
	}
	if p.LastSeen, err = parseTime(lastSeen); err != nil {
		return MasterMind{}, false, fmt.Errorf("db: mastermind by session: parse last_seen: %w", err)
	}
	return p, true, nil
}

func mastermindBySession(ctx context.Context, q queryer, kind, session string) (MasterMind, bool, error) {
	var (
		p         MasterMind
		locator   sql.Null[string]
		firstSeen string
		lastSeen  string
	)
	err := q.QueryRowContext(ctx,
		`SELECT id, harness_kind, session_id, transcript_locator, first_seen, last_seen
		   FROM mastermind WHERE harness_kind = ? AND session_id = ?`,
		kind, session).Scan(&p.ID, &p.HarnessKind, &p.SessionID, &locator, &firstSeen, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return MasterMind{}, false, nil
	}
	if err != nil {
		return MasterMind{}, false, fmt.Errorf("db: mastermind by session: %w", err)
	}

	p.TranscriptLocator = ptrIfValid(locator)
	if p.FirstSeen, err = parseTime(firstSeen); err != nil {
		return MasterMind{}, false, fmt.Errorf("db: mastermind by session: parse first_seen: %w", err)
	}
	if p.LastSeen, err = parseTime(lastSeen); err != nil {
		return MasterMind{}, false, fmt.Errorf("db: mastermind by session: parse last_seen: %w", err)
	}
	return p, true, nil
}

const roundColumns = `id, binding_id, number, started_at, closed_at, outcome, actor,
	candidate, harness, provider, model, mode, tier,
	commits, tree, gate_result, gate_exit, gate_duration_ms,
	in_tokens, cache_tokens, write_tokens, out_tokens, cost_usd, cost_basis,
	report_outcome, switches`

func scanRound(s rowScanner) (Round, error) {
	var r Round
	var startedAt string
	var closedAt sql.Null[string]
	var candidate, harness, provider, model, mode, tier sql.Null[string]
	var commits sql.Null[int64]
	var tree, gateResult sql.Null[string]
	var gateExit sql.Null[int64]
	var gateDurationMS sql.Null[int64]
	var inTokens, cacheTokens, writeTokens, outTokens sql.Null[int64]
	var costUSD sql.Null[float64]
	var costBasis, reportOutcome sql.Null[string]

	if err := s.Scan(&r.ID, &r.BindingID, &r.Number, &startedAt, &closedAt, &r.Outcome, &r.Actor,
		&candidate, &harness, &provider, &model, &mode, &tier,
		&commits, &tree, &gateResult, &gateExit, &gateDurationMS,
		&inTokens, &cacheTokens, &writeTokens, &outTokens, &costUSD, &costBasis,
		&reportOutcome, &r.Switches); err != nil {
		return Round{}, fmt.Errorf("scan: %w", err)
	}

	st, err := parseTime(startedAt)
	if err != nil {
		return Round{}, fmt.Errorf("parse started_at: %w", err)
	}
	r.StartedAt = st
	if r.ClosedAt, err = nullTimeFrom(closedAt); err != nil {
		return Round{}, fmt.Errorf("parse closed_at: %w", err)
	}
	r.Candidate = ptrIfValid(candidate)
	r.Harness = ptrIfValid(harness)
	r.Provider = ptrIfValid(provider)
	r.Model = ptrIfValid(model)
	r.Mode = ptrIfValid(mode)
	r.Tier = ptrIfValid(tier)
	r.Commits = intPtr(commits)
	r.Tree = ptrIfValid(tree)
	r.GateResult = ptrIfValid(gateResult)
	r.GateExit = intPtr(gateExit)
	r.GateDurationMS = ptrIfValid(gateDurationMS)
	r.InTokens = ptrIfValid(inTokens)
	r.CacheTokens = ptrIfValid(cacheTokens)
	r.WriteTokens = ptrIfValid(writeTokens)
	r.OutTokens = ptrIfValid(outTokens)
	r.CostUSD = ptrIfValid(costUSD)
	r.CostBasis = ptrIfValid(costBasis)
	r.ReportOutcome = ptrIfValid(reportOutcome)

	return r, nil
}

func (d *DB) Rounds(bindingID string) ([]Round, error) {
	return listRounds(context.Background(), d.sqlDB, bindingID)
}

func (t *Tx) Rounds(bindingID string) ([]Round, error) { return listRounds(t.ctx, t.conn, bindingID) }

func listRounds(ctx context.Context, q queryer, bindingID string) ([]Round, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+roundColumns+` FROM round WHERE binding_id = ? ORDER BY number ASC`, bindingID)
	if err != nil {
		return nil, fmt.Errorf("db: rounds: %w", err)
	}
	out, err := collectRows(rows, scanRound)
	if err != nil {
		return nil, fmt.Errorf("db: rounds: %w", err)
	}
	return out, nil
}

func (d *DB) Artifact(roundID, kind string) (Artifact, bool, error) {
	return getArtifact(context.Background(), d.sqlDB, roundID, kind)
}

func (t *Tx) Artifact(roundID, kind string) (Artifact, bool, error) {
	return getArtifact(t.ctx, t.conn, roundID, kind)
}

func getArtifact(ctx context.Context, q queryer, roundID, kind string) (Artifact, bool, error) {
	var a Artifact
	var capturedAt string
	err := q.QueryRowContext(ctx, `SELECT id, round_id, kind, text, bytes, sha256, captured_at
		FROM artifact WHERE round_id = ? AND kind = ? AND consult_id = ''`, roundID, kind).
		Scan(&a.ID, &a.RoundID, &a.Kind, &a.Text, &a.Bytes, &a.SHA256, &capturedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, false, nil
	}
	if err != nil {
		return Artifact{}, false, fmt.Errorf("db: artifact: %w", err)
	}
	ct, err := parseTime(capturedAt)
	if err != nil {
		return Artifact{}, false, fmt.Errorf("db: artifact: parse captured_at: %w", err)
	}
	a.CapturedAt = ct
	return a, true, nil
}

func (d *DB) Transcript(ownerKind, ownerID string, fromSeq, limit int) ([]TranscriptRecord, error) {
	return listTranscript(context.Background(), d.sqlDB, ownerKind, ownerID, fromSeq, limit)
}

func (t *Tx) Transcript(ownerKind, ownerID string, fromSeq, limit int) ([]TranscriptRecord, error) {
	return listTranscript(t.ctx, t.conn, ownerKind, ownerID, fromSeq, limit)
}

func listTranscript(ctx context.Context, q queryer, ownerKind, ownerID string, fromSeq, limit int) ([]TranscriptRecord, error) {
	query := `SELECT id, owner_kind, owner_id, seq, ts, record_json, record_json_codec, rendered, rendered_codec
		FROM transcript WHERE owner_kind = ? AND owner_id = ? AND seq >= ? ORDER BY seq ASC`
	args := []any{ownerKind, ownerID, fromSeq}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: transcript: %w", err)
	}
	out, err := collectRows(rows, scanTranscript)
	if err != nil {
		return nil, fmt.Errorf("db: transcript: %w", err)
	}
	return out, nil
}

func scanTranscript(s rowScanner) (TranscriptRecord, error) {
	var r TranscriptRecord
	var ts sql.Null[string]
	var recordJSON, rendered []byte
	var recordCodec, renderedCodec int
	if err := s.Scan(&r.ID, &r.OwnerKind, &r.OwnerID, &r.Seq, &ts,
		&recordJSON, &recordCodec, &rendered, &renderedCodec); err != nil {
		return TranscriptRecord{}, err
	}
	if ts.Valid {
		t, terr := parseTime(ts.V)
		if terr != nil {
			return TranscriptRecord{}, terr
		}
		r.TS = &t
	}
	jsonBytes, err := decodeColumn(recordJSON, recordCodec)
	if err != nil {
		return TranscriptRecord{}, err
	}
	renderedBytes, err := decodeColumn(rendered, renderedCodec)
	if err != nil {
		return TranscriptRecord{}, err
	}
	r.RecordJSON = string(jsonBytes)
	r.Rendered = string(renderedBytes)
	return r, nil
}

func (d *DB) Cursor(source string) (Cursor, bool, error) {
	return getCursor(context.Background(), d.sqlDB, source)
}

func (t *Tx) Cursor(source string) (Cursor, bool, error) { return getCursor(t.ctx, t.conn, source) }

func getCursor(ctx context.Context, q queryer, source string) (Cursor, bool, error) {
	var c Cursor
	var wholeSHA sql.Null[string]
	var updatedAt string
	err := q.QueryRowContext(ctx, `SELECT source, byte_offset, head_sha, whole_sha, updated_at
		FROM ingest_cursor WHERE source = ?`, source).
		Scan(&c.Source, &c.ByteOffset, &c.HeadSHA, &wholeSHA, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Cursor{}, false, nil
	}
	if err != nil {
		return Cursor{}, false, fmt.Errorf("db: cursor: %w", err)
	}
	c.WholeSHA = ptrIfValid(wholeSHA)
	ua, err := parseTime(updatedAt)
	if err != nil {
		return Cursor{}, false, fmt.Errorf("db: cursor: parse updated_at: %w", err)
	}
	c.UpdatedAt = ua
	return c, true, nil
}
