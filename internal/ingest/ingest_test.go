package ingest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestIngestFixtureLive(t *testing.T) {
	dir := copyFixture(t)
	d := openTestDB(t)
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	stats, err := Ingest(context.Background(), DirSource(dir), d, Deps{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	// The fixture's log carries no answer entry, so Artifacts is 0.
	wantStats := Stats{Bindings: 1, Rounds: 3, Events: 9}
	if stats != wantStats {
		t.Errorf("Stats = %+v, want %+v", stats, wantStats)
	}

	dbStats, err := d.Stats()
	if err != nil {
		t.Fatalf("db.Stats: %v", err)
	}
	wantRows := map[string]int{"repo": 1, "mastermind": 1, "binding": 1, "round": 3, "event": 9, "artifact": 0, "transcript": 0}
	for tbl, want := range wantRows {
		if dbStats.Rows[tbl] != want {
			t.Errorf("Rows[%s] = %d, want %d", tbl, dbStats.Rows[tbl], want)
		}
	}

	b := mustBinding(t, d, "fixture")
	if !strEq(b.RepoOrigin, "https://github.com/o/r") {
		t.Errorf("RepoOrigin = %v, want https://github.com/o/r", b.RepoOrigin)
	}
	if !strEq(b.Feature, "auth") {
		t.Errorf("Feature = %v, want auth", b.Feature)
	}
	if !strEq(b.Ticket, "o/r#607") {
		t.Errorf("Ticket = %v, want o/r#607", b.Ticket)
	}
	if !strEq(b.FinalState, "needs_you") {
		t.Errorf("FinalState = %v, want needs_you", b.FinalState)
	}
	if b.IngestSource != "live" {
		t.Errorf("IngestSource = %q, want live", b.IngestSource)
	}
	if b.MasterMindID == nil {
		t.Error("MasterMindID is nil, want a mastermind row")
	}
}

func TestIngestFixtureLiveRounds(t *testing.T) {
	dir := copyFixture(t)
	d := openTestDB(t)
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	b := mustBinding(t, d, "fixture")
	rounds := mustRounds(t, d, b.ID)
	if len(rounds) != 3 {
		t.Fatalf("len(rounds) = %d, want 3", len(rounds))
	}
	if rounds[0].Outcome != db.OutcomeReported || rounds[1].Outcome != db.OutcomeReported {
		t.Errorf("rounds 1-2 outcomes = %q/%q, want reported", rounds[0].Outcome, rounds[1].Outcome)
	}
	if !strEq(rounds[1].ReportOutcome, "halted") {
		t.Errorf("round 2 report_outcome = %v, want halted", rounds[1].ReportOutcome)
	}
	if !strEq(rounds[1].Harness, "agy") || !strEq(rounds[1].Provider, "google") {
		t.Errorf("round 2 builder = %v/%v, want agy/google", rounds[1].Harness, rounds[1].Provider)
	}
	if rounds[2].Outcome != db.OutcomeExited {
		t.Errorf("round 3 outcome = %q, want exited", rounds[2].Outcome)
	}

	events, err := d.Events(b.ID, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 9 {
		t.Errorf("len(events) = %d, want 9", len(events))
	}

	// Plain round-file artifacts are not mirrored: plan/report/diff/drift are
	// absent for every round.
	assertArtifact(t, d, rounds[0].ID, db.ArtifactPrompt, false)
	assertArtifact(t, d, rounds[0].ID, db.ArtifactReport, false)
	assertArtifact(t, d, rounds[0].ID, db.ArtifactDiff, false)
	assertArtifact(t, d, rounds[0].ID, db.ArtifactGateLog, false)
	assertArtifact(t, d, rounds[1].ID, db.ArtifactPrompt, false)
	assertArtifact(t, d, rounds[1].ID, db.ArtifactReport, false)
	assertArtifact(t, d, rounds[1].ID, db.ArtifactDrift, false)
	assertArtifact(t, d, rounds[2].ID, db.ArtifactPrompt, false)
	assertArtifact(t, d, rounds[2].ID, db.ArtifactReport, false)

	for _, rd := range rounds {
		recs, err := d.Transcript(db.OwnerRound, rd.ID, 0, 0)
		if err != nil {
			t.Fatalf("Transcript round %d: %v", rd.Number, err)
		}
		if len(recs) != 0 {
			t.Errorf("round %d has %d round-owned transcript rows, want 0", rd.Number, len(recs))
		}
	}
}

func TestIngestLinksEventsToRounds(t *testing.T) {
	dir := copyFixture(t)
	d := openTestDB(t)
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	b := mustBinding(t, d, "fixture")
	round2, err := d.Events(b.ID, 2)
	if err != nil {
		t.Fatalf("Events(round 2): %v", err)
	}
	if len(round2) != 3 {
		t.Fatalf("Events(round 2) = %d rows, want 3", len(round2))
	}
	for _, e := range round2 {
		var entry store.LogEntry
		if err := json.Unmarshal([]byte(e.EntryJSON), &entry); err != nil {
			t.Fatalf("unmarshal entry_json: %v", err)
		}
		if entry.Round != 2 {
			t.Errorf("Events(round 2) returned a round %d entry (seq %d)", entry.Round, e.Seq)
		}
		if e.RoundID == nil {
			t.Errorf("seq %d: RoundID is nil, want round 2's id", e.Seq)
		}
	}

	for round, want := range map[int]int{1: 4, 3: 2} {
		rows, err := d.Events(b.ID, round)
		if err != nil {
			t.Fatalf("Events(round %d): %v", round, err)
		}
		if len(rows) != want {
			t.Errorf("Events(round %d) = %d rows, want %d", round, len(rows), want)
		}
	}
}

func TestIngestFixtureArchiveEqualsLive(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	deps := Deps{Now: func() time.Time { return now }}

	liveDB := openTestDB(t)
	if _, err := Ingest(context.Background(), DirSource(copyFixture(t)), liveDB, deps); err != nil {
		t.Fatalf("live Ingest: %v", err)
	}
	archiveStore, recordID := archiveFixtureAs(t, "bind.json")
	archiveDB := openTestDB(t)
	if _, err := Ingest(context.Background(), ArchivedSource(archiveStore, recordID), archiveDB, deps); err != nil {
		t.Fatalf("archive Ingest: %v", err)
	}

	liveB := mustBinding(t, liveDB, "fixture")
	archB := mustBinding(t, archiveDB, "fixture")
	compareBindings(t, liveB, archB)
	if archB.IngestSource != "archive" {
		t.Errorf("archive IngestSource = %q, want archive", archB.IngestSource)
	}
	if archB.ArchivedAt == nil {
		t.Error("archive ArchivedAt is nil, want the archive stamp")
	}

	liveRounds := normalizeRounds(mustRounds(t, liveDB, liveB.ID))
	archRounds := normalizeRounds(mustRounds(t, archiveDB, archB.ID))
	if len(liveRounds) != len(archRounds) {
		t.Fatalf("round count live=%d archive=%d", len(liveRounds), len(archRounds))
	}
	for i := range liveRounds {
		lr, ar := liveRounds[i], archRounds[i]
		if !roundsEqual(lr, ar) {
			t.Errorf("round %d mismatch: live=%+v archive=%+v", lr.Number, lr, ar)
		}
		compareArtifacts(t, liveDB, archiveDB, lr, ar)
		compareTranscripts(t, liveDB, archiveDB, lr, ar)
	}
	compareEvents(t, liveDB, liveB.ID, archiveDB, archB.ID)
}

func compareBindings(t *testing.T, live, arch db.BindingRow) {
	t.Helper()
	if !strPtrEq(live.RepoOrigin, arch.RepoOrigin) {
		t.Errorf("RepoOrigin live=%v archive=%v", live.RepoOrigin, arch.RepoOrigin)
	}
	if !strPtrEq(live.Feature, arch.Feature) {
		t.Errorf("Feature live=%v archive=%v", live.Feature, arch.Feature)
	}
	if !strPtrEq(live.Ticket, arch.Ticket) {
		t.Errorf("Ticket live=%v archive=%v", live.Ticket, arch.Ticket)
	}
	if !strPtrEq(live.FinalState, arch.FinalState) {
		t.Errorf("FinalState live=%v archive=%v", live.FinalState, arch.FinalState)
	}
	if live.CWD != arch.CWD || live.BuilderMode != arch.BuilderMode {
		t.Errorf("CWD/BuilderMode mismatch: live=%q/%q archive=%q/%q", live.CWD, live.BuilderMode, arch.CWD, arch.BuilderMode)
	}
	if !live.CreatedAt.Equal(arch.CreatedAt) {
		t.Errorf("CreatedAt live=%v archive=%v", live.CreatedAt, arch.CreatedAt)
	}
}

func roundsEqual(lr, ar db.Round) bool {
	return lr.Number == ar.Number && lr.Outcome == ar.Outcome &&
		strPtrEq(lr.ReportOutcome, ar.ReportOutcome) &&
		strPtrEq(lr.Harness, ar.Harness) &&
		strPtrEq(lr.Provider, ar.Provider) &&
		strPtrEq(lr.Model, ar.Model) && intPtrEq(lr.Commits, ar.Commits) &&
		strPtrEq(lr.Tree, ar.Tree) && strPtrEq(lr.GateResult, ar.GateResult) &&
		lr.Switches == ar.Switches
}

func compareArtifacts(t *testing.T, liveDB, archiveDB *db.DB, lr, ar db.Round) {
	t.Helper()
	for _, kind := range []string{db.ArtifactPrompt, db.ArtifactReport, db.ArtifactDiff, db.ArtifactDrift} {
		la, lfound, _ := liveDB.Artifact(lr.ID, kind)
		aa, afound, _ := archiveDB.Artifact(ar.ID, kind)
		if lfound != afound {
			t.Errorf("round %d artifact %s found live=%v archive=%v", lr.Number, kind, lfound, afound)
			continue
		}
		if lfound && (la.Text != aa.Text || la.SHA256 != aa.SHA256 || la.Bytes != aa.Bytes) {
			t.Errorf("round %d artifact %s content mismatch", lr.Number, kind)
		}
	}
}

func compareTranscripts(t *testing.T, liveDB, archiveDB *db.DB, lr, ar db.Round) {
	t.Helper()
	lTranscript, _ := liveDB.Transcript(db.OwnerRound, lr.ID, 0, 0)
	aTranscript, _ := archiveDB.Transcript(db.OwnerRound, ar.ID, 0, 0)
	if len(lTranscript) != len(aTranscript) {
		t.Errorf("round %d transcript count live=%d archive=%d", lr.Number, len(lTranscript), len(aTranscript))
		return
	}
	for j := range lTranscript {
		if lTranscript[j].RecordJSON != aTranscript[j].RecordJSON || lTranscript[j].Rendered != aTranscript[j].Rendered {
			t.Errorf("round %d transcript row %d mismatch", lr.Number, j)
		}
	}
}

// compareEvents compares decoded entries, not raw entry_json: the live source is a
// directory whose log.jsonl lines are kept verbatim, while an archived record's
// log.jsonl is re-encoded from its event rows.
func compareEvents(t *testing.T, liveDB *db.DB, liveID string, archiveDB *db.DB, archID string) {
	t.Helper()
	liveEvents, err := liveDB.Events(liveID, 0)
	if err != nil {
		t.Fatalf("live Events: %v", err)
	}
	archEvents, err := archiveDB.Events(archID, 0)
	if err != nil {
		t.Fatalf("archive Events: %v", err)
	}
	if len(liveEvents) != len(archEvents) {
		t.Fatalf("event count live=%d archive=%d", len(liveEvents), len(archEvents))
	}
	for i := range liveEvents {
		var le, ae store.LogEntry
		if err := json.Unmarshal([]byte(liveEvents[i].EntryJSON), &le); err != nil {
			t.Fatalf("decode live event %d: %v", i, err)
		}
		if err := json.Unmarshal([]byte(archEvents[i].EntryJSON), &ae); err != nil {
			t.Fatalf("decode archive event %d: %v", i, err)
		}
		if !logEntryEqual(le, ae) {
			t.Errorf("event %d mismatch: live=%+v archive=%+v", i, le, ae)
		}
		if liveEvents[i].Seq != archEvents[i].Seq {
			t.Errorf("event %d seq live=%d archive=%d", i, liveEvents[i].Seq, archEvents[i].Seq)
		}
	}
}

func logEntryEqual(le, ae store.LogEntry) bool {
	return le.Kind == ae.Kind && le.Round == ae.Round && le.Direction == ae.Direction &&
		le.Confirmed == ae.Confirmed && le.TS.Equal(ae.TS) && le.Path == ae.Path &&
		le.Payload == ae.Payload && le.Outcome == ae.Outcome && le.Tier == ae.Tier &&
		le.Note == ae.Note
}

func TestIngestTwiceIsNoop(t *testing.T) {
	dir := copyFixture(t)
	d := openTestDB(t)

	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	before, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	stats2, err := Ingest(context.Background(), DirSource(dir), d, Deps{})
	if err != nil {
		t.Fatalf("second Ingest: %v", err)
	}
	if stats2 != (Stats{}) {
		t.Errorf("second Ingest Stats = %+v, want zero", stats2)
	}

	after, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	for tbl, n := range before.Rows {
		if after.Rows[tbl] != n {
			t.Errorf("Rows[%s] changed: before %d, after %d", tbl, n, after.Rows[tbl])
		}
	}
}

func TestIngestAppendsAfterNewRound(t *testing.T) {
	dir := copyFixture(t)
	d := openTestDB(t)

	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}

	f, err := os.OpenFile(filepath.Join(dir, "log.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open log.jsonl: %v", err)
	}
	if _, err := f.WriteString(`{"ts":"2026-09-10T10:30:00.000Z","round":4,"direction":"to_builder","kind":"plan","path":"/work/fixture/004-plan.md","confirmed":true,"tier":"high"}` + "\n"); err != nil {
		t.Fatalf("append log entry: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close log.jsonl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "004-plan.md"), []byte("round 4 plan"), 0o644); err != nil {
		t.Fatalf("write 004-plan.md: %v", err)
	}

	stats2, err := Ingest(context.Background(), DirSource(dir), d, Deps{})
	if err != nil {
		t.Fatalf("second Ingest: %v", err)
	}
	// 004-plan.md is not mirrored into an artifact row.
	want := Stats{Bindings: 1, Rounds: 1, Events: 1}
	if stats2 != want {
		t.Errorf("second Ingest Stats = %+v, want %+v", stats2, want)
	}

	b := mustBinding(t, d, "fixture")
	rounds := mustRounds(t, d, b.ID)
	if len(rounds) != 4 {
		t.Fatalf("len(rounds) = %d, want 4", len(rounds))
	}
	if rounds[3].Number != 4 {
		t.Errorf("rounds[3].Number = %d, want 4", rounds[3].Number)
	}
}

// TestIngestNoPhantomRoundFromBindRound pins that bind.json's "round" field is the
// next round number, so it must not manufacture an empty round with no files or
// events.
func TestIngestNoPhantomRoundFromBindRound(t *testing.T) {
	dir := copyFixtureAs(t, "bind-round4.json")
	d := openTestDB(t)

	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	b := mustBinding(t, d, "fixture")
	rounds := mustRounds(t, d, b.ID)
	if len(rounds) != 3 {
		t.Fatalf("len(rounds) = %d, want 3 (bind.json's round=4 must not create a phantom round)", len(rounds))
	}
	for _, r := range rounds {
		if r.Number == 4 {
			t.Errorf("round 4 exists, want no round from bind.json's round field alone")
		}
	}
}

// TestIngestRepoFromSourceCheckoutWhenCWDGone pins the repo fallback: with no
// RepoRef and b.CWD gone, Ingest falls back to RepoFacts(b.Repo), for a live and an
// archive source alike.
func TestIngestRepoFromSourceCheckoutWhenCWDGone(t *testing.T) {
	deps := Deps{Git: mapGitFacts{
		"/work/source-checkout": {originURL: "git@github.com:o/r.git", commonDir: "/work/source-checkout/.git"},
	}}

	d := openTestDB(t)
	if _, err := Ingest(context.Background(), DirSource(copyFixtureAs(t, "bind-cwd-gone.json")), d, deps); err != nil {
		t.Fatalf("live Ingest: %v", err)
	}
	b := mustBinding(t, d, "fixture")
	if b.RepoID == nil || !strEq(b.RepoOrigin, "https://github.com/o/r") {
		t.Errorf("live: RepoID/RepoOrigin = %v/%v, want a repo row from b.Repo", b.RepoID, b.RepoOrigin)
	}

	archiveStore, recordID := archiveFixtureAs(t, "bind-cwd-gone.json")
	archiveDB := openTestDB(t)
	if _, err := Ingest(context.Background(), ArchivedSource(archiveStore, recordID), archiveDB, deps); err != nil {
		t.Fatalf("archive Ingest: %v", err)
	}
	archB := mustBinding(t, archiveDB, "fixture")
	if archB.RepoID == nil || !strEq(archB.RepoOrigin, "https://github.com/o/r") {
		t.Errorf("archive: RepoID/RepoOrigin = %v/%v, want a repo row from b.Repo", archB.RepoID, archB.RepoOrigin)
	}
}

func TestIngestLegacyBindJSON(t *testing.T) {
	d := openTestDB(t)
	if _, err := Ingest(context.Background(), DirSource(copyLegacyFixture(t)), d, Deps{}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	b := mustBinding(t, d, "fixture")
	if b.RepoID != nil {
		t.Errorf("RepoID = %v, want nil (no Git dep, no repo_ref)", b.RepoID)
	}
	if b.Feature != nil {
		t.Errorf("Feature = %v, want nil", b.Feature)
	}
	if b.Ticket != nil {
		t.Errorf("Ticket = %v, want nil", b.Ticket)
	}
	if rounds := mustRounds(t, d, b.ID); len(rounds) != 3 {
		t.Errorf("len(rounds) = %d, want 3", len(rounds))
	}
}

func TestIngestResolvesRepoWhenMissing(t *testing.T) {
	d := openTestDB(t)
	deps := Deps{Git: fakeGitFacts{originURL: "git@github.com:o/r.git", commonDir: "/work/fixture/.git"}}
	if _, err := Ingest(context.Background(), DirSource(copyLegacyFixture(t)), d, deps); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	b := mustBinding(t, d, "fixture")
	if b.RepoID == nil {
		t.Fatal("RepoID is nil, want a repo row resolved from GitFacts")
	}
	if !strEq(b.RepoOrigin, "https://github.com/o/r") {
		t.Errorf("RepoOrigin = %v, want the normalised origin", b.RepoOrigin)
	}
}

// TestIngestResolvesGitOutsideTheTransaction pins that the git facts resolve
// before Ingest opens its write transaction, so it does not hold the db's write
// lock while it shells out to git.
func TestIngestResolvesGitOutsideTheTransaction(t *testing.T) {
	d := openTestDB(t)

	git := &txProbingGitFacts{d: d}

	if _, err := Ingest(context.Background(), DirSource(copyLegacyFixture(t)), d, Deps{Git: git}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if !git.called {
		t.Errorf("called git=%v, want true", git.called)
	}
	if git.txErr != nil {
		t.Errorf("Tx inside git = %v, want nil (no write lock held)", git.txErr)
	}
}

// TestIngestIgnoresTheMasterMindTranscriptLocator pins that a locator written into
// bind.json is dead state: ingest neither opens the file, writes a transcript row,
// nor saves a cursor, and the file is left byte-for-byte alone.
func TestIngestIgnoresTheMasterMindTranscriptLocator(t *testing.T) {
	dir := copyFixture(t)

	sessionPath := filepath.Join(t.TempDir(), "planner-session.jsonl")
	content := []byte(`{"type":"assistant","message":{"content":"hi"}}` + "\n")
	if err := os.WriteFile(sessionPath, content, 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}

	bindPath := filepath.Join(dir, "bind.json")
	bindData, err := os.ReadFile(bindPath)
	if err != nil {
		t.Fatalf("read bind.json: %v", err)
	}
	// A raw rewrite keeps the key even though the store type no longer carries
	// it: this is the "old bind.json still names a locator" case.
	var doc map[string]any
	if err := json.Unmarshal(bindData, &doc); err != nil {
		t.Fatalf("unmarshal bind.json: %v", err)
	}
	planner, _ := doc["planner"].(map[string]any)
	if planner == nil {
		t.Fatal("bind.json has no planner object")
	}
	planner["transcript_locator"] = sessionPath
	rewritten, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal bind.json: %v", err)
	}
	if err := os.WriteFile(bindPath, rewritten, 0o644); err != nil {
		t.Fatalf("write bind.json: %v", err)
	}

	// The locator file is unreadable: a lingering read would fail the ingest.
	if err := os.Chmod(sessionPath, 0o000); err != nil {
		t.Fatalf("chmod session: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(sessionPath, 0o644) })

	d := openTestDB(t)
	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	dbStats, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if dbStats.Rows["transcript"] != 0 {
		t.Errorf("transcript rows = %d, want 0", dbStats.Rows["transcript"])
	}
	if _, found, err := d.Cursor("planner::" + sessionPath); err != nil || found {
		t.Errorf("Cursor(planner) = (found %v, err %v), want (false, nil)", found, err)
	}

	if err := os.Chmod(sessionPath, 0o644); err != nil {
		t.Fatalf("chmod session back: %v", err)
	}
	after, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("read session after ingest: %v", err)
	}
	if string(after) != string(content) {
		t.Errorf("session file changed: got %q, want %q", after, content)
	}
}

// TestIngestWritesNoRoundFileMirror pins that every artifact row is an answer and
// no transcript row is written at all.
func TestIngestWritesNoRoundFileMirror(t *testing.T) {
	d := openTestDB(t)
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	if _, err := Ingest(context.Background(), DirSource(copyFixture(t)), d, Deps{Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	dbStats, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	b := mustBinding(t, d, "fixture")
	rounds := mustRounds(t, d, b.ID)
	if len(rounds) == 0 {
		t.Fatal("no rounds ingested, cannot check the mirror")
	}

	nonAnswerKinds := []string{
		db.ArtifactPrompt, db.ArtifactReport, db.ArtifactDiff, db.ArtifactDrift,
		db.ArtifactGateLog, db.ArtifactQuestion, db.ArtifactAsk, db.ArtifactFindings,
	}
	answers := 0
	for _, r := range rounds {
		for _, kind := range nonAnswerKinds {
			_, found, err := d.Artifact(r.ID, kind)
			if err != nil {
				t.Fatalf("Artifact(round %d, %s): %v", r.Number, kind, err)
			}
			if found {
				t.Errorf("round %d has a %q artifact row, want only %q rows", r.Number, kind, db.ArtifactAnswer)
			}
		}
		_, found, err := d.Artifact(r.ID, db.ArtifactAnswer)
		if err != nil {
			t.Fatalf("Artifact(round %d, %s): %v", r.Number, db.ArtifactAnswer, err)
		}
		if found {
			answers++
		}
		recs, err := d.Transcript(db.OwnerRound, r.ID, 0, 0)
		if err != nil {
			t.Fatalf("Transcript(round %d): %v", r.Number, err)
		}
		if len(recs) != 0 {
			t.Errorf("round %d has %d owner_kind=%q transcript rows, want 0", r.Number, len(recs), db.OwnerRound)
		}
	}

	if dbStats.Rows["artifact"] != answers {
		t.Errorf("artifact rows = %d, want %d (one per answer artifact)", dbStats.Rows["artifact"], answers)
	}
	if dbStats.Rows["transcript"] != 0 {
		t.Errorf("transcript rows = %d, want 0", dbStats.Rows["transcript"])
	}
}

func TestIngestBadBindIsErrSourceAndWritesNothing(t *testing.T) {
	d := openTestDB(t)
	before, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	if _, err := Ingest(context.Background(), DirSource(t.TempDir()), d, Deps{}); err == nil {
		t.Fatal("Ingest over a directory with no bind.json must fail")
	}

	after, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	for tbl, n := range before.Rows {
		if after.Rows[tbl] != n {
			t.Errorf("Rows[%s] changed on a failed Ingest: before %d, after %d", tbl, n, after.Rows[tbl])
		}
	}
}

func TestStatsAdd(t *testing.T) {
	a := Stats{Bindings: 1, Rounds: 2, Events: 3, Artifacts: 4, Skipped: 6}
	b := Stats{Bindings: 1, Rounds: 1, Events: 1, Artifacts: 1, Skipped: 1}
	want := Stats{Bindings: 2, Rounds: 3, Events: 4, Artifacts: 5, Skipped: 7}
	if got := a.Add(b); got != want {
		t.Errorf("Add = %+v, want %+v", got, want)
	}
}

// TestUpsertAnswerIfChanged pins the answer artifact's write-on-change rule: a
// new text writes and reports a change, the same text is a no-op.
func TestUpsertAnswerIfChanged(t *testing.T) {
	d := openTestDB(t)
	bindingID := seedMirrorBinding(t, d, "webshop")
	roundID := seedMirrorRound(t, d, bindingID, 1)
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	if err := d.Tx(func(tx *db.Tx) error {
		changed, err := upsertAnswerIfChanged(tx, roundID, "first", now)
		if err != nil {
			return err
		}
		if !changed {
			t.Error("first write = no change, want changed")
		}
		changed, err = upsertAnswerIfChanged(tx, roundID, "first", now)
		if err != nil {
			return err
		}
		if changed {
			t.Error("same text = changed, want no change")
		}
		_, err = upsertAnswerIfChanged(tx, roundID, "second", now)
		return err
	}); err != nil {
		t.Fatalf("Tx: %v", err)
	}

	a, ok, err := d.Artifact(roundID, db.ArtifactAnswer)
	if err != nil || !ok {
		t.Fatalf("Artifact(answer) = (ok %v, err %v)", ok, err)
	}
	if a.Text != "second" || a.Bytes != int64(len("second")) || a.SHA256 != sha256Hex([]byte("second")) {
		t.Errorf("answer artifact = %+v, want the last text with its size and hash", a)
	}
}

// TestLogEntryToEventOptionalFields pins the projection of the optional log-entry
// fields onto the event row.
func TestLogEntryToEventOptionalFields(t *testing.T) {
	delivered := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	ev := logEntryToEvent("b1", 7, `{"round":1}`, store.LogEntry{
		TS: dedupeAt, Kind: store.KindPrompt, Note: "n", Path: "/p",
		Confirmed: true, Late: true, DeliveredAt: &delivered, Flagged: 3, FlaggedBy: "someone",
	})
	if ev.BindingID != "b1" || ev.Seq != 7 || ev.EntryJSON != `{"round":1}` {
		t.Errorf("identity fields = %+v", ev)
	}
	if ev.DeliveredAt == nil || !ev.DeliveredAt.Equal(delivered) {
		t.Errorf("DeliveredAt = %v, want the entry's", ev.DeliveredAt)
	}
	if ev.Flagged == nil || *ev.Flagged != 3 || ev.FlaggedBy == nil || *ev.FlaggedBy != "someone" {
		t.Errorf("flagged/flaggedBy = %v/%v, want 3/someone", ev.Flagged, ev.FlaggedBy)
	}
	if ev.Note == nil || *ev.Note != "n" || ev.Path == nil || *ev.Path != "/p" {
		t.Errorf("note/path = %v/%v, want n//p", ev.Note, ev.Path)
	}
	if !ev.Confirmed || !ev.Late {
		t.Errorf("confirmed/late = %v/%v, want true/true", ev.Confirmed, ev.Late)
	}
}

// writePickFixture writes a minimal live binding directory: bind.json for b and
// a one-entry log.jsonl whose pick note is note.
func writePickFixture(t *testing.T, b store.Binding, note string) string {
	t.Helper()
	dir := t.TempDir()

	bind, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal bind.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bind.json"), bind, 0o644); err != nil {
		t.Fatalf("write bind.json: %v", err)
	}

	entry := store.LogEntry{
		TS:        time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC),
		Round:     1,
		Direction: store.DirToMasterMind,
		Kind:      store.KindPick,
		Confirmed: true,
		Note:      note,
	}
	line, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal log entry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "log.jsonl"), append(line, '\n'), 0o644); err != nil {
		t.Fatalf("write log.jsonl: %v", err)
	}
	return dir
}

// TestIngestCountsACustomWriterActorsOwnPicks pins the bug this round fixes: a
// binding whose actor is designer owns "picked <tok> for designer: ...", so the
// round is counted on <tok> and carries the actor designer.
func TestIngestCountsACustomWriterActorsOwnPicks(t *testing.T) {
	d := openTestDB(t)
	dir := writePickFixture(t, store.Binding{
		Name:             "fixture",
		CWD:              "/work/fixture",
		Builder:          store.Endpoint{Kind: "opencode", Mode: store.ModeHeadless},
		Role:             "designer",
		BuilderCandidate: "opencode/openrouter/z-ai/glm-5.3-flash",
		CreatedAt:        time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC),
		Round:            1,
	}, "picked claude/anthropic/sonnet for designer: order #1")

	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	rounds := mustRounds(t, d, mustBinding(t, d, "fixture").ID)
	if len(rounds) != 1 {
		t.Fatalf("len(rounds) = %d, want 1", len(rounds))
	}
	if !strEq(rounds[0].Candidate, "claude/anthropic/sonnet") {
		t.Errorf("Candidate = %v, want claude/anthropic/sonnet (the actor's own pick counts)", rounds[0].Candidate)
	}
	if rounds[0].Actor != "designer" {
		t.Errorf("Actor = %q, want designer", rounds[0].Actor)
	}
}

// TestIngestSkipsAConsultPick pins that a builder binding's round does not take
// a pick note naming another actor: "picked <tok> for reviewer: ..." is skipped
// and the round falls back to the binding's own candidate.
func TestIngestSkipsAConsultPick(t *testing.T) {
	d := openTestDB(t)
	dir := writePickFixture(t, store.Binding{
		Name:             "fixture",
		CWD:              "/work/fixture",
		Builder:          store.Endpoint{Kind: "opencode", Mode: store.ModeHeadless},
		Role:             "builder",
		BuilderCandidate: "opencode/fallback/model",
		CreatedAt:        time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC),
		Round:            1,
	}, "picked claude/anthropic/sonnet for reviewer: order #1")

	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	rounds := mustRounds(t, d, mustBinding(t, d, "fixture").ID)
	if len(rounds) != 1 {
		t.Fatalf("len(rounds) = %d, want 1", len(rounds))
	}
	if !strEq(rounds[0].Candidate, "opencode/fallback/model") {
		t.Errorf("Candidate = %v, want the binding's own candidate (the consult pick is skipped)", rounds[0].Candidate)
	}
	if rounds[0].Actor != "builder" {
		t.Errorf("Actor = %q, want builder", rounds[0].Actor)
	}
}

// TestIngestWritesTheRoundAccount pins that the binding's account becomes the
// round's account, so the per-login gate and the round line up in the record.
func TestIngestWritesTheRoundAccount(t *testing.T) {
	d := openTestDB(t)
	dir := writePickFixture(t, store.Binding{
		Name:             "fixture",
		CWD:              "/work/fixture",
		Builder:          store.Endpoint{Kind: "opencode", Mode: store.ModeHeadless},
		Role:             "builder",
		BuilderCandidate: "opencode/openrouter/z-ai/glm-5.3-flash",
		BuilderAccount:   "cp2",
		CreatedAt:        time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC),
		Round:            1,
	}, "picked opencode/openrouter/z-ai/glm-5.3-flash for builder: order #1")

	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	rounds := mustRounds(t, d, mustBinding(t, d, "fixture").ID)
	if len(rounds) != 1 {
		t.Fatalf("len(rounds) = %d, want 1", len(rounds))
	}
	if !strEq(rounds[0].Account, "cp2") {
		t.Errorf("Account = %v, want cp2", rounds[0].Account)
	}
}
