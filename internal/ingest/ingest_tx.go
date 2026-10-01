package ingest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

type logRead struct {
	present  bool
	entries  []store.LogEntry
	lines    [][]byte
	startSeq int
	cursor   db.Cursor
	// resumed reports that this read continued from a saved cursor instead of
	// reading the member from the top, so the database's own earlier rows are
	// the only copy of what came before.
	resumed bool
}

func (r *ingestRun) upsertRepo(tx *db.Tx) (*string, error) {
	if r.ref == nil || (r.ref.OriginURL == "" && r.ref.CommonDir == "") {
		return nil, nil
	}
	id, err := tx.UpsertRepo(db.Repo{
		OriginURL: nonEmptyPtr(r.ref.OriginURL),
		CommonDir: nonEmptyPtr(r.ref.CommonDir),
	})
	if err != nil {
		return nil, fmt.Errorf("upsert repo: %w", err)
	}
	return &id, nil
}

func (r *ingestRun) upsertMasterMind(tx *db.Tx) (*string, error) {
	if r.b.MasterMind.SessionID == "" {
		return nil, nil
	}
	record := db.MasterMind{
		ID:                r.b.MasterMindID,
		HarnessKind:       r.b.MasterMind.Kind,
		SessionID:         r.b.MasterMind.SessionID,
		TranscriptLocator: nonEmptyPtr(r.b.MasterMind.TranscriptLocator),
	}
	id, err := tx.UpsertMasterMind(record)
	if err != nil {
		// A binding can name a mastermind id its session no longer answers to:
		// the record was forgotten and the session registered anew, and the
		// binding kept the old id. In this origin the session is the identity,
		// so the mirror follows the id that already holds it instead of
		// failing the binding's whole mirror run every tick.
		if !errors.Is(err, db.ErrInvalid) {
			return nil, fmt.Errorf("upsert mastermind: %w", err)
		}
		held, ok, lerr := tx.MasterMindBySession(record.HarnessKind, record.SessionID)
		if lerr != nil {
			return nil, fmt.Errorf("upsert mastermind: %w", lerr)
		}
		if !ok {
			return nil, fmt.Errorf("upsert mastermind: %w", err)
		}
		r.logger.Warn("ingest: mastermind id is stale; following the session",
			"binding", r.b.Name, "from", record.ID, "to", held.ID)
		record.ID = held.ID
		if id, err = tx.UpsertMasterMind(record); err != nil {
			return nil, fmt.Errorf("upsert mastermind: %w", err)
		}
	}
	return &id, nil
}

func (r *ingestRun) readLog(tx *db.Tx) (logRead, error) {
	var lr logRead
	if !r.members["log.jsonl"] {
		return lr, nil
	}
	lr.present = true

	opener, key := sourceOpener(r.src, "log.jsonl")
	cur, found, err := tx.Cursor(key)
	if err != nil {
		return logRead{}, fmt.Errorf("log cursor: %w", err)
	}
	lines, startSeq, next, reset, err := readAppendOnly(opener, key, cur, found)
	if err != nil {
		return logRead{}, fmt.Errorf("read log.jsonl: %w", err)
	}
	if reset {
		r.logger.Info("ingest: cursor reset", "binding", r.b.Name, "member", "log.jsonl")
	}
	lr.startSeq, lr.cursor, lr.lines = startSeq, next, lines
	lr.resumed = found && !reset
	for _, line := range lines {
		var e store.LogEntry
		if err := json.Unmarshal(line, &e); err != nil {
			r.logger.Warn("ingest: undecodable log entry", "binding", r.b.Name, "err", err)
			continue
		}
		lr.entries = append(lr.entries, e)
	}
	return lr, nil
}

// builderModeOf defaults to "pane": archives from before headless mode record a
// pane builder.
func builderModeOf(b store.Binding) string {
	if b.Builder.Mode != "" {
		return string(b.Builder.Mode)
	}
	return "pane"
}

// upsertBinding writes the binding row. An archived binding's CWD is always gone,
// so b.Repo is its only path to a repo row.
func (r *ingestRun) upsertBinding(tx *db.Tx, repoID, mastermindID *string, log logRead) (string, error) {
	createdAt := r.b.CreatedAt
	if createdAt.IsZero() {
		if len(log.entries) > 0 {
			createdAt = log.entries[0].TS
		} else {
			createdAt = r.deps.Now()
		}
	}

	var forkedFromBindingID *string
	if r.b.ForkedFrom != "" {
		if fb, found, err := tx.Binding(r.b.ForkedFrom); err == nil && found {
			id := fb.ID
			forkedFromBindingID = &id
		}
	}
	var forkedFromRound *int
	if r.b.ForkedAtRound > 0 {
		n := r.b.ForkedAtRound
		forkedFromRound = &n
	}

	var archivedAt *time.Time
	var archivePath *string
	if r.kind == "archive" {
		_, path := r.src.Origin()
		p := path
		archivePath = &p
		if ts, ok := r.src.(archivedAtter); ok {
			if at, found := ts.ArchivedAt(); found {
				archivedAt = &at
			}
		}
	}

	bindingID, err := tx.UpsertBinding(db.Binding{
		Name:                r.b.Name,
		RepoID:              repoID,
		MasterMindID:        mastermindID,
		Feature:             nonEmptyPtr(r.b.Feature),
		Ticket:              nonEmptyPtr(r.b.Ticket),
		ForkedFromBindingID: forkedFromBindingID,
		ForkedFromRound:     forkedFromRound,
		CWD:                 r.b.CWD,
		Worktree:            nonEmptyPtr(r.b.Worktree),
		Branch:              nonEmptyPtr(r.b.Branch),
		BaseCommit:          nonEmptyPtr(r.b.Base),
		Tier:                nonEmptyPtr(r.b.Tier),
		Gate:                nonEmptyPtr(r.b.Gate),
		BuilderMode:         builderModeOf(r.b),
		Server:              nonEmptyPtr(r.b.Builder.Server),
		CreatedAt:           createdAt,
		FinalState:          nonEmptyPtr(string(r.b.State)),
		ArchivedAt:          archivedAt,
		ArchivePath:         archivePath,
		IngestSource:        r.kind,
	})
	if err != nil {
		return "", fmt.Errorf("upsert binding: %w", err)
	}
	return bindingID, nil
}

// appendEvents appends log.jsonl's new lines and returns "all": every entry this
// binding has ever logged. A fresh cursor already holds the whole file, so the
// db's own earlier rows are folded in only when the cursor resumed.
func (r *ingestRun) appendEvents(tx *db.Tx, bindingID string, log logRead) ([]store.LogEntry, error) {
	all := log.entries
	if !log.present {
		return all, nil
	}

	if had, err := tx.Events(bindingID, 0); err == nil && len(had) > 0 && log.resumed {
		merged := make([]store.LogEntry, 0, len(had)+len(log.entries))
		for _, ev := range had {
			var e store.LogEntry
			if json.Unmarshal([]byte(ev.EntryJSON), &e) == nil {
				merged = append(merged, e)
			}
		}
		all = append(merged, log.entries...)
	}

	if len(log.lines) > 0 {
		dbEvents := make([]db.Event, 0, len(log.lines))
		for i, line := range log.lines {
			var e store.LogEntry
			if json.Unmarshal(line, &e) != nil {
				continue
			}
			dbEvents = append(dbEvents, logEntryToEvent(bindingID, log.startSeq+i, string(line), e))
		}
		added, err := tx.AppendEvents(bindingID, dbEvents)
		if err != nil {
			return nil, fmt.Errorf("append events: %w", err)
		}
		r.stats.Events += added
	}
	if err := tx.SaveCursor(log.cursor); err != nil {
		return nil, fmt.Errorf("save log cursor: %w", err)
	}
	return all, nil
}

// upsertRounds writes every round the binding's events and member files name, and
// returns each round number's id.
func (r *ingestRun) upsertRounds(tx *db.Tx, bindingID string, all []store.LogEntry) (map[int]string, error) {
	roundSet := map[int]bool{}
	for _, e := range all {
		if e.Round > 0 {
			roundSet[e.Round] = true
		}
	}
	for m := range r.members {
		if n, ok := roundFromMember(m); ok {
			roundSet[n] = true
		}
	}
	rounds := make([]int, 0, len(roundSet))
	for n := range roundSet {
		rounds = append(rounds, n)
	}
	sort.Ints(rounds)

	existing, err := tx.Rounds(bindingID)
	if err != nil {
		return nil, fmt.Errorf("existing rounds: %w", err)
	}
	hadRound := make(map[int]bool, len(existing))
	for _, rd := range existing {
		hadRound[rd.Number] = true
	}

	now := r.deps.Now()
	roundIDs := make(map[int]string, len(rounds))
	for _, n := range rounds {
		roundID, err := r.upsertRound(tx, bindingID, n, all, now, hadRound)
		if err != nil {
			return nil, err
		}
		roundIDs[n] = roundID
	}
	return roundIDs, nil
}

func (r *ingestRun) upsertRound(tx *db.Tx, bindingID string, n int, all []store.LogEntry, now time.Time, hadRound map[int]bool) (string, error) {
	rd := roundFacts(all, n)
	rd.BindingID = bindingID
	rd.Number = n
	rd.Outcome = deriveOutcome(all, n, r.b, r.members)
	rd.Switches = switchesForRound(all, n)

	rd.Actor = actorOf(r.b)
	rd.Account = nonEmptyPtr(r.b.BuilderAccount)
	if cand, ref, ok := candidateForRound(all, n, r.b); ok {
		candTok := cand
		rd.Candidate = &candTok
		if ref.Harness != "" {
			h := ref.Harness
			rd.Harness = &h
		}
		if ref.Provider != "" {
			p := ref.Provider
			rd.Provider = &p
		}
		if ref.Model != "" {
			m := ref.Model
			rd.Model = &m
		}
		mode := builderModeOf(r.b)
		rd.Mode = &mode
	}

	roundID, err := tx.UpsertRound(rd)
	if err != nil {
		return "", fmt.Errorf("upsert round %d: %w", n, err)
	}
	if !hadRound[n] {
		r.stats.Rounds++
	}

	if text, ok := lastAnswerPayload(all, n); ok {
		changed, err := upsertAnswerIfChanged(tx, roundID, text, now)
		if err != nil {
			return "", fmt.Errorf("artifact answer: %w", err)
		}
		if changed {
			r.stats.Artifacts++
		}
	}
	return roundID, nil
}

// appendMasterMindTranscript reads a live mastermind's own transcript past its cursor.
func (r *ingestRun) appendMasterMindTranscript(tx *db.Tx, mastermindID *string) error {
	if mastermindID == nil || r.kind != "live" || r.locator == "" {
		return nil
	}
	if _, err := os.Stat(r.locator); err != nil {
		return nil
	}

	// why: the ingest cursor's "planner::" prefix is state already written.
	key := "planner::" + r.locator
	cur, found, err := tx.Cursor(key)
	if err != nil {
		return fmt.Errorf("mastermind transcript cursor: %w", err)
	}
	opener := func() (io.ReadSeekCloser, error) { return os.Open(r.locator) }
	lines, startSeq, next, reset, err := readAppendOnly(opener, key, cur, found)
	if err != nil {
		return fmt.Errorf("read mastermind transcript: %w", err)
	}
	if reset {
		r.logger.Info("ingest: cursor reset", "binding", r.b.Name, "member", "mastermind transcript")
	}
	if len(lines) > 0 {
		recs := mastermindTranscriptRecords(r.b.MasterMind.Kind, lines, startSeq)
		added, err := tx.AppendTranscript(db.OwnerMasterMind, *mastermindID, recs)
		if err != nil {
			return fmt.Errorf("append mastermind transcript: %w", err)
		}
		r.stats.TranscriptRecords += added
	}
	if err := tx.SaveCursor(next); err != nil {
		return fmt.Errorf("save mastermind transcript cursor: %w", err)
	}
	return nil
}

// logEntryToEvent keeps entryJSON as the exact line read from the source.
func logEntryToEvent(bindingID string, seq int, entryJSON string, e store.LogEntry) db.Event {
	ev := db.Event{
		BindingID: bindingID,
		Seq:       seq,
		TS:        e.TS,
		Kind:      string(e.Kind),
		Direction: string(e.Direction),
		Note:      nonEmptyPtr(e.Note),
		Path:      nonEmptyPtr(e.Path),
		Confirmed: e.Confirmed,
		Late:      e.Late,
		EntryJSON: entryJSON,
	}
	if e.DeliveredAt != nil {
		t := *e.DeliveredAt
		ev.DeliveredAt = &t
	}
	if e.Flagged > 0 {
		f := e.Flagged
		ev.Flagged = &f
	}
	if e.FlaggedBy != "" {
		fb := e.FlaggedBy
		ev.FlaggedBy = &fb
	}
	return ev
}

// linkEventsToRounds sets round_id on every still-unlinked event whose decoded
// LogEntry.Round matches a round seen this tick, including events left unlinked by
// an earlier ingest.
func linkEventsToRounds(tx *db.Tx, bindingID string, roundIDs map[int]string) error {
	if len(roundIDs) == 0 {
		return nil
	}

	events, err := tx.Events(bindingID, 0)
	if err != nil {
		return err
	}

	seqsByRound := make(map[int][]int)
	for _, e := range events {
		if e.RoundID != nil {
			continue
		}
		var entry store.LogEntry
		if json.Unmarshal([]byte(e.EntryJSON), &entry) != nil {
			continue
		}
		if entry.Round <= 0 {
			continue
		}
		seqsByRound[entry.Round] = append(seqsByRound[entry.Round], e.Seq)
	}

	for n, seqs := range seqsByRound {
		roundID, ok := roundIDs[n]
		if !ok {
			continue
		}
		if err := tx.LinkEvents(bindingID, roundID, seqs); err != nil {
			return err
		}
	}
	return nil
}

func lastAnswerPayload(events []store.LogEntry, n int) (string, bool) {
	text, found := "", false
	for _, e := range events {
		if e.Round == n && e.Kind == store.KindAnswer {
			text, found = e.Payload, true
		}
	}
	return text, found
}

func upsertAnswerIfChanged(tx *db.Tx, roundID, text string, now time.Time) (bool, error) {
	existing, found, err := tx.Artifact(roundID, db.ArtifactAnswer)
	if err != nil {
		return false, err
	}
	if found && existing.Text == text {
		return false, nil
	}
	sha := sha256Hex([]byte(text))
	if err := tx.UpsertArtifact(db.Artifact{
		RoundID:    roundID,
		Kind:       db.ArtifactAnswer,
		Text:       text,
		Bytes:      int64(len(text)),
		SHA256:     sha,
		CapturedAt: now,
	}); err != nil {
		return false, err
	}
	return true, nil
}
