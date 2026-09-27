package relevo

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/store"
)

// fakeClaimStore is the map-backed ClaimStore the route tests use: a claim
// present for a mastermind id means live, absent means not.
type fakeClaimStore map[string]*delivery.Claim

func (f fakeClaimStore) Live(mastermind string, now time.Time) (*delivery.Claim, error) {
	return f[mastermind], nil
}

func (f fakeClaimStore) Write(c delivery.Claim, now time.Time) error {
	f[c.MasterMind] = &c
	return nil
}

func (f fakeClaimStore) Remove(mastermind string, pid int) error {
	delete(f, mastermind)
	return nil
}

// routeRuntime is a minimal Runtime for the delivery-route tests: a temp
// store, a fixed clock and nothing else wired.
func routeRuntime(t *testing.T) Runtime {
	t.Helper()
	return Runtime{
		Store: store.New(t.TempDir()),
		Now:   func() time.Time { return baseTime },
	}
}

// seedPending saves an active binding and one unconfirmed mastermind-bound
// entry, which is exactly what delivery.DeliverPending and Pull work on.
func seedPending(t *testing.T, rt Runtime, name, mastermindID, kind string) store.Binding {
	t.Helper()
	b := store.Binding{
		Name:         name,
		CWD:          "/repo/" + name,
		Round:        1,
		State:        store.StateActive,
		MasterMind:   store.Endpoint{Kind: kind, SessionID: "sess"},
		MasterMindID: mastermindID,
		Builder:      store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(b); err != nil {
			return err
		}
		return delivery.Queue(context.Background(), deliveryDeps(rt), tx, name, store.LogEntry{
			Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
			Payload: "round 1 report", Path: "/tmp/report.md",
		})
	}); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return b
}

// testClaimMasterMind is a valid mastermind id (pl_ plus 12 characters of
// [a-z2-7]), the shape the claim store keys on.
const testClaimMasterMind = "pl_aaaaaaaabbbb"

// otherClaimMasterMind is a second valid id, for the "a different mastermind's
// claim is not this one's" cases.
const otherClaimMasterMind = "pl_ccccccccdddd"

// alwaysAlive reports every pid as live, so a claim's fake pid does not
// depend on which pids exist on the test machine.
func alwaysAlive(int) bool { return true }
