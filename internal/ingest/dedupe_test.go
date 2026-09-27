package ingest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/legacy"
)

func TestDedupeMirrorDeletesIdenticalReportAndKeepsOneByteDifferent(t *testing.T) {
	d := openTestDB(t)
	const name = "webshop"
	bindingID := seedMirrorBinding(t, d, name)
	recordID := seedRecordAt(t, d, name, "claude", dedupeAt)

	round3 := seedMirrorRound(t, d, bindingID, 3)
	body := "003-report.md: the builder's report\n"
	putRoundFile(t, d, recordID, "003-report.md", 3, body)
	same := putArtifact(t, d, round3, db.ArtifactReport, body)

	round4 := seedMirrorRound(t, d, bindingID, 4)
	putRoundFile(t, d, recordID, "004-report.md", 4, "004-report.md: the builder's report\n")
	putArtifact(t, d, round4, db.ArtifactReport, "004-report.md: the builder's report!")

	plan := mustPlan(t, d)

	if plan.stats.MirrorBindings != 1 || plan.stats.Unmapped != 0 {
		t.Errorf("bindings/unmapped = %d/%d, want 1/0", plan.stats.MirrorBindings, plan.stats.Unmapped)
	}
	if plan.stats.ArtifactsDeleted != 1 || plan.stats.ArtifactsKept != 1 {
		t.Errorf("deleted/kept = %d/%d, want 1/1", plan.stats.ArtifactsDeleted, plan.stats.ArtifactsKept)
	}
	if len(plan.artifactIDs) != 1 || plan.artifactIDs[0] != same.ID {
		t.Errorf("artifactIDs = %v, want [%s]", plan.artifactIDs, same.ID)
	}

	// DedupeMirror is read-only: both rows are still there.
	if _, ok, err := d.Artifact(round3, db.ArtifactReport); err != nil || !ok {
		t.Errorf("Artifact(round 3 report) after planning = (ok %v, err %v), want (true, nil)", ok, err)
	}
	if _, ok, err := d.Artifact(round4, db.ArtifactReport); err != nil || !ok {
		t.Errorf("Artifact(round 4 report) after planning = (ok %v, err %v), want (true, nil)", ok, err)
	}
}

func TestDedupeMirrorKeepsAnswerArtifacts(t *testing.T) {
	d := openTestDB(t)
	const name = "webshop"
	bindingID := seedMirrorBinding(t, d, name)
	seedRecordAt(t, d, name, "claude", dedupeAt)
	round3 := seedMirrorRound(t, d, bindingID, 3)

	putArtifact(t, d, round3, db.ArtifactAnswer, "the answer\n")

	plan := mustPlan(t, d)

	if plan.stats.ArtifactsDeleted != 0 || plan.stats.ArtifactsKept != 1 {
		t.Errorf("deleted/kept = %d/%d, want 0/1", plan.stats.ArtifactsDeleted, plan.stats.ArtifactsKept)
	}
	if len(plan.artifactIDs) != 0 {
		t.Errorf("artifactIDs = %v, want none", plan.artifactIDs)
	}
	if _, ok, err := d.Artifact(round3, db.ArtifactAnswer); err != nil || !ok {
		t.Errorf("answer artifact after planning = (ok %v, err %v), want (true, nil)", ok, err)
	}
}

func TestDedupeMirrorKeepsEveryRowOfAnUnmappedBinding(t *testing.T) {
	d := openTestDB(t)
	const name = "webshop"
	bindingID := seedMirrorBinding(t, d, name)
	round3 := seedMirrorRound(t, d, bindingID, 3)

	// The record's created_at is an hour off, so nothing maps.
	recordID := seedRecordAt(t, d, name, "claude", dedupeAt.Add(time.Hour))

	body := "003-report.md: the builder's report\n"
	putRoundFile(t, d, recordID, "003-report.md", 3, body)
	putArtifact(t, d, round3, db.ArtifactReport, body)
	appendTranscript(t, d, db.OwnerRound, round3, []db.TranscriptRecord{{Seq: 0, Rendered: "a line"}})

	plan := mustPlan(t, d)

	if plan.stats.MirrorBindings != 1 || plan.stats.Unmapped != 1 {
		t.Errorf("bindings/unmapped = %d/%d, want 1/1", plan.stats.MirrorBindings, plan.stats.Unmapped)
	}
	if plan.stats.ArtifactsDeleted != 0 || len(plan.artifactIDs) != 0 || plan.stats.ArtifactsKept != 0 {
		t.Errorf("artifactIDs = %v (ArtifactsKept %d), want none", plan.artifactIDs, plan.stats.ArtifactsKept)
	}
	if plan.stats.TranscriptRoundsDeleted != 0 || len(plan.transcriptOwners) != 0 || plan.stats.TranscriptRoundsKept != 0 {
		t.Errorf("transcriptOwners = %v (kept %d), want none", plan.transcriptOwners, plan.stats.TranscriptRoundsKept)
	}
	if _, ok, err := d.Artifact(round3, db.ArtifactReport); err != nil || !ok {
		t.Errorf("artifact of an unmapped binding = (ok %v, err %v), want (true, nil)", ok, err)
	}
}

// TestDedupeMirrorPlansTranscriptByDerivationOrByStreamLines covers both transcript
// proofs: exact re-derivation, the stream-lines fallback, and a round neither
// vouches for.
func TestDedupeMirrorPlansTranscriptByDerivationOrByStreamLines(t *testing.T) {
	d := openTestDB(t)
	const name = "webshop"
	bindingID := seedMirrorBinding(t, d, name)
	recordID := seedRecordAt(t, d, name, "claude", dedupeAt)

	line1 := `{"ts":"2026-09-01T10:00:00Z","type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`
	line2 := `{"ts":"2026-09-01T10:00:01Z","type":"user","message":{"content":"go on"}}`
	raw := line1 + "\n" + line2 + "\n"
	lines := [][]byte{[]byte(line1), []byte(line2)}

	round3 := seedMirrorRound(t, d, bindingID, 3)
	putRoundFile(t, d, recordID, "003-builder.jsonl", 3, raw)
	same, skipped := streamTranscriptRecords("claude", lines, 0)
	if skipped != 0 {
		t.Fatalf("streamTranscriptRecords skipped %d lines, want 0", skipped)
	}
	appendTranscript(t, d, db.OwnerRound, round3, same)

	// Altered rendered text: exact re-derivation fails, the stream-lines proof
	// still vouches for it.
	round4 := seedMirrorRound(t, d, bindingID, 4)
	putRoundFile(t, d, recordID, "004-runner.jsonl", 4, raw)
	altered, _ := streamTranscriptRecords("claude", lines, 0)
	altered[1].Rendered = "tampered"
	appendTranscript(t, d, db.OwnerRound, round4, altered)

	// A record JSON that is no stream line: neither proof holds.
	round5 := seedMirrorRound(t, d, bindingID, 5)
	putRoundFile(t, d, recordID, "005-builder.jsonl", 5, raw)
	stranger, _ := streamTranscriptRecords("claude", lines, 0)
	stranger[1].RecordJSON = `{"x":1}`
	appendTranscript(t, d, db.OwnerRound, round5, stranger)

	plan := mustPlan(t, d)

	if plan.stats.TranscriptRoundsDeleted != 2 || plan.stats.TranscriptRowsDeleted != 4 {
		t.Errorf("rounds/rows deleted = %d/%d, want 2/4", plan.stats.TranscriptRoundsDeleted, plan.stats.TranscriptRowsDeleted)
	}
	if plan.stats.TranscriptRoundsKept != 1 || plan.stats.TranscriptRoundsByStreamLines != 1 {
		t.Errorf("kept/byStreamLines = %d/%d, want 1/1", plan.stats.TranscriptRoundsKept, plan.stats.TranscriptRoundsByStreamLines)
	}
	if plan.stats.TranscriptRowsRenamed != 0 {
		t.Errorf("TranscriptRowsRenamed = %d, want 0", plan.stats.TranscriptRowsRenamed)
	}
	wantOwners := []string{round3, round4}
	if !reflect.DeepEqual(plan.transcriptOwners, wantOwners) {
		t.Errorf("transcriptOwners = %v, want %v", plan.transcriptOwners, wantOwners)
	}
}

// TestStreamLinesCoverReadsThePreRenameStream: a round sealed under the
// pre-rename stream name still proves its rows through streamLinesCover.
func TestStreamLinesCoverReadsThePreRenameStream(t *testing.T) {
	d := openTestDB(t)
	const name = "webshop"
	seedMirrorBinding(t, d, name)
	recordID := seedRecordAt(t, d, name, "claude", dedupeAt)

	line := `{"ts":"2026-09-01T10:00:00Z","type":"assistant","message":{"content":"hi"}}`
	putRoundFile(t, d, recordID, "003-builder.jsonl", 3, line+"\n")

	record, ok, err := d.RecordGet("", name)
	if err != nil || !ok {
		t.Fatalf("RecordGet = (ok %v, err %v), want the record", ok, err)
	}
	covered, renamed, err := streamLinesCover(d, record, db.Round{Number: 3}, []db.TranscriptRecord{
		{Seq: 0, RecordJSON: line, Rendered: "hi"},
	}, nil)
	if err != nil {
		t.Fatalf("streamLinesCover: %v", err)
	}
	if !covered || renamed != 0 {
		t.Errorf("covered/renamed = %v/%d, want true/0", covered, renamed)
	}
}

func TestDedupeMirrorPlansRenamedStreamLines(t *testing.T) {
	scene := func(t *testing.T) (*db.DB, string) {
		t.Helper()
		d := openTestDB(t)
		const name = "webshop"
		bindingID := seedMirrorBinding(t, d, name)
		recordID := seedRecordAt(t, d, name, "claude", dedupeAt)
		round1 := seedMirrorRound(t, d, bindingID, 1)
		putRoundFile(t, d, recordID, "001-builder.jsonl", 1, `{"path":"/h/state/new/b/001-plan.md"}`+"\n")
		appendTranscript(t, d, db.OwnerRound, round1, []db.TranscriptRecord{
			{Seq: 0, RecordJSON: `{"path":"/h/state/old/b/001-plan.md"}`, Rendered: "the plan"},
		})
		return d, round1
	}

	t.Run("with the rename", func(t *testing.T) {
		d, round1 := scene(t)
		plan, err := DedupeMirror(d, []legacy.Prefix{{Old: "/h/state/old", New: "/h/state/new"}})
		if err != nil {
			t.Fatalf("DedupeMirror: %v", err)
		}
		if plan.stats.TranscriptRoundsDeleted != 1 || plan.stats.TranscriptRoundsByStreamLines != 1 {
			t.Errorf("stats = %+v, want 1 round deleted by stream lines", plan.stats)
		}
		if plan.stats.TranscriptRowsRenamed != 1 {
			t.Errorf("TranscriptRowsRenamed = %d, want 1", plan.stats.TranscriptRowsRenamed)
		}
		if len(plan.transcriptOwners) != 1 || plan.transcriptOwners[0] != round1 {
			t.Errorf("transcriptOwners = %v, want [%s]", plan.transcriptOwners, round1)
		}
	})

	t.Run("without the rename", func(t *testing.T) {
		d, _ := scene(t)
		plan := mustPlan(t, d)
		if plan.stats.TranscriptRoundsKept != 1 || plan.stats.TranscriptRoundsDeleted != 0 {
			t.Errorf("stats = %+v, want the round kept and nothing deleted", plan.stats)
		}
		if len(plan.transcriptOwners) != 0 {
			t.Errorf("transcriptOwners = %v, want none", plan.transcriptOwners)
		}
	})
}

// TestDedupeMirrorSealedLinesProveRowsWithNoRecordJSON covers all three sealed-lines
// cases over four rounds, each sealed with the same line1 + "\n\n" + line2 + "\n".
func TestDedupeMirrorSealedLinesProveRowsWithNoRecordJSON(t *testing.T) {
	d := openTestDB(t)
	const name = "webshop"
	bindingID := seedMirrorBinding(t, d, name)
	recordID := seedRecordAt(t, d, name, "claude", dedupeAt)

	line1 := `{"ts":"2026-09-01T10:00:00Z","type":"assistant","message":{"content":"hi"}}`
	line2 := `{"ts":"2026-09-01T10:00:01Z","type":"user","message":{"content":"go on"}}`
	stream := line1 + "\n\n" + line2 + "\n"

	// The two stream rows carry no ts, so only the sealed-lines rule can plan.
	streamRows := func(rendered string) []db.TranscriptRecord {
		return []db.TranscriptRecord{
			{Seq: 0, RecordJSON: line1, Rendered: "hello"},
			{Seq: 1, RecordJSON: "", Rendered: rendered},
			{Seq: 2, RecordJSON: line2, Rendered: "go on"},
		}
	}

	round1 := seedMirrorRound(t, d, bindingID, 1)
	putRoundFile(t, d, recordID, "001-builder.jsonl", 1, stream)
	appendTranscript(t, d, db.OwnerRound, round1, streamRows(""))

	round2 := seedMirrorRound(t, d, bindingID, 2)
	putRoundFile(t, d, recordID, "002-builder.jsonl", 2, stream)
	putRoundFile(t, d, recordID, "002-builder.log", 2, "● shell ls\n")
	appendTranscript(t, d, db.OwnerRound, round2, streamRows("● shell ls"))

	// A log holding a different line leaves the rendered row unproven.
	round3 := seedMirrorRound(t, d, bindingID, 3)
	putRoundFile(t, d, recordID, "003-builder.jsonl", 3, stream)
	putRoundFile(t, d, recordID, "003-builder.log", 3, "● shell pwd\n")
	appendTranscript(t, d, db.OwnerRound, round3, streamRows("● shell ls"))

	// No log file at all: the rendered row is unproven too.
	round4 := seedMirrorRound(t, d, bindingID, 4)
	putRoundFile(t, d, recordID, "004-builder.jsonl", 4, stream)
	appendTranscript(t, d, db.OwnerRound, round4, streamRows("● shell ls"))

	plan := mustPlan(t, d)

	if plan.stats.TranscriptRoundsDeleted != 2 || plan.stats.TranscriptRoundsKept != 2 {
		t.Errorf("deleted/kept = %d/%d, want 2/2", plan.stats.TranscriptRoundsDeleted, plan.stats.TranscriptRoundsKept)
	}
	if plan.stats.TranscriptRoundsByStreamLines != 2 {
		t.Errorf("TranscriptRoundsByStreamLines = %d, want 2", plan.stats.TranscriptRoundsByStreamLines)
	}
	wantOwners := []string{round1, round2}
	if !reflect.DeepEqual(plan.transcriptOwners, wantOwners) {
		t.Errorf("transcriptOwners = %v, want %v", plan.transcriptOwners, wantOwners)
	}
}

// TestDedupeMirrorSealedLinesWithNoStreamUseTheLog covers the log-lines proof when
// the record has no sealed stream at all.
func TestDedupeMirrorSealedLinesWithNoStreamUseTheLog(t *testing.T) {
	d := openTestDB(t)
	const name = "webshop"
	bindingID := seedMirrorBinding(t, d, name)
	recordID := seedRecordAt(t, d, name, "claude", dedupeAt)

	// Rows whose seqs differ from the log's order, so exact re-derivation cannot
	// reproduce them.
	round1 := seedMirrorRound(t, d, bindingID, 1)
	putRoundFile(t, d, recordID, "001-builder.log", 1, "a\nb\n")
	appendTranscript(t, d, db.OwnerRound, round1, []db.TranscriptRecord{
		{Seq: 5, RecordJSON: "", Rendered: "a"},
		{Seq: 6, RecordJSON: "", Rendered: "b"},
	})

	// Plus one row with a record JSON that no stream holds.
	round2 := seedMirrorRound(t, d, bindingID, 2)
	putRoundFile(t, d, recordID, "002-builder.log", 2, "a\nb\n")
	appendTranscript(t, d, db.OwnerRound, round2, []db.TranscriptRecord{
		{Seq: 5, RecordJSON: "", Rendered: "a"},
		{Seq: 6, RecordJSON: "", Rendered: "b"},
		{Seq: 7, RecordJSON: `{"k":1}`, Rendered: ""},
	})

	plan := mustPlan(t, d)

	if plan.stats.TranscriptRoundsDeleted != 1 || plan.stats.TranscriptRoundsKept != 1 {
		t.Errorf("deleted/kept = %d/%d, want 1/1", plan.stats.TranscriptRoundsDeleted, plan.stats.TranscriptRoundsKept)
	}
	if plan.stats.TranscriptRoundsByStreamLines != 1 {
		t.Errorf("TranscriptRoundsByStreamLines = %d, want 1", plan.stats.TranscriptRoundsByStreamLines)
	}
	if len(plan.transcriptOwners) != 1 || plan.transcriptOwners[0] != round1 {
		t.Errorf("transcriptOwners = %v, want [%s]", plan.transcriptOwners, round1)
	}
}

// TestDedupeMirrorAcceptsALogDerivedTranscriptWhenAStreamExists pins that the log
// source is consulted even when the record also holds a stream.
func TestDedupeMirrorAcceptsALogDerivedTranscriptWhenAStreamExists(t *testing.T) {
	d := openTestDB(t)
	const name = "webshop"
	bindingID := seedMirrorBinding(t, d, name)
	recordID := seedRecordAt(t, d, name, "claude", dedupeAt)

	streamLine := `{"ts":"2026-09-01T10:00:00Z","type":"assistant","message":{"content":"hello"}}`
	logBody := "the log's first line\n" + "the log's second line\n"
	logLines := [][]byte{[]byte("the log's first line"), []byte("the log's second line")}

	round1 := seedMirrorRound(t, d, bindingID, 1)
	putRoundFile(t, d, recordID, "001-builder.jsonl", 1, streamLine+"\n")
	putRoundFile(t, d, recordID, "001-builder.log", 1, logBody)
	fromLog := logOnlyTranscriptRecords(logLines, 0)
	appendTranscript(t, d, db.OwnerRound, round1, fromLog)

	round2 := seedMirrorRound(t, d, bindingID, 2)
	putRoundFile(t, d, recordID, "002-builder.jsonl", 2, streamLine+"\n")
	putRoundFile(t, d, recordID, "002-builder.log", 2, logBody)
	altered := logOnlyTranscriptRecords(logLines, 0)
	altered[1].Rendered = "tampered"
	appendTranscript(t, d, db.OwnerRound, round2, altered)

	plan := mustPlan(t, d)

	if plan.stats.TranscriptRoundsDeleted != 1 || plan.stats.TranscriptRoundsKept != 1 {
		t.Errorf("deleted/kept = %d/%d, want 1/1", plan.stats.TranscriptRoundsDeleted, plan.stats.TranscriptRoundsKept)
	}
	if plan.stats.TranscriptRowsDeleted != len(fromLog) {
		t.Errorf("TranscriptRowsDeleted = %d, want %d", plan.stats.TranscriptRowsDeleted, len(fromLog))
	}
	if len(plan.transcriptOwners) != 1 || plan.transcriptOwners[0] != round1 {
		t.Errorf("transcriptOwners = %v, want [%s]", plan.transcriptOwners, round1)
	}
}

func TestDedupeMirrorNeverPlansPlannerTranscript(t *testing.T) {
	d := openTestDB(t)
	const name = "webshop"
	bindingID := seedMirrorBinding(t, d, name)
	recordID := seedRecordAt(t, d, name, "claude", dedupeAt)

	line := `{"ts":"2026-09-01T10:00:00Z","type":"assistant","message":{"content":"hi"}}`
	round3 := seedMirrorRound(t, d, bindingID, 3)
	putRoundFile(t, d, recordID, "003-runner.jsonl", 3, line+"\n")
	roundRecs, _ := streamTranscriptRecords("claude", [][]byte{[]byte(line)}, 0)
	appendTranscript(t, d, db.OwnerRound, round3, roundRecs)

	appendTranscript(t, d, db.OwnerPlanner, round3, []db.TranscriptRecord{
		{Seq: 0, RecordJSON: line, Rendered: "the planner's own line"},
	})

	plan := mustPlan(t, d)

	if plan.stats.TranscriptRoundsDeleted != 1 || len(plan.transcriptOwners) != 1 || plan.transcriptOwners[0] != round3 {
		t.Fatalf("transcriptOwners = %v (rounds deleted %d), want [%s]", plan.transcriptOwners, plan.stats.TranscriptRoundsDeleted, round3)
	}

	if err := d.Tx(func(tx *db.Tx) error {
		_, derr := tx.DeleteRoundTranscript(round3)
		return derr
	}); err != nil {
		t.Fatalf("DeleteRoundTranscript: %v", err)
	}
	plannerRows, err := d.Transcript(db.OwnerPlanner, round3, 0, 0)
	if err != nil {
		t.Fatalf("Transcript(planner): %v", err)
	}
	if len(plannerRows) != 1 {
		t.Errorf("planner transcript has %d rows after the round's delete, want 1", len(plannerRows))
	}
}

func TestDedupeMirrorLeavesABindingMatchingTwoRecordsUnmapped(t *testing.T) {
	d := openTestDB(t)
	const name = "webshop"
	bindingID := seedMirrorBinding(t, d, name)
	round3 := seedMirrorRound(t, d, bindingID, 3)

	liveID := seedRecordAt(t, d, name, "claude", dedupeAt)
	archivedJSON := recordJSON(t, name, "claude")
	if err := d.Tx(func(tx *db.Tx) error {
		_, aerr := tx.RecordPutArchived(db.Record{
			Owner:     "",
			Name:      name,
			State:     "archived",
			Round:     1,
			CWD:       "/tmp/" + name,
			JSON:      archivedJSON,
			CreatedAt: dedupeAt,
			UpdatedAt: dedupeAt,
		}, dedupeAt.Add(time.Hour))
		return aerr
	}); err != nil {
		t.Fatalf("RecordPutArchived: %v", err)
	}

	body := "003-report.md: the builder's report\n"
	putRoundFile(t, d, liveID, "003-report.md", 3, body)
	putArtifact(t, d, round3, db.ArtifactReport, body)

	plan := mustPlan(t, d)

	if plan.stats.MirrorBindings != 1 || plan.stats.Unmapped != 1 {
		t.Errorf("bindings/unmapped = %d/%d, want 1/1", plan.stats.MirrorBindings, plan.stats.Unmapped)
	}
	if len(plan.artifactIDs) != 0 {
		t.Errorf("artifactIDs = %v, want none", plan.artifactIDs)
	}
}

func TestDedupeMirrorRehearsal(t *testing.T) {
	src := os.Getenv("RELEVO_DEDUPE_REHEARSAL")
	if src == "" {
		t.Skip("RELEVO_DEDUPE_REHEARSAL is not set")
	}

	dir := t.TempDir()
	dst := filepath.Join(dir, filepath.Base(src))
	copyRehearsalFile(t, src, dst)
	if _, err := os.Stat(src + "-wal"); err == nil {
		copyRehearsalFile(t, src+"-wal", dst+"-wal")
	}

	d, err := db.Open(dst)
	if err != nil {
		t.Fatalf("db.Open(%s): %v", dst, err)
	}
	defer func() { _ = d.Close() }()

	plan, err := DedupeMirror(d, rehearsalRenames(t))
	if err != nil {
		t.Fatalf("DedupeMirror: %v", err)
	}

	t.Logf("rehearsal stats: %+v", plan.stats)
	t.Logf("rehearsal: %d artifact rows, %d transcript rounds, %d transcript rows planned for deletion",
		len(plan.artifactIDs), len(plan.transcriptOwners), plan.stats.TranscriptRowsDeleted)
}

func TestDedupeMirrorOnceDeletesBacksUpAndRecordsKV(t *testing.T) {
	d := openTestDB(t)
	round3 := seedPlannedScene(t, d)
	backupDir := t.TempDir()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	stats, ran, err := DedupeMirrorOnce(d, backupDir, nil, now)
	if err != nil {
		t.Fatalf("DedupeMirrorOnce: %v", err)
	}
	if !ran {
		t.Fatal("ran = false, want true")
	}

	want := seedPlannedSceneCounts()
	want.DoneAt = now
	want.BackupPath = dedupeBackupPath(backupDir, now)
	if stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}

	// The planned rows are gone; the kept ones are not.
	if _, ok, err := d.Artifact(round3, db.ArtifactReport); err != nil || ok {
		t.Errorf("report artifact after the run = (ok %v, err %v), want (false, nil)", ok, err)
	}
	if _, ok, err := d.Artifact(round3, db.ArtifactAnswer); err != nil || !ok {
		t.Errorf("answer artifact after the run = (ok %v, err %v), want (true, nil)", ok, err)
	}
	roundRows, err := d.Transcript(db.OwnerRound, round3, 0, 0)
	if err != nil {
		t.Fatalf("Transcript(round): %v", err)
	}
	if len(roundRows) != 0 {
		t.Errorf("round transcript has %d rows after the run, want 0", len(roundRows))
	}
	plannerRows, err := d.Transcript(db.OwnerPlanner, round3, 0, 0)
	if err != nil {
		t.Fatalf("Transcript(planner): %v", err)
	}
	if len(plannerRows) != 1 {
		t.Errorf("planner transcript has %d rows after the run, want 1", len(plannerRows))
	}

	stored, ok, err := d.KVGet(dedupeKVKey)
	if err != nil || !ok {
		t.Fatalf("KVGet(%s) = (ok %v, err %v), want (true, nil)", dedupeKVKey, ok, err)
	}
	var recorded DedupeStats
	if err := json.Unmarshal(stored, &recorded); err != nil {
		t.Fatalf("kv value is not a DedupeStats: %v", err)
	}
	if !reflect.DeepEqual(recorded, stats) {
		t.Errorf("kv stats = %+v, want %+v", recorded, stats)
	}

	// The backup still holds what the run deleted, so it can be undone.
	copyDB, err := db.Open(stats.BackupPath)
	if err != nil {
		t.Fatalf("Open(backup): %v", err)
	}
	defer func() { _ = copyDB.Close() }()
	if _, ok, err := copyDB.Artifact(round3, db.ArtifactReport); err != nil || !ok {
		t.Errorf("backup report artifact = (ok %v, err %v), want (true, nil)", ok, err)
	}
	copiedRows, err := copyDB.Transcript(db.OwnerRound, round3, 0, 0)
	if err != nil {
		t.Fatalf("backup Transcript(round): %v", err)
	}
	if len(copiedRows) != 2 {
		t.Errorf("backup round transcript has %d rows, want 2", len(copiedRows))
	}
}

func TestDedupeMirrorOnceRunsOnlyOnce(t *testing.T) {
	d := openTestDB(t)
	seedPlannedScene(t, d)
	backupDir := t.TempDir()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	if _, _, err := DedupeMirrorOnce(d, backupDir, nil, now); err != nil {
		t.Fatalf("first DedupeMirrorOnce: %v", err)
	}

	before, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	stats, ran, err := DedupeMirrorOnce(d, backupDir, nil, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("second DedupeMirrorOnce: %v", err)
	}
	if ran {
		t.Error("second run ran = true, want false")
	}
	if stats != (DedupeStats{}) {
		t.Errorf("second run stats = %+v, want the zero value", stats)
	}

	after, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if !reflect.DeepEqual(before.Rows, after.Rows) {
		t.Errorf("rows after the second run = %v, want %v", after.Rows, before.Rows)
	}

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("ReadDir(backupDir): %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("backupDir holds %d files, want 1", len(entries))
	}
}

func TestDedupeMirrorOnceRunsAfterAV1Run(t *testing.T) {
	d := openTestDB(t)
	round3 := seedPlannedScene(t, d)
	if err := d.KVPut("mirror-dedupe.v1", []byte(`{"done_at":"2026-09-24T12:00:00Z"}`)); err != nil {
		t.Fatalf("KVPut(v1): %v", err)
	}
	backupDir := t.TempDir()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	stats, ran, err := DedupeMirrorOnce(d, backupDir, nil, now)
	if err != nil {
		t.Fatalf("DedupeMirrorOnce: %v", err)
	}
	if !ran {
		t.Fatal("ran = false, want true: a v1 row must not stop v2")
	}
	if stats.TranscriptRoundsDeleted != 1 {
		t.Errorf("TranscriptRoundsDeleted = %d, want 1", stats.TranscriptRoundsDeleted)
	}

	rows, err := d.Transcript(db.OwnerRound, round3, 0, 0)
	if err != nil {
		t.Fatalf("Transcript(round): %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("round transcript has %d rows after the run, want 0", len(rows))
	}

	if _, ok, err := d.KVGet("mirror-dedupe.v1"); err != nil || !ok {
		t.Errorf("KVGet(v1) after the run = (ok %v, err %v), want (true, nil)", ok, err)
	}
	if _, ok, err := d.KVGet(dedupeKVKey); err != nil || !ok {
		t.Errorf("KVGet(v2) after the run = (ok %v, err %v), want (true, nil)", ok, err)
	}
}

func TestDedupeMirrorOnceBackupFailureDeletesNothing(t *testing.T) {
	d := openTestDB(t)
	round3 := seedPlannedScene(t, d)
	backupDir := t.TempDir()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	backupPath := dedupeBackupPath(backupDir, now)
	if err := os.WriteFile(backupPath, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("pre-create the backup path: %v", err)
	}

	_, ran, err := DedupeMirrorOnce(d, backupDir, nil, now)
	if err == nil {
		t.Fatal("DedupeMirrorOnce with an unwritable backup path = nil, want an error")
	}
	if ran {
		t.Error("ran = true, want false")
	}

	if _, ok, err := d.Artifact(round3, db.ArtifactReport); err != nil || !ok {
		t.Errorf("report artifact after the failed backup = (ok %v, err %v), want (true, nil)", ok, err)
	}
	roundRows, err := d.Transcript(db.OwnerRound, round3, 0, 0)
	if err != nil {
		t.Fatalf("Transcript(round): %v", err)
	}
	if len(roundRows) != 2 {
		t.Errorf("round transcript has %d rows after the failed backup, want 2", len(roundRows))
	}
	if _, ok, err := d.KVGet(dedupeKVKey); err != nil || ok {
		t.Errorf("KVGet(%s) after the failed backup = (ok %v, err %v), want (false, nil)", dedupeKVKey, ok, err)
	}
}

func TestDedupeMirrorOnceWithNothingToDelete(t *testing.T) {
	d := openTestDB(t)
	const name = "webshop"
	bindingID := seedMirrorBinding(t, d, name)
	seedRecordAt(t, d, name, "claude", dedupeAt)
	seedMirrorRound(t, d, bindingID, 3)

	backupDir := t.TempDir()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	stats, ran, err := DedupeMirrorOnce(d, backupDir, nil, now)
	if err != nil {
		t.Fatalf("DedupeMirrorOnce: %v", err)
	}
	if !ran {
		t.Fatal("ran = false, want true")
	}
	if stats.MirrorBindings != 1 {
		t.Errorf("MirrorBindings = %d, want 1", stats.MirrorBindings)
	}
	if stats.ArtifactsDeleted != 0 || stats.TranscriptRoundsDeleted != 0 {
		t.Errorf("stats = %+v, want nothing deleted", stats)
	}
	if stats.BackupPath != "" {
		t.Errorf("BackupPath = %q, want empty: nothing was deleted", stats.BackupPath)
	}

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("ReadDir(backupDir): %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("backupDir holds %v, want no file", entries)
	}

	stored, ok, err := d.KVGet(dedupeKVKey)
	if err != nil || !ok {
		t.Fatalf("KVGet(%s) = (ok %v, err %v), want (true, nil)", dedupeKVKey, ok, err)
	}
	var recorded DedupeStats
	if err := json.Unmarshal(stored, &recorded); err != nil {
		t.Fatalf("kv value is not a DedupeStats: %v", err)
	}
	if !reflect.DeepEqual(recorded, stats) {
		t.Errorf("kv stats = %+v, want %+v", recorded, stats)
	}
}
