package ingest

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// splitPair opens a split pair whose split pass has already finished, as every
// daemon's pair has: a later pass that finds a machine-local row in the shared
// file moves it and takes a full backup first.
func splitPair(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: "01ORIGIN"})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, _, err := db.SplitOnce(d, t.TempDir(), time.Now()); err != nil {
		t.Fatalf("first SplitOnce: %v", err)
	}
	return d
}

func TestIngestLeavesTheSplitNothingToMove(t *testing.T) {
	d := splitPair(t)
	if _, err := Ingest(context.Background(), DirSource(copyFixture(t)), d, Deps{}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	stats, ran, err := db.SplitOnce(d, t.TempDir(), time.Now())
	if err != nil {
		t.Fatalf("second SplitOnce: %v", err)
	}
	if ran {
		t.Errorf("the split pass moved %d rows out of the shared file after an ingest, want none", stats.RowsMoved)
	}
}

func TestIngestResumesFromTheCursorInTheLocalFile(t *testing.T) {
	d := splitPair(t)
	dir := copyFixture(t)
	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	local, err := d.Local().Stats()
	if err != nil {
		t.Fatalf("local Stats: %v", err)
	}
	if local.Rows["ingest_cursor"] == 0 {
		t.Fatal("the ingest saved no cursor in the local file")
	}
	again, err := Ingest(context.Background(), DirSource(dir), d, Deps{})
	if err != nil {
		t.Fatalf("second Ingest: %v", err)
	}
	if again != (Stats{}) {
		t.Errorf("second Ingest Stats = %+v, want zero", again)
	}
}

// A cursor not saved after the commit -- the process died in between --
// re-reads lines whose events are already in, and must add none of them twice.
func TestIngestWithoutItsCursorAddsNoEventTwice(t *testing.T) {
	d := splitPair(t)
	dir := copyFixture(t)
	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	before, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	_, key := sourceOpener(DirSource(dir), "log.jsonl")
	if err := d.SaveCursor(db.Cursor{Source: key}); err != nil {
		t.Fatalf("rewind the cursor: %v", err)
	}
	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("second Ingest: %v", err)
	}
	after, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if after.Rows["event"] != before.Rows["event"] {
		t.Errorf("event rows = %d after a replay, want %d", after.Rows["event"], before.Rows["event"])
	}
}

// The first start after the cursor moved to the local file can resume from an
// older cursor than the last ingest saved: the split pass keeps a local row
// over a newer shared stray. The lines re-read from there are already in, and
// must change no round.
func TestIngestFromAnOlderCursorChangesNoRound(t *testing.T) {
	d := splitPair(t)
	dir := copyFixture(t)
	logPath := filepath.Join(dir, "log.jsonl")
	full, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := bytes.SplitAfter(full, []byte("\n"))
	if err := os.WriteFile(logPath, bytes.Join(lines[:len(lines)/2], nil), 0o644); err != nil {
		t.Fatalf("truncate log: %v", err)
	}
	src := DirSource(dir)
	if _, err := Ingest(context.Background(), src, d, Deps{}); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	_, key := sourceOpener(src, "log.jsonl")
	older, found, err := d.Cursor(key)
	if err != nil || !found {
		t.Fatalf("Cursor after the first ingest = found %v, err %v", found, err)
	}
	if err := os.WriteFile(logPath, full, 0o644); err != nil {
		t.Fatalf("restore log: %v", err)
	}
	if _, err := Ingest(context.Background(), src, d, Deps{}); err != nil {
		t.Fatalf("second Ingest: %v", err)
	}
	b, err := src.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	row, ok, err := d.Binding(b.Name)
	if err != nil || !ok {
		t.Fatalf("Binding(%s) = found %v, err %v", b.Name, ok, err)
	}
	want, err := d.Rounds(row.ID)
	if err != nil {
		t.Fatalf("Rounds: %v", err)
	}
	wantStats, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	if err := d.SaveCursor(older); err != nil {
		t.Fatalf("restore the older cursor: %v", err)
	}
	if _, err := Ingest(context.Background(), src, d, Deps{}); err != nil {
		t.Fatalf("Ingest from the older cursor: %v", err)
	}
	got, err := d.Rounds(row.ID)
	if err != nil {
		t.Fatalf("Rounds: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rounds after a replay from an older cursor:\n got %+v\nwant %+v", got, want)
	}
	gotStats, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	for tbl, n := range wantStats.Rows {
		if gotStats.Rows[tbl] != n {
			t.Errorf("Rows[%s] = %d after a replay, want %d", tbl, gotStats.Rows[tbl], n)
		}
	}
}

// A binding re-ingested with nothing appended -- every daemon start re-ingests
// each live binding -- must rebuild its rounds from the events already in,
// not from the empty read: a round rebuilt from no events loses its start,
// its close, its outcome and its cost.
func TestIngestWithNothingNewKeepsEveryRound(t *testing.T) {
	d := splitPair(t)
	src := DirSource(copyFixture(t))
	if _, err := Ingest(context.Background(), src, d, Deps{}); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	b, err := src.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	row, ok, err := d.Binding(b.Name)
	if err != nil || !ok {
		t.Fatalf("Binding(%s) = found %v, err %v", b.Name, ok, err)
	}
	want, err := d.Rounds(row.ID)
	if err != nil || len(want) == 0 {
		t.Fatalf("Rounds after the first ingest = %d rounds, err %v; want some", len(want), err)
	}

	if _, err := Ingest(context.Background(), src, d, Deps{}); err != nil {
		t.Fatalf("second Ingest: %v", err)
	}
	got, err := d.Rounds(row.ID)
	if err != nil {
		t.Fatalf("Rounds: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rounds after an ingest with nothing new =\n%+v\nwant\n%+v", got, want)
	}
}
