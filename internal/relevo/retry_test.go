package relevo

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestRetryPlanReadsSealed is §4.5's retry contract: RetryPlan returns the
// bytes of a plan even after the seal pass moved it out of the binding
// directory and into a round_file row. The seal is driven exactly as the
// daemon drives it (sealRounds under the state lock), so a retry keeps working
// on a round relevo no longer holds on disk.
func TestRetryPlanReadsSealed(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	rt := Runtime{Store: st, Now: func() time.Time { return baseTime }}

	// Round 3 and DONE: rounds 1 and 2 are behind the binding and it is
	// finished, so both are sealable (store.Sealable).
	b := store.Binding{
		Name: "webshop", CWD: "/repo/webshop", Round: 3, State: store.StateDone,
		MasterMindID: "pl_aaaaaaaabbbb",
	}
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	plan := []byte("# Round 1 plan\n\nDo the thing.\n")
	if err := os.WriteFile(st.PromptPath("webshop", 1), plan, 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	if err := st.WithLock(func(tx *store.Tx) error {
		sealRounds(st, tx, b, rt.Policy.ArtifactMaxBytes())
		return nil
	}); err != nil {
		t.Fatalf("sealRounds: %v", err)
	}
	if _, err := os.Stat(st.PromptPath("webshop", 1)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the plan is still on disk after the seal (err %v)", err)
	}

	got, err := RetryPlan(rt, "webshop", 1)
	if err != nil {
		t.Fatalf("RetryPlan: %v", err)
	}
	if string(got) != string(plan) {
		t.Errorf("RetryPlan = %q, want the sealed plan %q", got, plan)
	}
}

// TestRetryPlanMissingNamesTheRound: a round with no plan recorded is an
// error naming the round, not an empty plan a Send would push.
func TestRetryPlanMissingNamesTheRound(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)

	_, err := RetryPlan(rt, "webshop", 4)
	if err == nil {
		t.Fatal("RetryPlan must fail for a round with no plan")
	}
	if want := "no plan recorded for webshop round 4"; err.Error() != want {
		t.Errorf("RetryPlan error = %q, want %q", err.Error(), want)
	}
}

// TestPullMarksTuiRoute: Pull is pullPending with the cockpit's own route
// (§4.5). It returns the pending text and marks the entry delivered to "tui",
// so the daemon's own delivery and a background wait no longer claim it.
func TestPullMarksTuiRoute(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	text, found, err := delivery.Pull(context.Background(), rt.Store, "webshop", "tui")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !found || text == "" {
		t.Fatalf("Pull found=%v text=%q, want the queued payload", found, text)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := entries[len(entries)-1]
	if !last.Confirmed {
		t.Error("the entry must be confirmed")
	}
	if last.Route != "tui" {
		t.Errorf("Route = %q, want tui", last.Route)
	}
}
