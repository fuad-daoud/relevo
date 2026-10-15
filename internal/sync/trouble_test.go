package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// TestTroubleFromFoldsEveryHoldKindIntoTheHoldList pins that a locked or
// boundary hold (a Reason), a newer-schema hold, a stalled origin and a gap all
// reach the list a pull prints, while a drop stays out of it.
func TestTroubleFromFoldsEveryHoldKindIntoTheHoldList(t *testing.T) {
	res := synclog.ImportResult{
		Held: []synclog.Hold{
			{Origin: "m2", Label: "m2", Seq: 1, Schema: 9},
			{Origin: "m3", Reason: "m3 stopped at a signed entry: run relevo db sync unlock"},
			{Origin: "m4", Reason: "m4 stopped at a boundary"},
		},
		Stalled: []synclog.Stall{{Origin: "m5"}},
		Gaps:    []synclog.Gap{{Origin: "m6", Want: 3, Got: 5}},
		Dropped: []synclog.Drop{{Origin: "m7", Seq: 2, Reason: "unreadable"}},
	}
	holds := TroubleFrom(res).Holds()
	if len(holds) != 5 {
		t.Fatalf("holds = %v, want three held, one stalled and one gap", holds)
	}
	for _, want := range []string{"upgrade", "unlock", "boundary", "m6 is held at 2"} {
		found := false
		for _, h := range holds {
			found = found || strings.Contains(h, want)
		}
		if !found {
			t.Errorf("holds = %v, want one naming %q", holds, want)
		}
	}
}

// TestReplicaFilesIsTheSetDisableDeletes pins that the files a start tightens
// and the files the turn-off removes are one set: the replica, its driver files
// and nothing else.
func TestReplicaFilesIsTheSetDisableDeletes(t *testing.T) {
	dir := t.TempDir()
	replica := filepath.Join(dir, "relevo-sync.db")
	for _, suffix := range []string{"", "-wal", "-wal-revert", "-sibling"} {
		if err := os.WriteFile(replica+suffix, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(dir, "relevo.db")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	listed, err := ReplicaFiles(replica)
	if err != nil || len(listed) != 4 {
		t.Fatalf("ReplicaFiles = %v (%v), want the four replica files", listed, err)
	}
	deleted, err := deleteReplica(replica)
	if err != nil || !reflect.DeepEqual(listed, deleted) {
		t.Fatalf("deleteReplica = %v (%v), want %v", deleted, err, listed)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("an unrelated file was removed: %v", err)
	}
}
