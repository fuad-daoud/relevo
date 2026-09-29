package db

import (
	"testing"
	"time"
)

func TestRevisionInsertAndRead(t *testing.T) {
	d := openTestDB(t)
	now := time.Now().UTC().Truncate(time.Millisecond)

	var revs []int64
	for i, source := range []string{"cli", "rollback"} {
		revs = append(revs, insertRevision(t, d, i, source, now))
	}
	if revs[0] != 1 || revs[1] != 2 {
		t.Fatalf("revs = %v, want [1 2]", revs)
	}

	rows, err := d.Revisions(0)
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("Revisions(0) = %d rows, want 2", len(rows))
	}
	if rows[0].Rev != 2 || rows[1].Rev != 1 {
		t.Errorf("Revisions newest first = [%d %d], want [2 1]", rows[0].Rev, rows[1].Rev)
	}
	if rows[0].Snapshot != nil {
		t.Errorf("Revisions left Snapshot = %q, want nil", rows[0].Snapshot)
	}
	if rows[0].Source != "rollback" || rows[0].Message != "message rollback" || rows[0].Version != 2 {
		t.Errorf("rows[0] = %+v, want rollback / message rollback / version 2", rows[0])
	}
	if !rows[0].At.Equal(now) {
		t.Errorf("rows[0].At = %v, want %v", rows[0].At, now)
	}
	if string(rows[0].Changes) != `[{"path":"policy.x","op":"add","after":1}]` {
		t.Errorf("rows[0].Changes = %q", rows[0].Changes)
	}

	one, err := d.Revisions(1)
	if err != nil {
		t.Fatalf("Revisions(1): %v", err)
	}
	if len(one) != 1 || one[0].Rev != 2 {
		t.Errorf("Revisions(1) = %v, want just rev 2", one)
	}

	got, ok, err := d.Revision(1)
	if err != nil || !ok {
		t.Fatalf("Revision(1) = (_, %v, %v), want present", ok, err)
	}
	if string(got.Snapshot) != `{"policy":{}}` {
		t.Errorf("Revision(1).Snapshot = %q, want the stored object", got.Snapshot)
	}
	if got.Source != "cli" {
		t.Errorf("Revision(1).Source = %q, want cli", got.Source)
	}

	if _, ok, err := d.Revision(99); err != nil || ok {
		t.Errorf("Revision(99) = (_, %v, %v), want absent", ok, err)
	}
}

func insertRevision(t *testing.T, d *DB, i int, source string, now time.Time) int64 {
	t.Helper()
	var rev int64
	err := d.Tx(func(tx *Tx) error {
		exists, err := tx.RevisionsExist()
		if err != nil {
			return err
		}
		if want := i > 0; exists != want {
			t.Errorf("RevisionsExist before insert %d = %v, want %v", i+1, exists, want)
		}
		rev, err = tx.RevisionInsert(RevisionRow{
			At:       now,
			Source:   source,
			Message:  "message " + source,
			Version:  int64(i + 1),
			Changes:  []byte(`[{"path":"policy.x","op":"add","after":1}]`),
			Snapshot: []byte(`{"policy":{}}`),
		})
		return err
	})
	if err != nil {
		t.Fatalf("Tx %d: %v", i+1, err)
	}
	return rev
}
