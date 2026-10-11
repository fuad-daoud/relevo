package ingest

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// TestDedupeMirrorOnceSkipsTheReclaimOnAServedHandle pins the dedupe on the
// handle a startup pass now runs on: the rows the pass proved duplicate are
// gone, the backup was taken, and the file compaction is skipped rather than
// refused -- so the stats carry no reclaim failure, which is a failure nobody
// could act on and that a served handle makes certain.
//
// It fails if the pass records VacuumErr for a reclaim it could not have run.
func TestDedupeMirrorOnceSkipsTheReclaimOnAServedHandle(t *testing.T) {
	d := openTestDB(t)
	round3 := seedPlannedScene(t, d)
	_ = db.NewOwner(d)

	backupDir := t.TempDir()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	stats, ran, err := DedupeMirrorOnce(d, backupDir, now)
	if err != nil {
		t.Fatalf("DedupeMirrorOnce on a served handle: %v", err)
	}
	if !ran {
		t.Fatal("ran = false, want the pass to run on a served handle")
	}
	if stats.VacuumErr != "" {
		t.Errorf("the reclaim error = %q, want none: a served handle skips the reclaim rather than failing it", stats.VacuumErr)
	}
	if stats.BackupPath == "" {
		t.Error("no backup path recorded: the pass deleted rows with nothing to restore them from")
	}
	if _, ok, aerr := d.Artifact(round3, db.ArtifactReport); aerr != nil || ok {
		t.Errorf("the planned artifact = (ok %t, err %v), want it gone", ok, aerr)
	}
}

// TestDedupeMirrorOnceKeepsReadersCorrectDuringTheDelete pins the reader side
// of the pass: every planned row is deleted inside one transaction, so a reader
// running alongside it sees either the row or its absence -- never a partial one,
// and never an error it would have to classify. The handle is served, as every
// startup pass's handle now is: an unserved one would also run the in-place
// reclaim, which swaps the pool out from under the reader.
func TestDedupeMirrorOnceKeepsReadersCorrectDuringTheDelete(t *testing.T) {
	d := openTestDB(t)
	round3 := seedPlannedScene(t, d)
	_ = db.NewOwner(d)

	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			if _, _, err := d.Artifact(round3, db.ArtifactReport); err != nil {
				done <- err
				return
			}
		}
	}()

	if _, ran, err := DedupeMirrorOnce(d, t.TempDir(), time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)); err != nil || !ran {
		close(stop)
		t.Fatalf("DedupeMirrorOnce = (ran %t, err %v), want it to run", ran, err)
	}
	close(stop)
	if err := <-done; err != nil {
		t.Errorf("a reader running alongside the delete: %v", err)
	}
}
