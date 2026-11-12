package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestOpenCheckMembers pins the running-check census: only a row with no
// result and a recorded process maps its chain's member to its pid, through
// both the store and the transaction handles. A chain with no writer resolves
// through its first member, the same rule the check log target uses.
func TestOpenCheckMembers(t *testing.T) {
	t.Parallel()

	rt, _, c := chainCheckFixture(t)
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.ChainPut(db.ChainRow{
			ID: db.NewID(), Name: "second", Status: "running", Phase: "build", Step: "building",
			Plan: 1, Plans: 1, Reviewer: "rev",
			CreatedAt: baseTime, UpdatedAt: baseTime,
		})
	}); err != nil {
		t.Fatalf("plant second chain: %v", err)
	}
	put := func(name string, run int, result string, pid int) {
		t.Helper()
		row := db.ChainCheckRow{
			Run: run, Step: "verify", Visit: 1, Command: "make check",
			Result: result, PID: pid, StartedAt: baseTime.Unix(),
		}
		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			return tx.ChainCheckPut(name, row)
		}); err != nil {
			t.Fatalf("ChainCheckPut %s/%d: %v", name, run, err)
		}
	}
	put(c.Name, 1, "", 4321)                  // live: no result, recorded process
	put(c.Name, 2, "pass", 4322)              // settled: done, not running
	put(c.Name, 3, chainCheckStarting, 0)     // never started: no process
	put("second", 1, "", 4331)                // live, writerless chain
	put("second", 2, chainCheckStopped, 4332) // stopped: settled

	want := map[string]int{"shop": 4321, "rev": 4331}
	check := func(label string, got map[string]int, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if len(got) != len(want) {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
		for name, pid := range want {
			if got[name] != pid {
				t.Errorf("%s[%q] = %d, want %d (%v)", label, name, got[name], pid, got)
			}
		}
	}
	got, err := OpenCheckMembers(rt.Store)
	check("OpenCheckMembers(store)", got, err)
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		got, err := OpenCheckMembers(tx)
		check("OpenCheckMembers(tx)", got, err)
		return nil
	}); err != nil {
		t.Fatalf("WithLock: %v", err)
	}
}
