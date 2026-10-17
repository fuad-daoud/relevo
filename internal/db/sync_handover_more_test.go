package db

import (
	"errors"
	"testing"
)

func TestHandOverRefusesABindingThisInstallationDoesNotWrite(t *testing.T) {
	d := openTermTestDB(t, "m1")
	err := d.Tx(func(tx *Tx) error { _, err := tx.HandOver("", "nobody", "m2", handoverAt); return err })
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("HandOver of an unknown binding = %v, want ErrInvalid", err)
	}
	err = d.Tx(func(tx *Tx) error { return tx.SetRootWriter("round", "x", "m2") })
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("SetRootWriter on a non-root = %v, want ErrInvalid", err)
	}
}

func TestOriginSchemaOnlyRises(t *testing.T) {
	d := openTermTestDB(t, "m1")
	for _, v := range []int{30, 31, 29} {
		if err := d.NoteOriginSchema("zen", v); err != nil {
			t.Fatalf("NoteOriginSchema(%d): %v", v, err)
		}
	}
	got, err := d.OriginSchemas()
	if err != nil || got["zen"] != 31 {
		t.Errorf("OriginSchemas = (%v, %v), want zen at 31", got, err)
	}
}

func TestSetRegrantMarksTheTermAndTakeoverBackKeepsIt(t *testing.T) {
	d := openTermTestDB(t, "m1")
	err := d.Tx(func(tx *Tx) error {
		if err := tx.SetTerm("r", "m2", 4); err != nil {
			return err
		}
		return tx.SetRegrant("r", "m2", "m2", 9)
	})
	if err != nil {
		t.Fatalf("regrant: %v", err)
	}
	upto, origin, seq, found, err := d.Term("r", "m2")
	if err != nil || !found || upto != 4 || origin != "m2" || seq != 9 {
		t.Errorf("term = (%d, %q, %d, %t, %v), want the fence 4 with regrant (m2, 9)", upto, origin, seq, found, err)
	}
}

// A takeover applied on the old writer records its roots as left, and on a
// third machine with no root row it only raises the fence.
func TestRecordLeaveTakeoverFlipsHeldRootsAndFencesAbsentOnes(t *testing.T) {
	d := openTermTestDB(t, "m1")
	roots, _ := seedHandoverBinding(t, d, "w")
	err := d.Tx(func(tx *Tx) error {
		for _, r := range []struct{ table, id string }{{"binding_record", roots.RecordID}, {"binding", roots.BindingID}} {
			held, err := tx.RecordLeaveTakeover(r.table, r.id, "m2", "m1", 5, handoverAt)
			if err != nil || !held {
				t.Fatalf("%s: held = %t, err = %v, want a held root", r.table, held, err)
			}
		}
		held, err := tx.RecordLeaveTakeover("binding_record", "absent", "m2", "m9", 3, handoverAt)
		if err != nil || held {
			t.Fatalf("absent root: held = %t, err = %v, want a fence only", held, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
	if got := writerOf(t, d, "binding_record", roots.RecordID); got != "m2" {
		t.Errorf("record writer = %q, want m2", got)
	}
	left, found, err := d.BindingLeftByRoot(roots.RecordID)
	if err != nil || !found || left.Kind != LeftTakeover || left.ToWriter != "m2" {
		t.Errorf("left row = (%+v, %t, %v), want a takeover to m2", left, found, err)
	}
	if upto, _, _, found, _ := d.Term("absent", "m9"); !found || upto != 3 {
		t.Errorf("fence for the absent root = (%d, %t), want 3", upto, found)
	}
	if err := d.Tx(func(tx *Tx) error { return tx.ClearBindingLeft(roots.RecordID) }); err != nil {
		t.Fatalf("ClearBindingLeft: %v", err)
	}
	if _, found, _ := d.BindingLeftByRoot(roots.RecordID); found {
		t.Error("ClearBindingLeft left the row")
	}
}

func TestRepointMirrorFencedSummaryAndOutboxPending(t *testing.T) {
	d := openTermTestDB(t, "m1")
	roots, _ := seedHandoverBinding(t, d, "w")
	var repoID string
	url := "https://example.test/acme/w"
	err := d.Tx(func(tx *Tx) error {
		var err error
		if repoID, err = tx.UpsertRepo(Repo{OriginURL: &url}); err != nil {
			return err
		}
		return tx.RepointMirror("w", repoID)
	})
	if err != nil {
		t.Fatalf("repoint: %v", err)
	}
	var got string
	if err := d.sqlDB.QueryRow(`SELECT repo_id FROM binding WHERE id = ?`, roots.BindingID).Scan(&got); err != nil || got != repoID {
		t.Errorf("mirror repo = (%q, %v), want %q", got, err, repoID)
	}

	if err := d.Tx(func(tx *Tx) error { _, err := tx.HandOver("", "w", "m2", handoverAt); return err }); err != nil {
		t.Fatalf("HandOver: %v", err)
	}
	if n, err := d.OutboxPendingFor(roots.RecordID); err != nil || n == 0 {
		t.Errorf("OutboxPendingFor = (%d, %v), want the queued flip", n, err)
	}
	if err := d.RecordFencedChange(roots.RecordID, "round_file", `["x","y"]`, handoverAt); err != nil {
		t.Fatalf("RecordFencedChange: %v", err)
	}
	fenced, err := d.FencedSummary()
	if err != nil || len(fenced) != 1 || fenced[0].Name != "w" || fenced[0].Taker != "m2" || fenced[0].Count != 1 {
		t.Errorf("FencedSummary = (%+v, %v), want one fenced change of w for m2", fenced, err)
	}
	if _, _, writer, found, err := d.ForeignRoots("", "w"); err != nil || !found || writer != "m2" {
		t.Errorf("ForeignRoots = (%q, %t, %v), want the new writer m2", writer, found, err)
	}
}

func TestGuardsAllowWritesUnderRootsThisInstallationStillWrites(t *testing.T) {
	d := openTermTestDB(t, "m1")
	roots, roundID := seedHandoverBinding(t, d, "w")
	err := d.Tx(func(tx *Tx) error {
		if err := tx.guardRoot(""); err != nil {
			return err
		}
		if err := tx.guardRound(""); err != nil {
			return err
		}
		if err := tx.guardRound("missing"); err != nil {
			return err
		}
		if err := tx.guardArtifact("missing"); err != nil {
			return err
		}
		if err := tx.guardRound(roundID); err != nil {
			return err
		}
		return tx.EventConfirm(roots.RecordID, 1, handoverAt, "r", "{}")
	})
	if err != nil {
		t.Errorf("guards refused a write under a root this installation writes: %v", err)
	}
}
