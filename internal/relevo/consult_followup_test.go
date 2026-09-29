package relevo

import (
	"context"
	"os"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// reconcileOnce runs the FULL Reconcile, not consult.Reconcile. The difference
// is the point: tickConsults calls the inner function directly and so cannot
// see any gate Reconcile puts in front of it.
func reconcileOnce(t *testing.T, rt Runtime, state store.State) store.Binding {
	t.Helper()
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		b.State = state
		out, err = Reconcile(context.Background(), rt, tx, b)
		if err != nil {
			return err
		}
		return tx.Save(out)
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return out
}

// TestConsultOnADoneBindingIsStillFinished pins the gap between `relevo done`
// and a consult that is still working. Reconcile would otherwise return on
// StateDone before it ever reached the consults, leaving the record
// ConsultRunning and its findings undelivered. Consults reconcile first, so a
// DONE binding still delivers them.
func TestConsultOnADoneBindingIsStillFinished(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, c := seedHeadlessConsult(t, fr)

	stream := `{"type":"assistant","message":{"content":[{"type":"text","text":"FINDINGS BODY"}]}}` + "\n" +
		"relevo-exit:0\n"
	if err := os.WriteFile(c.Endpoint.LogPath, []byte(stream), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	fr.script(c.Endpoint.PID, false)
	fr.exit(c.Endpoint.PID, 0)

	got := reconcileOnce(t, rt, store.StateDone)

	if len(got.Consults) != 1 {
		t.Fatalf("got %d consults, want 1", len(got.Consults))
	}
	if got.Consults[0].State != store.ConsultDone {
		t.Errorf("consult landed in %q, want %q: a consult on a DONE binding must still be finished",
			got.Consults[0].State, store.ConsultDone)
	}
}
