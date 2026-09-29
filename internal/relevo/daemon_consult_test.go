package relevo

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/consult"
	"github.com/fuad-daoud/relevo/internal/store"
)

// A finished consult must be persisted by the daemon's own Tick, not only by
// a caller that saves unconditionally. Found on the first real consult (#63):
// finishConsult mutates the Consults slice in place, so the pre- and
// post-reconcile bindings share the change and the tick's SameBinding gate
// skips the save -- and the next tick queues the findings entry again.
func TestTickPersistsFinishedConsultOnce(t *testing.T) {
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

	d := NewDaemon(rt, time.Second)
	for i := 0; i < 3; i++ {
		if err := d.Tick(context.Background()); err != nil {
			t.Fatalf("Tick %d: %v", i, err)
		}
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := b.Consults[0].State; got != store.ConsultDone {
		t.Errorf("consult state on disk = %q, want %q", got, store.ConsultDone)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.Kind == store.KindFindings {
			n++
		}
	}
	if n != 1 {
		t.Errorf("findings entries logged across 3 ticks = %d, want 1", n)
	}
}

func TestTickPersistsAnExpiredReservation(t *testing.T) {
	t.Parallel()

	rt, clock := seedSpawning(t)
	clock.Advance(consult.SpawnTimeout + time.Second)

	d := NewDaemon(rt, time.Second)
	for i := 0; i < 2; i++ {
		if err := d.Tick(context.Background()); err != nil {
			t.Fatalf("Tick %d: %v", i, err)
		}
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := b.Consults[0].State; got != store.ConsultSilent {
		t.Errorf("consult state on disk = %q, want %q", got, store.ConsultSilent)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.Kind == store.KindFindings {
			n++
		}
	}
	if n != 1 {
		t.Errorf("findings entries logged across 2 ticks = %d, want 1", n)
	}
}
