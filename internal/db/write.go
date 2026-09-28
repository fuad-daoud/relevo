package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"modernc.org/sqlite"
)

func nullableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func nullableInt(i *int) any {
	if i == nil {
		return nil
	}
	return *i
}

func nullableInt64(i *int64) any {
	if i == nil {
		return nil
	}
	return *i
}

func nullableFloat64(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// UpsertRepo inserts or updates r by its natural key: origin_url when set,
// else common_dir. A hit fills the other column when it is null in the db and
// set on r.
func (t *Tx) UpsertRepo(r Repo) (string, error) {
	switch {
	case r.OriginURL != nil:
		return t.upsertRepoBy("origin_url", *r.OriginURL, r)
	case r.CommonDir != nil:
		return t.upsertRepoBy("common_dir", *r.CommonDir, r)
	default:
		return "", fmt.Errorf("db: upsert repo: Repo needs OriginURL or CommonDir: %w", ErrInvalid)
	}
}

func (t *Tx) upsertRepoBy(col, val string, r Repo) (string, error) {
	var id string
	var originURL, commonDir sql.Null[string]
	err := t.queryRow(`SELECT id, origin_url, common_dir FROM repo WHERE `+col+` = ?`, val).
		Scan(&id, &originURL, &commonDir)
	if err == nil {
		if !originURL.Valid && r.OriginURL != nil {
			if _, err := t.exec(`UPDATE repo SET origin_url = ? WHERE id = ?`, *r.OriginURL, id); err != nil {
				return "", fmt.Errorf("db: upsert repo: fill origin_url: %w", mapBusy(err))
			}
		}
		if !commonDir.Valid && r.CommonDir != nil {
			if _, err := t.exec(`UPDATE repo SET common_dir = ? WHERE id = ?`, *r.CommonDir, id); err != nil {
				return "", fmt.Errorf("db: upsert repo: fill common_dir: %w", mapBusy(err))
			}
		}
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("db: upsert repo: select: %w", mapBusy(err))
	}

	id = NewID()
	firstSeen := r.FirstSeen
	if firstSeen.IsZero() {
		firstSeen = time.Now()
	}
	if _, err := t.exec(`INSERT INTO repo (id, origin_url, common_dir, first_seen) VALUES (?, ?, ?, ?)`,
		id, nullableString(r.OriginURL), nullableString(r.CommonDir), formatTime(firstSeen)); err != nil {
		return "", fmt.Errorf("db: upsert repo: insert: %w", mapBusy(err))
	}
	return id, nil
}

// UpsertMasterMind inserts or updates p by its natural key: (harness_kind,
// session_id). When p.ID is set the record's own id wins instead: ingest
// upserts by the id `relevo mastermind init` minted, and the natural key stays as
// the uniqueness guard. A (harness_kind, session_id) another id already holds
// is refused with ErrInvalid rather than silently merging two identities.
func (t *Tx) UpsertMasterMind(p MasterMind) (string, error) {
	if p.HarnessKind == "" || p.SessionID == "" {
		return "", fmt.Errorf("db: upsert mastermind: HarnessKind and SessionID are required: %w", ErrInvalid)
	}

	lastSeen := p.LastSeen
	if lastSeen.IsZero() {
		lastSeen = time.Now()
	}

	if p.ID != "" {
		return t.upsertMasterMindByID(p, lastSeen)
	}

	var id string
	var locator sql.Null[string]
	err := t.queryRow(`SELECT id, transcript_locator FROM mastermind WHERE harness_kind = ? AND session_id = ?`,
		p.HarnessKind, p.SessionID).Scan(&id, &locator)
	if err == nil {
		// The locator updates only when p now carries one; otherwise the db's
		// existing value (if any) is kept, not cleared.
		var locatorArg any
		if locator.Valid {
			locatorArg = locator.V
		}
		if p.TranscriptLocator != nil {
			locatorArg = *p.TranscriptLocator
		}
		if _, err := t.exec(`UPDATE mastermind SET last_seen = ?, transcript_locator = ? WHERE id = ?`,
			formatTime(lastSeen), locatorArg, id); err != nil {
			return "", fmt.Errorf("db: upsert mastermind: update: %w", mapBusy(err))
		}
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("db: upsert mastermind: select: %w", mapBusy(err))
	}

	id = NewID()
	firstSeen := p.FirstSeen
	if firstSeen.IsZero() {
		firstSeen = lastSeen
	}
	if _, err := t.exec(`INSERT INTO mastermind (id, harness_kind, session_id, transcript_locator, first_seen, last_seen) VALUES (?, ?, ?, ?, ?, ?)`,
		id, p.HarnessKind, p.SessionID, nullableString(p.TranscriptLocator), formatTime(firstSeen), formatTime(lastSeen)); err != nil {
		return "", fmt.Errorf("db: upsert mastermind: insert: %w", mapBusy(err))
	}
	return id, nil
}

func (t *Tx) upsertMasterMindByID(p MasterMind, lastSeen time.Time) (string, error) {
	if err := t.assertMasterMindKeyFree(p.ID, p.HarnessKind, p.SessionID); err != nil {
		return "", err
	}

	var existing string
	err := t.queryRow(`SELECT id FROM mastermind WHERE id = ?`, p.ID).Scan(&existing)
	switch {
	case err == nil:
		set := []string{"harness_kind = ?", "session_id = ?", "last_seen = ?"}
		args := []any{p.HarnessKind, p.SessionID, formatTime(lastSeen)}
		if p.TranscriptLocator != nil {
			set = append(set, "transcript_locator = ?")
			args = append(args, *p.TranscriptLocator)
		}
		args = append(args, p.ID)
		if _, uerr := t.exec(`UPDATE mastermind SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...); uerr != nil {
			return "", fmt.Errorf("db: upsert mastermind by id: update: %w", mapMasterMindKey(uerr))
		}
		return p.ID, nil
	case errors.Is(err, sql.ErrNoRows):
		firstSeen := p.FirstSeen
		if firstSeen.IsZero() {
			firstSeen = lastSeen
		}
		if _, ierr := t.exec(`INSERT INTO mastermind (id, harness_kind, session_id, transcript_locator, first_seen, last_seen) VALUES (?, ?, ?, ?, ?, ?)`,
			p.ID, p.HarnessKind, p.SessionID, nullableString(p.TranscriptLocator),
			formatTime(firstSeen), formatTime(lastSeen)); ierr != nil {
			return "", fmt.Errorf("db: upsert mastermind by id: insert: %w", mapMasterMindKey(ierr))
		}
		return p.ID, nil
	default:
		return "", fmt.Errorf("db: upsert mastermind by id: select: %w", mapBusy(err))
	}
}

// assertMasterMindKeyFree refuses to hand one (harness_kind, session_id) to a
// second id, so the failure surfaces as ErrInvalid.
func (t *Tx) assertMasterMindKeyFree(id, kind, session string) error {
	var other string
	err := t.queryRow(`SELECT id FROM mastermind WHERE harness_kind = ? AND session_id = ?`, kind, session).Scan(&other)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("db: upsert mastermind by id: select natural key: %w", mapBusy(err))
	}
	if other == id {
		return nil
	}
	return fmt.Errorf("db: upsert mastermind by id: (harness_kind, session_id) is held by mastermind %q: %w", other, ErrInvalid)
}

// sqliteConstraint is SQLITE_CONSTRAINT. A UNIQUE index violation reports it
// possibly with an extended code, which is why mapMasterMindKey masks the low
// byte.
const sqliteConstraint = 19

// mapMasterMindKey turns a sqlite constraint violation into ErrInvalid.
func mapMasterMindKey(err error) error {
	if err == nil {
		return nil
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqliteConstraint {
		return fmt.Errorf("mastermind (harness_kind, session_id) already exists: %w", ErrInvalid)
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return fmt.Errorf("mastermind (harness_kind, session_id) already exists: %w", ErrInvalid)
	}
	return mapBusy(err)
}

// UpsertBinding inserts or updates b by its natural key: (name, created_at).
func (t *Tx) UpsertBinding(b Binding) (string, error) {
	if b.Name == "" {
		return "", fmt.Errorf("db: upsert binding: Name: %w", ErrInvalid)
	}
	if b.CreatedAt.IsZero() {
		return "", fmt.Errorf("db: upsert binding: CreatedAt: %w", ErrInvalid)
	}
	if !ValidIngestSource(b.IngestSource) {
		return "", fmt.Errorf("db: upsert binding: IngestSource: %w", ErrInvalid)
	}

	createdAt := formatTime(b.CreatedAt)

	var id string
	err := t.queryRow(`SELECT id FROM binding WHERE name = ? AND created_at = ?`, b.Name, createdAt).Scan(&id)
	if err == nil {
		if _, uerr := t.exec(`UPDATE binding SET repo_id=?, mastermind_id=?, feature=?, ticket=?, forked_from_binding_id=?,
				forked_from_round=?, cwd=?, worktree=?, branch=?, base_commit=?, tier=?, gate=?,
				builder_mode=?, server=?, final_state=?, archived_at=?, archive_path=?, ingest_source=?
			WHERE id=?`,
			nullableString(b.RepoID), nullableString(b.MasterMindID), nullableString(b.Feature),
			nullableString(b.Ticket),
			nullableString(b.ForkedFromBindingID), nullableInt(b.ForkedFromRound),
			b.CWD, nullableString(b.Worktree), nullableString(b.Branch), nullableString(b.BaseCommit),
			nullableString(b.Tier), nullableString(b.Gate), b.BuilderMode, nullableString(b.Server),
			nullableString(b.FinalState), nullableTime(b.ArchivedAt), nullableString(b.ArchivePath),
			b.IngestSource, id); uerr != nil {
			return "", fmt.Errorf("db: upsert binding: update: %w", mapBusy(uerr))
		}
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("db: upsert binding: select: %w", mapBusy(err))
	}

	id = NewID()
	if _, err := t.exec(`INSERT INTO binding (id, name, repo_id, mastermind_id, feature, ticket, forked_from_binding_id,
			forked_from_round, cwd, worktree, branch, base_commit, tier, gate, builder_mode, server,
			created_at, final_state, archived_at, archive_path, ingest_source)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, b.Name, nullableString(b.RepoID), nullableString(b.MasterMindID), nullableString(b.Feature),
		nullableString(b.Ticket),
		nullableString(b.ForkedFromBindingID), nullableInt(b.ForkedFromRound),
		b.CWD, nullableString(b.Worktree), nullableString(b.Branch), nullableString(b.BaseCommit),
		nullableString(b.Tier), nullableString(b.Gate), b.BuilderMode, nullableString(b.Server),
		createdAt, nullableString(b.FinalState), nullableTime(b.ArchivedAt), nullableString(b.ArchivePath),
		b.IngestSource); err != nil {
		return "", fmt.Errorf("db: upsert binding: insert: %w", mapBusy(err))
	}
	return id, nil
}

// UpsertRound inserts or updates r by its natural key: (binding_id, number).
func (t *Tx) UpsertRound(r Round) (string, error) {
	if r.BindingID == "" {
		return "", fmt.Errorf("db: upsert round: BindingID: %w", ErrInvalid)
	}
	if !ValidOutcome(r.Outcome) {
		return "", fmt.Errorf("db: upsert round: Outcome: %w", ErrInvalid)
	}
	// An empty actor is the builder actor, the column's own default and the seeded actor.
	if r.Actor == "" {
		r.Actor = "builder"
	}

	var id string
	err := t.queryRow(`SELECT id FROM round WHERE binding_id = ? AND number = ?`, r.BindingID, r.Number).Scan(&id)
	if err == nil {
		if _, uerr := t.exec(`UPDATE round SET started_at=?, closed_at=?, outcome=?, candidate=?,
				harness=?, provider=?, model=?, mode=?, actor=?, tier=?,
				commits=?, tree=?, gate_result=?, gate_exit=?, gate_duration_ms=?,
				in_tokens=?, cache_tokens=?, write_tokens=?, out_tokens=?, cost_usd=?, cost_basis=?,
				report_outcome=?, switches=?
			WHERE id=?`,
			formatTime(r.StartedAt), nullableTime(r.ClosedAt), r.Outcome, nullableString(r.Candidate),
			nullableString(r.Harness), nullableString(r.Provider), nullableString(r.Model),
			nullableString(r.Mode), r.Actor, nullableString(r.Tier),
			nullableInt(r.Commits), nullableString(r.Tree), nullableString(r.GateResult), nullableInt(r.GateExit),
			nullableInt64(r.GateDurationMS),
			nullableInt64(r.InTokens), nullableInt64(r.CacheTokens), nullableInt64(r.WriteTokens), nullableInt64(r.OutTokens),
			nullableFloat64(r.CostUSD), nullableString(r.CostBasis),
			nullableString(r.ReportOutcome), r.Switches,
			id); uerr != nil {
			return "", fmt.Errorf("db: upsert round: update: %w", mapBusy(uerr))
		}
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("db: upsert round: select: %w", mapBusy(err))
	}

	id = NewID()
	startedAt := r.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	if _, err := t.exec(`INSERT INTO round (id, binding_id, number, started_at, closed_at, outcome,
			candidate, harness, provider, model, mode, actor, tier,
			commits, tree, gate_result, gate_exit, gate_duration_ms,
			in_tokens, cache_tokens, write_tokens, out_tokens, cost_usd, cost_basis,
			report_outcome, switches)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, r.BindingID, r.Number, formatTime(startedAt), nullableTime(r.ClosedAt), r.Outcome,
		nullableString(r.Candidate), nullableString(r.Harness), nullableString(r.Provider),
		nullableString(r.Model), nullableString(r.Mode), r.Actor, nullableString(r.Tier),
		nullableInt(r.Commits), nullableString(r.Tree), nullableString(r.GateResult), nullableInt(r.GateExit),
		nullableInt64(r.GateDurationMS),
		nullableInt64(r.InTokens), nullableInt64(r.CacheTokens), nullableInt64(r.WriteTokens), nullableInt64(r.OutTokens),
		nullableFloat64(r.CostUSD), nullableString(r.CostBasis),
		nullableString(r.ReportOutcome), r.Switches); err != nil {
		return "", fmt.Errorf("db: upsert round: insert: %w", mapBusy(err))
	}
	return id, nil
}

// AppendEvents inserts every event not already present, keyed on
// (binding_id, seq), and returns how many were added.
func (t *Tx) AppendEvents(bindingID string, evs []Event) (int, error) {
	added := 0
	for _, e := range evs {
		id := e.ID
		if id == "" {
			id = NewID()
		}
		res, err := t.exec(`INSERT OR IGNORE INTO event (id, binding_id, round_id, seq, ts, kind, direction,
				note, path, delivered_at, confirmed, late, flagged, flagged_by, entry_json)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, bindingID, nullableString(e.RoundID), e.Seq, formatTime(e.TS), e.Kind, e.Direction,
			nullableString(e.Note), nullableString(e.Path), nullableTime(e.DeliveredAt),
			boolToInt(e.Confirmed), boolToInt(e.Late), nullableInt(e.Flagged), nullableString(e.FlaggedBy), e.EntryJSON)
		if err != nil {
			return added, fmt.Errorf("db: append events: %w", mapBusy(err))
		}
		n, err := res.RowsAffected()
		if err != nil {
			return added, fmt.Errorf("db: append events: rows affected: %w", err)
		}
		added += int(n)
	}
	return added, nil
}

func (t *Tx) UpsertArtifact(a Artifact) error {
	if !ValidArtifactKind(a.Kind) {
		return fmt.Errorf("db: upsert artifact: Kind: %w", ErrInvalid)
	}

	consultID := ""
	if a.ConsultID != nil {
		consultID = *a.ConsultID
	}

	var id string
	err := t.queryRow(`SELECT id FROM artifact WHERE round_id = ? AND kind = ? AND consult_id = ?`,
		a.RoundID, a.Kind, consultID).Scan(&id)
	if err == nil {
		if _, uerr := t.exec(`UPDATE artifact SET text=?, bytes=?, sha256=?, captured_at=? WHERE id=?`,
			a.Text, a.Bytes, a.SHA256, formatTime(a.CapturedAt), id); uerr != nil {
			return fmt.Errorf("db: upsert artifact: update: %w", mapBusy(uerr))
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("db: upsert artifact: select: %w", mapBusy(err))
	}

	id = a.ID
	if id == "" {
		id = NewID()
	}
	if _, err := t.exec(`INSERT INTO artifact (id, round_id, kind, consult_id, text, bytes, sha256, captured_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		id, a.RoundID, a.Kind, consultID, a.Text, a.Bytes, a.SHA256, formatTime(a.CapturedAt)); err != nil {
		return fmt.Errorf("db: upsert artifact: insert: %w", mapBusy(err))
	}
	return nil
}

// DeleteArtifact removes the artifact row with this id. A missing id is not an
// error: the caller may be applying a plan built against a database that has
// since lost the row, and the end state it wants already holds.
func (t *Tx) DeleteArtifact(id string) error {
	if _, err := t.exec(`DELETE FROM artifact WHERE id = ?`, id); err != nil {
		return fmt.Errorf("db: delete artifact %s: %w", id, mapBusy(err))
	}
	return nil
}

// DeleteRoundTranscript removes every transcript row of one mirror round and
// returns how many rows went. The owner kind is hard-coded to OwnerRound, so
// this cannot delete a mastermind transcript even if handed a mastermind's id.
func (t *Tx) DeleteRoundTranscript(roundID string) (int64, error) {
	res, err := t.exec(`DELETE FROM transcript WHERE owner_kind = ? AND owner_id = ?`, OwnerRound, roundID)
	if err != nil {
		return 0, fmt.Errorf("db: delete round transcript %s: %w", roundID, mapBusy(err))
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("db: delete round transcript %s: rows affected: %w", roundID, err)
	}
	return n, nil
}

// AppendTranscript inserts every record not already present, keyed on
// (owner_kind, owner_id, seq), and returns how many were added.
func (t *Tx) AppendTranscript(ownerKind, ownerID string, recs []TranscriptRecord) (int, error) {
	if !ValidOwnerKind(ownerKind) {
		return 0, fmt.Errorf("db: append transcript: OwnerKind: %w", ErrInvalid)
	}

	added := 0
	for _, r := range recs {
		id := r.ID
		if id == "" {
			id = NewID()
		}
		res, err := t.exec(`INSERT OR IGNORE INTO transcript (id, owner_kind, owner_id, seq, ts, record_json, rendered)
			VALUES (?,?,?,?,?,?,?)`,
			id, ownerKind, ownerID, r.Seq, nullableTime(r.TS), r.RecordJSON, r.Rendered)
		if err != nil {
			return added, fmt.Errorf("db: append transcript: %w", mapBusy(err))
		}
		n, err := res.RowsAffected()
		if err != nil {
			return added, fmt.Errorf("db: append transcript: rows affected: %w", err)
		}
		added += int(n)
	}
	return added, nil
}

// LinkEvents sets round_id = roundID on every event of bindingID whose
// round_id is still null and whose seq is in seqs. The WHERE round_id IS NULL
// clause makes a second call a no-op rather than overwriting the link.
func (t *Tx) LinkEvents(bindingID, roundID string, seqs []int) error {
	if roundID == "" {
		return fmt.Errorf("db: link events: RoundID: %w", ErrInvalid)
	}
	if len(seqs) == 0 {
		return nil
	}

	placeholders := make([]string, len(seqs))
	args := make([]any, 0, len(seqs)+2)
	args = append(args, roundID, bindingID)
	for i, seq := range seqs {
		placeholders[i] = "?"
		args = append(args, seq)
	}

	query := `UPDATE event SET round_id = ? WHERE binding_id = ? AND round_id IS NULL AND seq IN (` +
		strings.Join(placeholders, ",") + `)`
	if _, err := t.exec(query, args...); err != nil {
		return fmt.Errorf("db: link events: %w", mapBusy(err))
	}
	return nil
}

func (t *Tx) SaveCursor(c Cursor) error {
	if c.Source == "" {
		return fmt.Errorf("db: save cursor: Source: %w", ErrInvalid)
	}

	updatedAt := c.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}
	if _, err := t.exec(`INSERT OR REPLACE INTO ingest_cursor (source, byte_offset, head_sha, whole_sha, updated_at)
		VALUES (?,?,?,?,?)`,
		c.Source, c.ByteOffset, c.HeadSHA, nullableString(c.WholeSHA), formatTime(updatedAt)); err != nil {
		return fmt.Errorf("db: save cursor: %w", mapBusy(err))
	}
	return nil
}

// The *DB forms below wrap one Tx each.

func (d *DB) UpsertRepo(r Repo) (string, error) {
	var id string
	err := d.Tx(func(t *Tx) error {
		var err error
		id, err = t.UpsertRepo(r)
		return err
	})
	return id, err
}

func (d *DB) UpsertMasterMind(p MasterMind) (string, error) {
	var id string
	err := d.Tx(func(t *Tx) error {
		var err error
		id, err = t.UpsertMasterMind(p)
		return err
	})
	return id, err
}

func (d *DB) UpsertBinding(b Binding) (string, error) {
	var id string
	err := d.Tx(func(t *Tx) error {
		var err error
		id, err = t.UpsertBinding(b)
		return err
	})
	return id, err
}

func (d *DB) UpsertRound(r Round) (string, error) {
	var id string
	err := d.Tx(func(t *Tx) error {
		var err error
		id, err = t.UpsertRound(r)
		return err
	})
	return id, err
}

func (d *DB) AppendEvents(bindingID string, evs []Event) (int, error) {
	var added int
	err := d.Tx(func(t *Tx) error {
		var err error
		added, err = t.AppendEvents(bindingID, evs)
		return err
	})
	return added, err
}

func (d *DB) UpsertArtifact(a Artifact) error {
	return d.Tx(func(t *Tx) error {
		return t.UpsertArtifact(a)
	})
}

func (d *DB) AppendTranscript(ownerKind, ownerID string, recs []TranscriptRecord) (int, error) {
	var added int
	err := d.Tx(func(t *Tx) error {
		var err error
		added, err = t.AppendTranscript(ownerKind, ownerID, recs)
		return err
	})
	return added, err
}

func (d *DB) SaveCursor(c Cursor) error {
	return d.Tx(func(t *Tx) error {
		return t.SaveCursor(c)
	})
}
