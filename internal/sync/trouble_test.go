package sync

import (
	"fmt"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// troubleMarker reads the trouble marker the pipeline writes.
func troubleMarker(t *testing.T, kv db.KV) Trouble {
	t.Helper()
	var tr Trouble
	if err := marker(kv, KeyTrouble, &tr); err != nil {
		t.Fatalf("read the trouble marker: %v", err)
	}
	return tr
}

// TestDroppedTroubleSurvivesACleanAttemptAndRetryClearsIt pins the one trouble
// that outlives its run: a dropped batch has already had the origin's mark moved
// past it, so it is never offered again and only a reader can act on it. A
// later clean attempt must not erase it, and the operator's retry is what clears
// it. Held and gap reports are per-run and stay per-run.
func TestDroppedTroubleSurvivesACleanAttemptAndRetryClearsIt(t *testing.T) {
	_, shared, local := openSplit(t)
	runner := &Runner{Client: synclog.NewMemTransport(shared.Origin()), Local: local}

	drop := synclog.Drop{Origin: "m2", Seq: 4, Reason: "unreadable body"}.String()
	runner.record(SteadyResult{Trouble: Trouble{Dropped: []string{drop}}})

	// A later attempt that dropped nothing must carry the earlier drop forward.
	runner.record(SteadyResult{})
	got := troubleMarker(t, local)
	if len(got.Dropped) != 1 || got.Dropped[0] != drop {
		t.Fatalf("dropped after a clean attempt = %v, want the drop kept", got.Dropped)
	}

	if err := runner.Retry(); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if got := troubleMarker(t, local); len(got.Dropped) != 0 {
		t.Errorf("dropped after the retry = %v, want cleared", got.Dropped)
	}
}

// TestCapDroppedKeepsTheNewest pins the cap: a machine that drops for a long
// time keeps the newest reports rather than growing the marker without bound,
// because the newest are the ones still worth acting on.
func TestCapDroppedKeepsTheNewest(t *testing.T) {
	var list []string
	for i := 0; i < maxDroppedReports+10; i++ {
		list = append(list, fmt.Sprintf("drop-%d", i))
	}
	got := capDropped(list)
	if len(got) != maxDroppedReports {
		t.Fatalf("capDropped kept %d reports, want %d", len(got), maxDroppedReports)
	}
	if got[len(got)-1] != list[len(list)-1] {
		t.Errorf("capDropped dropped the newest report %q", got[len(got)-1])
	}
	if got[0] != list[len(list)-maxDroppedReports] {
		t.Errorf("capDropped kept %q first, want the oldest report still in the cap", got[0])
	}
}
