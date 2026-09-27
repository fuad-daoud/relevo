package db

import (
	"errors"
	"testing"
	"time"
)

func TestUpsertRepoByNaturalKey(t *testing.T) {
	cases := []struct {
		name string
		repo Repo
	}{
		{"origin url", Repo{OriginURL: ptr("https://example.test/a.git")}},
		{"common dir", Repo{CommonDir: ptr("/home/x/repo/.git")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := openTestDB(t)
			r := c.repo
			r.FirstSeen = time.Now()

			id1, err := d.UpsertRepo(r)
			if err != nil {
				t.Fatalf("UpsertRepo (1st): %v", err)
			}
			id2, err := d.UpsertRepo(r)
			if err != nil {
				t.Fatalf("UpsertRepo (2nd): %v", err)
			}
			if id1 != id2 {
				t.Errorf("ids differ: %q != %q", id1, id2)
			}

			var count int
			if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM repo`).Scan(&count); err != nil {
				t.Fatalf("count repo: %v", err)
			}
			if count != 1 {
				t.Errorf("repo has %d rows, want 1", count)
			}
		})
	}
}

func TestUpsertRepoFillsMissingKey(t *testing.T) {
	d := openTestDB(t)
	origin := "https://example.test/b.git"
	commonDir := "/home/x/b/.git"

	id1, err := d.UpsertRepo(Repo{OriginURL: ptr(origin), FirstSeen: time.Now()})
	if err != nil {
		t.Fatalf("UpsertRepo (origin only): %v", err)
	}

	id2, err := d.UpsertRepo(Repo{OriginURL: ptr(origin), CommonDir: ptr(commonDir), FirstSeen: time.Now()})
	if err != nil {
		t.Fatalf("UpsertRepo (both): %v", err)
	}
	if id1 != id2 {
		t.Fatalf("ids differ: %q != %q", id1, id2)
	}

	var gotCommonDir string
	if err := d.sqlDB.QueryRow(`SELECT common_dir FROM repo WHERE id = ?`, id1).Scan(&gotCommonDir); err != nil {
		t.Fatalf("select common_dir: %v", err)
	}
	if gotCommonDir != commonDir {
		t.Errorf("common_dir = %q, want %q", gotCommonDir, commonDir)
	}
}

func TestUpsertMasterMindUpdatesLastSeen(t *testing.T) {
	d := openTestDB(t)
	t1 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)

	id1, err := d.UpsertMasterMind(MasterMind{HarnessKind: "claude", SessionID: "sess-1", FirstSeen: t1, LastSeen: t1})
	if err != nil {
		t.Fatalf("UpsertMasterMind (1st): %v", err)
	}
	id2, err := d.UpsertMasterMind(MasterMind{HarnessKind: "claude", SessionID: "sess-1", FirstSeen: t2, LastSeen: t2})
	if err != nil {
		t.Fatalf("UpsertMasterMind (2nd): %v", err)
	}
	if id1 != id2 {
		t.Fatalf("ids differ: %q != %q", id1, id2)
	}

	var gotLastSeen string
	if err := d.sqlDB.QueryRow(`SELECT last_seen FROM mastermind WHERE id = ?`, id1).Scan(&gotLastSeen); err != nil {
		t.Fatalf("select last_seen: %v", err)
	}
	if gotLastSeen != formatTime(t2) {
		t.Errorf("last_seen = %q, want %q", gotLastSeen, formatTime(t2))
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM mastermind`).Scan(&count); err != nil {
		t.Fatalf("count mastermind: %v", err)
	}
	if count != 1 {
		t.Errorf("mastermind has %d rows, want 1", count)
	}
}

func TestUpsertBindingNaturalKey(t *testing.T) {
	d := openTestDB(t)
	t1 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)

	id1, err := d.UpsertBinding(newTestBinding("webshop", t1))
	if err != nil {
		t.Fatalf("UpsertBinding (t1): %v", err)
	}
	id1Again, err := d.UpsertBinding(newTestBinding("webshop", t1))
	if err != nil {
		t.Fatalf("UpsertBinding (t1 again): %v", err)
	}
	if id1 != id1Again {
		t.Fatalf("same (name, created_at) minted a new id: %q != %q", id1, id1Again)
	}

	id2, err := d.UpsertBinding(newTestBinding("webshop", t2))
	if err != nil {
		t.Fatalf("UpsertBinding (t2): %v", err)
	}
	if id2 == id1 {
		t.Fatalf("different created_at reused id %q", id1)
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM binding WHERE name = 'webshop'`).Scan(&count); err != nil {
		t.Fatalf("count binding: %v", err)
	}
	if count != 2 {
		t.Errorf("binding has %d rows, want 2", count)
	}
}

func TestUpsertRoundReplacesColumns(t *testing.T) {
	d := openTestDB(t)
	bindingID, err := d.UpsertBinding(newTestBinding("webshop", time.Now()))
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}

	id1, err := d.UpsertRound(newTestRound(bindingID, 1, OutcomeOpen))
	if err != nil {
		t.Fatalf("UpsertRound (open): %v", err)
	}
	id2, err := d.UpsertRound(newTestRound(bindingID, 1, OutcomeReported))
	if err != nil {
		t.Fatalf("UpsertRound (reported): %v", err)
	}
	if id1 != id2 {
		t.Fatalf("ids differ: %q != %q", id1, id2)
	}

	var outcome string
	if err := d.sqlDB.QueryRow(`SELECT outcome FROM round WHERE id = ?`, id1).Scan(&outcome); err != nil {
		t.Fatalf("select outcome: %v", err)
	}
	if outcome != OutcomeReported {
		t.Errorf("outcome = %q, want %q", outcome, OutcomeReported)
	}
}

// TestUpsertRoundEmptyActorIsBuilder pins that a caller that leaves Round.Actor
// empty stores the builder actor, the column's own default, on both the insert
// and the update path.
func TestUpsertRoundEmptyActorIsBuilder(t *testing.T) {
	d := openTestDB(t)
	bindingID, err := d.UpsertBinding(newTestBinding("webshop", time.Now()))
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}

	if _, err := d.UpsertRound(newTestRound(bindingID, 1, OutcomeOpen)); err != nil {
		t.Fatalf("UpsertRound (insert): %v", err)
	}
	if _, err := d.UpsertRound(newTestRound(bindingID, 1, OutcomeReported)); err != nil {
		t.Fatalf("UpsertRound (update): %v", err)
	}

	rounds, err := d.Rounds(bindingID)
	if err != nil {
		t.Fatalf("Rounds: %v", err)
	}
	if len(rounds) != 1 {
		t.Fatalf("Rounds = %d rows, want 1", len(rounds))
	}
	if rounds[0].Actor != "builder" {
		t.Errorf("Actor = %q, want builder for an empty Round.Actor", rounds[0].Actor)
	}
}

func TestAppendEventsIgnoresKnownSeq(t *testing.T) {
	d := openTestDB(t)
	bindingID, err := d.UpsertBinding(newTestBinding("webshop", time.Now()))
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}

	mkEvent := func(seq int) Event {
		return Event{BindingID: bindingID, Seq: seq, TS: time.Now(), Kind: "send", Direction: "mastermind_to_builder", EntryJSON: "{}"}
	}

	added, err := d.AppendEvents(bindingID, []Event{mkEvent(1), mkEvent(2), mkEvent(3)})
	if err != nil {
		t.Fatalf("AppendEvents (1st): %v", err)
	}
	if added != 3 {
		t.Fatalf("added = %d, want 3", added)
	}

	added, err = d.AppendEvents(bindingID, []Event{mkEvent(1), mkEvent(2), mkEvent(3), mkEvent(4)})
	if err != nil {
		t.Fatalf("AppendEvents (2nd): %v", err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1", added)
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM event WHERE binding_id = ?`, bindingID).Scan(&count); err != nil {
		t.Fatalf("count event: %v", err)
	}
	if count != 4 {
		t.Errorf("event has %d rows, want 4", count)
	}
}

func TestLinkEventsSetsRoundID(t *testing.T) {
	d := openTestDB(t)
	bindingID, err := d.UpsertBinding(newTestBinding("webshop", time.Now()))
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}
	roundID, err := d.UpsertRound(newTestRound(bindingID, 1, OutcomeReported))
	if err != nil {
		t.Fatalf("UpsertRound: %v", err)
	}

	mkEvent := func(seq int) Event {
		return Event{BindingID: bindingID, Seq: seq, TS: time.Now(), Kind: "send", Direction: "mastermind_to_builder", EntryJSON: "{}"}
	}
	if _, err := d.AppendEvents(bindingID, []Event{mkEvent(1), mkEvent(2), mkEvent(3)}); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}

	if err := d.Tx(func(tx *Tx) error {
		return tx.LinkEvents(bindingID, roundID, []int{1, 2})
	}); err != nil {
		t.Fatalf("LinkEvents: %v", err)
	}

	evs, err := d.Events(bindingID, 1)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("Events(round 1) = %d rows, want 2", len(evs))
	}
	for _, e := range evs {
		if e.RoundID == nil || *e.RoundID != roundID {
			t.Errorf("event seq %d RoundID = %v, want %q", e.Seq, e.RoundID, roundID)
		}
	}

	// seq 3 was never linked, so its round_id must still be null.
	all, err := d.Events(bindingID, 0)
	if err != nil {
		t.Fatalf("Events(all): %v", err)
	}
	for _, e := range all {
		if e.Seq == 3 && e.RoundID != nil {
			t.Errorf("seq 3 RoundID = %v, want nil (never linked)", *e.RoundID)
		}
	}

	// A re-link for another round must not clobber an already-linked event.
	otherRoundID, err := d.UpsertRound(newTestRound(bindingID, 2, OutcomeReported))
	if err != nil {
		t.Fatalf("UpsertRound (2): %v", err)
	}
	if err := d.Tx(func(tx *Tx) error {
		return tx.LinkEvents(bindingID, otherRoundID, []int{1})
	}); err != nil {
		t.Fatalf("LinkEvents (again): %v", err)
	}
	evs, err = d.Events(bindingID, 1)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evs) != 2 {
		t.Errorf("Events(round 1) after re-link = %d rows, want 2 (still linked to round 1)", len(evs))
	}
}

func TestUpsertArtifactByKind(t *testing.T) {
	d := openTestDB(t)
	bindingID, err := d.UpsertBinding(newTestBinding("webshop", time.Now()))
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}
	roundID, err := d.UpsertRound(newTestRound(bindingID, 1, OutcomeOpen))
	if err != nil {
		t.Fatalf("UpsertRound: %v", err)
	}

	a := Artifact{RoundID: roundID, Kind: ArtifactPrompt, Text: "plan v1", Bytes: 7, SHA256: "aaa", CapturedAt: time.Now()}
	if err := d.UpsertArtifact(a); err != nil {
		t.Fatalf("UpsertArtifact (1st): %v", err)
	}
	a.Text = "plan v2"
	a.SHA256 = "bbb"
	if err := d.UpsertArtifact(a); err != nil {
		t.Fatalf("UpsertArtifact (2nd): %v", err)
	}

	got, ok, err := d.Artifact(roundID, ArtifactPrompt)
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	if !ok {
		t.Fatal("Artifact not found")
	}
	if got.Text != "plan v2" {
		t.Errorf("Text = %q, want %q", got.Text, "plan v2")
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM artifact WHERE round_id = ?`, roundID).Scan(&count); err != nil {
		t.Fatalf("count artifact: %v", err)
	}
	if count != 1 {
		t.Errorf("artifact has %d rows, want 1", count)
	}
}

func TestAppendTranscriptIgnoresKnownSeq(t *testing.T) {
	d := openTestDB(t)

	mkRecord := func(seq int) TranscriptRecord {
		return TranscriptRecord{Seq: seq, RecordJSON: "{}", Rendered: "line"}
	}

	added, err := d.AppendTranscript(OwnerMasterMind, "sess-1", []TranscriptRecord{mkRecord(0), mkRecord(1)})
	if err != nil {
		t.Fatalf("AppendTranscript (1st): %v", err)
	}
	if added != 2 {
		t.Fatalf("added = %d, want 2", added)
	}

	added, err = d.AppendTranscript(OwnerMasterMind, "sess-1", []TranscriptRecord{mkRecord(0), mkRecord(1), mkRecord(2)})
	if err != nil {
		t.Fatalf("AppendTranscript (2nd): %v", err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1", added)
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM transcript WHERE owner_id = ?`, "sess-1").Scan(&count); err != nil {
		t.Fatalf("count transcript: %v", err)
	}
	if count != 3 {
		t.Errorf("transcript has %d rows, want 3", count)
	}
}

func TestSaveCursorReplaces(t *testing.T) {
	d := openTestDB(t)

	if err := d.SaveCursor(Cursor{Source: "/x/log.jsonl", ByteOffset: 10, HeadSHA: "aaa", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveCursor (1st): %v", err)
	}
	if err := d.SaveCursor(Cursor{Source: "/x/log.jsonl", ByteOffset: 20, HeadSHA: "aaa", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveCursor (2nd): %v", err)
	}

	got, ok, err := d.Cursor("/x/log.jsonl")
	if err != nil {
		t.Fatalf("Cursor: %v", err)
	}
	if !ok {
		t.Fatal("Cursor not found")
	}
	if got.ByteOffset != 20 {
		t.Errorf("ByteOffset = %d, want 20", got.ByteOffset)
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM ingest_cursor`).Scan(&count); err != nil {
		t.Fatalf("count ingest_cursor: %v", err)
	}
	if count != 1 {
		t.Errorf("ingest_cursor has %d rows, want 1", count)
	}
}

// TestDeleteArtifactRemovesOnlyTheNamedRow pins that only the named row goes,
// and a missing id is a no-op.
func TestDeleteArtifactRemovesOnlyTheNamedRow(t *testing.T) {
	d := openTestDB(t)
	roundID := seedMirrorRow(t, d, "webshop", time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC), 3)

	report := Artifact{RoundID: roundID, Kind: ArtifactReport, Text: "report\n", Bytes: 7, SHA256: "aa", CapturedAt: time.Now()}
	if err := d.UpsertArtifact(report); err != nil {
		t.Fatalf("UpsertArtifact(report): %v", err)
	}
	plan := Artifact{RoundID: roundID, Kind: ArtifactPrompt, Text: "plan\n", Bytes: 5, SHA256: "bb", CapturedAt: time.Now()}
	if err := d.UpsertArtifact(plan); err != nil {
		t.Fatalf("UpsertArtifact(plan): %v", err)
	}

	got, ok, err := d.Artifact(roundID, ArtifactReport)
	if err != nil || !ok {
		t.Fatalf("Artifact(report) = (ok %v, err %v), want (true, nil)", ok, err)
	}

	if err := d.Tx(func(tx *Tx) error { return tx.DeleteArtifact(got.ID) }); err != nil {
		t.Fatalf("DeleteArtifact: %v", err)
	}

	if _, ok, err := d.Artifact(roundID, ArtifactReport); err != nil || ok {
		t.Errorf("Artifact(report) after delete = (ok %v, err %v), want (false, nil)", ok, err)
	}
	if _, ok, err := d.Artifact(roundID, ArtifactPrompt); err != nil || !ok {
		t.Errorf("Artifact(plan) after deleting the report = (ok %v, err %v), want (true, nil)", ok, err)
	}

	if err := d.Tx(func(tx *Tx) error { return tx.DeleteArtifact(got.ID) }); err != nil {
		t.Errorf("DeleteArtifact(a missing id): %v, want nil", err)
	}
}

// TestDeleteRoundTranscriptLeavesMasterMindRows pins that only round-owned rows
// go: a mastermind-owned row with the same owner id survives because the owner
// kind is hard-coded, never taken from the caller.
func TestDeleteRoundTranscriptLeavesMasterMindRows(t *testing.T) {
	d := openTestDB(t)
	const ownerID = "owner-shared-by-both-kinds"
	recs := []TranscriptRecord{
		{Seq: 0, Rendered: "one"},
		{Seq: 1, Rendered: "two"},
	}

	if _, err := d.AppendTranscript(OwnerRound, ownerID, recs); err != nil {
		t.Fatalf("AppendTranscript(round): %v", err)
	}
	if _, err := d.AppendTranscript(OwnerMasterMind, ownerID, recs); err != nil {
		t.Fatalf("AppendTranscript(mastermind): %v", err)
	}

	var n int64
	if err := d.Tx(func(tx *Tx) error {
		var derr error
		n, derr = tx.DeleteRoundTranscript(ownerID)
		return derr
	}); err != nil {
		t.Fatalf("DeleteRoundTranscript: %v", err)
	}
	if n != 2 {
		t.Errorf("DeleteRoundTranscript deleted %d rows, want 2", n)
	}

	roundRows, err := d.Transcript(OwnerRound, ownerID, 0, 0)
	if err != nil {
		t.Fatalf("Transcript(round): %v", err)
	}
	if len(roundRows) != 0 {
		t.Errorf("round transcript has %d rows after the delete, want 0", len(roundRows))
	}

	mastermindRows, err := d.Transcript(OwnerMasterMind, ownerID, 0, 0)
	if err != nil {
		t.Fatalf("Transcript(mastermind): %v", err)
	}
	if len(mastermindRows) != len(recs) {
		t.Errorf("mastermind transcript has %d rows, want %d (it must be untouched)", len(mastermindRows), len(recs))
	}
}

func TestWriterRejectsInvalidEnum(t *testing.T) {
	d := openTestDB(t)
	bindingID, err := d.UpsertBinding(newTestBinding("webshop", time.Now()))
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}

	_, err = d.UpsertRound(Round{BindingID: bindingID, Number: 1, StartedAt: time.Now(), Outcome: "won"})
	if err == nil {
		t.Fatal("UpsertRound with invalid Outcome: got nil error")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("UpsertRound with invalid Outcome: err = %v, want ErrInvalid", err)
	}
}
