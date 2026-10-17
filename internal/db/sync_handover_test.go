package db

import (
	"errors"
	"testing"
	"time"
)

var handoverAt = time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)

// seedHandoverBinding writes a live record, its history mirror and one child
// under each root, all written by origin m1.
func seedHandoverBinding(t *testing.T, d *DB, name string) (Roots, string) {
	t.Helper()
	var roots Roots
	var roundID string
	err := d.Tx(func(tx *Tx) error {
		recID, err := tx.RecordPut(Record{Owner: "", Name: name, State: "active", Round: 1, CWD: "/x", JSON: "{}", CreatedAt: handoverAt})
		if err != nil {
			return err
		}
		bID, err := tx.UpsertBinding(Binding{Name: name, CWD: "/x", BuilderMode: "headless", CreatedAt: handoverAt, IngestSource: IngestLive})
		if err != nil {
			return err
		}
		roundID, err = tx.UpsertRound(Round{BindingID: bID, Number: 1, StartedAt: handoverAt, Outcome: OutcomeOpen, Actor: "builder"})
		if err != nil {
			return err
		}
		if err := tx.EventAppend(recID, RecordEvent{Seq: 1, TS: handoverAt, Round: 1, Direction: "to_runner", Kind: "plan", JSON: "{}"}); err != nil {
			return err
		}
		roots = Roots{RecordID: recID, BindingID: bID}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return roots, roundID
}

func writerOf(t *testing.T, d *DB, table, id string) string {
	t.Helper()
	var w string
	if err := d.sqlDB.QueryRow(`SELECT writer FROM `+table+` WHERE id = ?`, id).Scan(&w); err != nil {
		t.Fatalf("read writer of %s %s: %v", table, id, err)
	}
	return w
}

// A hand-over flips both roots, records both as left, and the flip is queued
// under the writer that gave the binding away, so its exporter carries it.
func TestHandOverFlipsBothRootsAndAttributesToTheOldWriter(t *testing.T) {
	d := openTermTestDB(t, "m1")
	roots, _ := seedHandoverBinding(t, d, "w")
	if _, err := d.sqlDB.Exec(`DELETE FROM sync_outbox`); err != nil {
		t.Fatalf("clear outbox: %v", err)
	}
	if err := d.Tx(func(tx *Tx) error { _, err := tx.HandOver("", "w", "m2", handoverAt); return err }); err != nil {
		t.Fatalf("HandOver: %v", err)
	}
	for table, id := range map[string]string{"binding_record": roots.RecordID, "binding": roots.BindingID} {
		if got := writerOf(t, d, table, id); got != "m2" {
			t.Errorf("%s writer = %q, want m2", table, got)
		}
		var origin string
		if err := d.sqlDB.QueryRow(`SELECT origin FROM sync_outbox WHERE tbl = ? AND pk = ? ORDER BY seq DESC LIMIT 1`, table, `["`+id+`"]`).Scan(&origin); err != nil {
			t.Fatalf("outbox row of %s: %v", table, err)
		}
		if origin != "m1" {
			t.Errorf("%s flip is attributed to %q, want the old writer m1", table, origin)
		}
	}
	left, found, err := d.BindingLeftByName("", "w")
	if err != nil || !found || left.ToWriter != "m2" || left.Kind != LeftHandover {
		t.Fatalf("left row = (%+v, %t, %v), want handover to m2", left, found, err)
	}
	if rows, err := d.BindingsLeft(); err != nil || len(rows) != 1 {
		t.Errorf("BindingsLeft = (%v, %v), want only the record root", rows, err)
	}
	if _, ok, err := d.RecordGet("", "w"); err != nil || ok {
		t.Errorf("RecordGet after the hand-over = (%t, %v), want not in this machine's scope", ok, err)
	}
}

// Every local writer that takes a root id refuses once the root was handed
// over, because the triggers would attribute the row to the new writer and the
// exporter would drop it unseen.
func TestLocalWritersRefuseUnderLeftRoot(t *testing.T) {
	d := openTermTestDB(t, "m1")
	roots, roundID := seedHandoverBinding(t, d, "w")
	if err := d.Tx(func(tx *Tx) error { _, err := tx.HandOver("", "w", "m2", handoverAt); return err }); err != nil {
		t.Fatalf("HandOver: %v", err)
	}
	writers := map[string]func(*Tx) error{
		"EventAppend": func(tx *Tx) error {
			return tx.EventAppend(roots.RecordID, RecordEvent{Seq: 2, TS: handoverAt, Round: 1, Direction: "to_runner", Kind: "plan", JSON: "{}"})
		},
		"EventReplaceAll": func(tx *Tx) error { return tx.EventReplaceAll(roots.RecordID, nil) },
		"EventConfirm":    func(tx *Tx) error { return tx.EventConfirm(roots.RecordID, 1, handoverAt, "r", "{}") },
		"EventAdmit":      func(tx *Tx) error { return tx.EventAdmit(roots.RecordID, 1, "{}") },
		"RoundFilePut": func(tx *Tx) error {
			return tx.RoundFilePut(roots.RecordID, "001-plan.md", 1, []byte("x"), handoverAt, handoverAt)
		},
		"UpsertRound": func(tx *Tx) error {
			_, err := tx.UpsertRound(Round{BindingID: roots.BindingID, Number: 2, StartedAt: handoverAt, Outcome: OutcomeOpen, Actor: "builder"})
			return err
		},
		"AppendEvents": func(tx *Tx) error {
			_, err := tx.AppendEvents(roots.BindingID, []Event{{Seq: 1, TS: handoverAt, Kind: "k"}})
			return err
		},
		"LinkEvents": func(tx *Tx) error { return tx.LinkEvents(roots.BindingID, roundID, []int{1}) },
		"UpsertArtifact": func(tx *Tx) error {
			return tx.UpsertArtifact(Artifact{RoundID: roundID, Kind: ArtifactReport, Text: "x", CapturedAt: handoverAt})
		},
		"DeleteRoundTranscript": func(tx *Tx) error { _, err := tx.DeleteRoundTranscript(roundID); return err },
		"AppendTranscript": func(tx *Tx) error {
			_, err := tx.AppendTranscript(OwnerRound, roundID, []TranscriptRecord{{ID: "t1", OwnerKind: OwnerRound, OwnerID: roundID, Seq: 1}})
			return err
		},
		"UpsertBinding same name": func(tx *Tx) error {
			_, err := tx.UpsertBinding(Binding{Name: "w", CWD: "/y", BuilderMode: "headless", CreatedAt: handoverAt.Add(time.Hour), IngestSource: IngestLive})
			return err
		},
		"RecordPut same name": func(tx *Tx) error {
			_, err := tx.RecordPut(Record{Owner: "", Name: "w", State: "active", CWD: "/y", JSON: "{}"})
			return err
		},
	}
	for name, write := range writers {
		err := d.Tx(write)
		if !errors.Is(err, ErrWriterMoved) {
			t.Errorf("%s under a handed-over root = %v, want ErrWriterMoved", name, err)
		}
	}
}

func TestRaiseTermNeverLowersTheFence(t *testing.T) {
	d := openTermTestDB(t, "m1")
	err := d.Tx(func(tx *Tx) error {
		for _, upto := range []int{9, 4} {
			if err := tx.RaiseTerm("r", "old", upto); err != nil {
				return err
			}
		}
		got, _, _, found, err := tx.Term("r", "old")
		if err != nil || !found || got != 9 {
			t.Errorf("fence after raising 9 then 4 = (%d, %t, %v), want 9", got, found, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
}

// A takeover flips both roots to the taker, fences the old writer at the mark,
// drops the taker's own earlier fence and forgets that the taker once left.
func TestTakeOverFlipsRootsAndRaisesTheOldWritersTerm(t *testing.T) {
	d := openTermTestDB(t, "m2")
	roots, _ := seedHandoverBinding(t, d, "w")
	err := d.Tx(func(tx *Tx) error {
		for _, r := range []struct{ table, id string }{{"binding_record", roots.RecordID}, {"binding", roots.BindingID}} {
			if err := tx.SetRootWriter(r.table, r.id, "old"); err != nil {
				return err
			}
		}
		if err := tx.SetTerm(roots.RecordID, "m2", 3); err != nil {
			return err
		}
		return tx.RecordBindingLeft(BindingLeft{RootID: roots.RecordID, Owner: "", Name: "w", ToWriter: "old", Kind: LeftHandover, At: handoverAt})
	})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	err = d.Tx(func(tx *Tx) error {
		r, _, writer, found, err := tx.ForeignRoots("", "w")
		if err != nil || !found || writer != "old" {
			t.Fatalf("ForeignRoots = (%+v, %q, %t, %v), want the old writer", r, writer, found, err)
		}
		return tx.TakeOver(r, "old", 12)
	})
	if err != nil {
		t.Fatalf("TakeOver: %v", err)
	}
	for table, id := range map[string]string{"binding_record": roots.RecordID, "binding": roots.BindingID} {
		if got := writerOf(t, d, table, id); got != "m2" {
			t.Errorf("%s writer = %q, want m2", table, got)
		}
		if upto, _, _, found, _ := d.Term(id, "old"); !found || upto != 12 {
			t.Errorf("%s: old writer's term = (%d, %t), want 12", table, upto, found)
		}
	}
	if _, _, _, found, _ := d.Term(roots.RecordID, "m2"); found {
		t.Error("the taker's own term survived the takeover")
	}
	if _, found, _ := d.BindingLeftByRoot(roots.RecordID); found {
		t.Error("the taker still records the binding as left")
	}
}

// The manifest of a left binding names every child, so the reconcile walk can
// tell a row it handed over from one it deleted.
func TestHandOverKeepsTheChildrenKeysForReconcile(t *testing.T) {
	d := openTermTestDB(t, "m1")
	roots, roundID := seedHandoverBinding(t, d, "w")
	if err := d.Tx(func(tx *Tx) error { _, err := tx.HandOver("", "w", "m2", handoverAt); return err }); err != nil {
		t.Fatalf("HandOver: %v", err)
	}
	left, err := d.LeftRows()
	if err != nil {
		t.Fatalf("LeftRows: %v", err)
	}
	for _, want := range []RowRef{
		{"binding_record", `["` + roots.RecordID + `"]`},
		{"binding", `["` + roots.BindingID + `"]`},
		{"round", `["` + roundID + `"]`},
		{"binding_event", `["` + roots.RecordID + `",1]`},
	} {
		if !left[want] {
			t.Errorf("left rows lack %+v (have %v)", want, left)
		}
	}
}

func TestPendingCtlQueuesInOrderAndDropsWhatWasAppended(t *testing.T) {
	d := openTermTestDB(t, "m1")
	for _, pk := range []string{`["a"]`, `["b"]`, `["c"]`} {
		pk := pk
		if err := d.Tx(func(tx *Tx) error { return tx.QueueCtl(CtlItem{Table: "ctl:takeover", PK: pk, Body: []byte(`{}`)}) }); err != nil {
			t.Fatalf("QueueCtl: %v", err)
		}
	}
	if err := d.DropPendingCtl(2); err != nil {
		t.Fatalf("DropPendingCtl: %v", err)
	}
	got, err := d.PendingCtl()
	if err != nil || len(got) != 1 || got[0].PK != `["c"]` {
		t.Fatalf("pending = (%+v, %v), want only c", got, err)
	}
}
