package relevo

// The reconcile verb as the repair for a log that kept rows this machine no
// longer holds: the children a cascaded delete left behind, which the outbox
// never offered as deletes.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/dbtest"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// reconcileVerb runs the reconcile verb on the machine, as a dry run or for real.
func (m *cfgMachine) reconcileVerb(t *testing.T, dryRun bool) *wire.SyncResult {
	t.Helper()
	runner := &VerbRunner{
		Shared: m.mdb,
		Local:  m.local,
		Path:   m.mdb.Path(),
		Runner: m.runner,
		Open:   func(context.Context) (synclog.LogTransport, error) { return m.transport(), nil },
	}
	res := runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbReconcile, DryRun: dryRun}, nil)
	if !res.OK {
		t.Fatalf("reconcile refused: %s (%s)", res.Message, res.Code)
	}
	return res
}

// strandedChildren builds the log a cascaded delete left: a binding whose events
// and sealed file reached the log, then a delete of the binding that took its
// children through the foreign key, so only the binding's own delete was sent.
// It returns the binding's id.
func strandedChildren(t *testing.T, m *cfgMachine) string {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	var id string
	if err := m.mdb.Tx(func(tx *db.Tx) error {
		var err error
		if id, err = tx.RecordPut(db.Record{Name: "alpha", State: "active", Round: 1, CWD: "/tmp/alpha",
			JSON: `{"name":"alpha"}`, CreatedAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		if err := tx.EventAppend(id, db.RecordEvent{Seq: 1, TS: now, Round: 1, Direction: "to_planner",
			Kind: "report", JSON: `{"seq":1}`}); err != nil {
			return err
		}
		return tx.RoundFilePut(id, "001-report.md", 1, []byte("report\n"), now, now)
	}); err != nil {
		t.Fatalf("seed the binding: %v", err)
	}
	m.attempt(t)

	raw := dbtest.RawOpen(t, m.mdb.Path())
	if _, err := raw.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("foreign keys: %v", err)
	}
	if _, err := raw.Exec(`DELETE FROM binding_record WHERE id = ?`, id); err != nil {
		t.Fatalf("cascaded delete: %v", err)
	}
	m.attempt(t)
	if n := len(headChildren(t, m, id)); n != 2 {
		t.Fatalf("the fixture left %d children in the log, want the event and the file", n)
	}
	return id
}

// headChildren lists the log's head rows that hang off the binding id.
func headChildren(t *testing.T, m *cfgMachine, id string) []string {
	t.Helper()
	head, err := m.transport().Head(m.mdb.Origin())
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	var out []string
	for _, h := range head {
		if (h.Table == "round_file" || h.Table == "binding_event") && strings.Contains(h.PK, id) {
			out = append(out, h.Table+" "+h.PK)
		}
	}
	return out
}

// TestReconcileVerbDeletesTheChildrenACascadeLeftInTheLog pins the repair: a
// reconcile appends a delete for every head row this machine no longer holds,
// and a second one finds the log in step.
func TestReconcileVerbDeletesTheChildrenACascadeLeftInTheLog(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newFleetMachine(t, log)
	a.unlockVerb(t, true)
	id := strandedChildren(t, a)

	res := a.reconcileVerb(t, false)
	if !res.Applied || !strings.Contains(res.Report, "2 deletes") {
		t.Errorf("reconcile = applied %t, %q; want the two deletes appended", res.Applied, res.Report)
	}
	if left := headChildren(t, a, id); len(left) != 0 {
		t.Errorf("the log still holds %v after the reconcile", left)
	}
	if again := a.reconcileVerb(t, false); again.Applied || !strings.Contains(again.Report, "in step") {
		t.Errorf("a second reconcile = applied %t, %q; want nothing to append", again.Applied, again.Report)
	}
}

// TestReconcileVerbDryRunAppendsNothing pins that the dry run lists the same
// deletes and leaves the log as it was.
func TestReconcileVerbDryRunAppendsNothing(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newFleetMachine(t, log)
	a.unlockVerb(t, true)
	id := strandedChildren(t, a)

	res := a.reconcileVerb(t, true)
	if res.Applied || !strings.Contains(res.Report, "round_file: 0 to append, 1 to delete") ||
		!strings.Contains(res.Report, "binding_event: 0 to append, 1 to delete") {
		t.Errorf("dry run = applied %t, %q; want both deletes listed", res.Applied, res.Report)
	}
	if left := headChildren(t, a, id); len(left) != 2 {
		t.Errorf("the dry run changed the log: %d children left, want 2", len(left))
	}
}
