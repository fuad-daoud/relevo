package serve

import (
	"net/http"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// recordHeadGate writes a head-ledger gate without fanning out, so the test
// drives the resync pass alone.
func recordHeadGate(t *testing.T, env *testEnv, token string, until time.Time, reason string) {
	t.Helper()
	if _, err := availability.Unavailable(relevo.AvailabilityDeps(ledgerRuntime(env.srv)), token, until, reason); err != nil {
		t.Fatalf("record head gate: %v", err)
	}
}

// setRowVerified flips a table row's verified flag under the lock.
func setRowVerified(env *testEnv, id string, verified bool) {
	env.srv.mu.Lock()
	defer env.srv.mu.Unlock()
	if row := burstTableRow(env.srv.burstTable, id); row != nil {
		row.Verified = verified
	}
}

// resyncEnv is a head with one scripted node and a joined gate fan-out.
func resyncEnv(t *testing.T, script *nodeScript, clock *time.Time) (*testEnv, *hookJoin) {
	t.Helper()
	env, _ := gateFanoutEnv(t, []*nodeScript{script}, clock)
	join := &hookJoin{}
	env.srv.gateFanoutHook = join.hook
	return env, join
}

const resyncToken = "claude/anthropic/haiku"

// TestResyncDeliversToLateVerifiedNode pins the pass: a gate recorded while
// its node was unverified reaches that node on the next pass after it
// verifies, with the head's Until and no new traffic, and a further pass
// sends nothing.
func TestResyncDeliversToLateVerifiedNode(t *testing.T) {
	clock := time.Now().UTC()
	script := &nodeScript{}
	env, join := resyncEnv(t, script, &clock)
	setRowVerified(env, "b1", false)

	until := clock.Add(2 * time.Hour)
	recordHeadGate(t, env, resyncToken, until, "rate limited test")

	env.srv.resyncGates(clock)
	if got := script.unavailableHits.Load(); got != 0 {
		t.Fatalf("unverified node hits = %d, want 0: the pass sends only to verified nodes", got)
	}

	setRowVerified(env, "b1", true)
	env.srv.resyncGates(clock)
	tickUntil(t, func() bool { return script.unavailableHits.Load() == 1 }, "the verified node never saw the gate")
	tickUntil(t, join.fired, "the resync fan-out never landed")
	if err := join.first(); err != nil {
		t.Fatalf("resync fan-out err = %v, want the gate landed", err)
	}
	seen := script.gateTokens()
	if len(seen) != 1 || !seen[0].until.Equal(until) {
		t.Fatalf("node gates = %+v, want the one gate with the head's Until", seen)
	}

	env.srv.resyncGates(clock)
	if got := script.unavailableHits.Load(); got != 1 {
		t.Fatalf("node hits after a heard pass = %d, want still 1", got)
	}
}

// TestResyncSkipsClearedGate pins that a clear stops the resync: after the
// head clears the gate its heard row is forgotten and no further unavailable
// reaches the node.
func TestResyncSkipsClearedGate(t *testing.T) {
	clock := time.Now().UTC()
	script := &nodeScript{}
	env, join := resyncEnv(t, script, &clock)

	recordHeadGate(t, env, resyncToken, clock.Add(2*time.Hour), "rate limited test")
	env.srv.resyncGates(clock)
	tickUntil(t, func() bool { return script.unavailableHits.Load() == 1 }, "the node never saw the gate")
	tickUntil(t, join.fired, "the resync fan-out never landed")

	if _, _, err := AdminAvailable(env.srv, "anthropic"); err != nil {
		t.Fatalf("AdminAvailable: %v", err)
	}
	env.srv.resyncGates(clock)
	if got := script.unavailableHits.Load(); got != 1 {
		t.Fatalf("node unavailable hits after the clear = %d, want still 1", got)
	}
	if env.srv.gateHeardBy("b1", "anthropic", clock.Add(2*time.Hour)) {
		t.Error("heard still holds the cleared gate, want it forgotten")
	}
}

// TestResyncSkipsExpiredGate pins that an expired gate is never sent: a
// gate whose Until has passed is absent from the live set, so the pass
// forgets it instead of transmitting it.
func TestResyncSkipsExpiredGate(t *testing.T) {
	clock := time.Now().UTC()
	script := &nodeScript{}
	env, _ := resyncEnv(t, script, &clock)

	recordHeadGate(t, env, resyncToken, clock.Add(-time.Minute), "rate limited test")
	env.srv.resyncGates(clock.Add(time.Minute))
	if got := script.unavailableHits.Load(); got != 0 {
		t.Fatalf("node hits for an expired gate = %d, want 0", got)
	}
}

// TestResyncRetriesFailingNode pins the failure path: a refusing node stays
// unmarked and is retried next pass, its failures only logged, and a later
// success marks it heard.
func TestResyncRetriesFailingNode(t *testing.T) {
	clock := time.Now().UTC()
	script := &nodeScript{unavailableMsg: "node boom"}
	script.unavailableStatus.Store(http.StatusInternalServerError)
	env, join := resyncEnv(t, script, &clock)
	logs := captureGateLogs(t)

	until := clock.Add(2 * time.Hour)
	recordHeadGate(t, env, resyncToken, until, "rate limited test")

	env.srv.resyncGates(clock)
	tickUntil(t, func() bool { return script.unavailableHits.Load() == 1 }, "the node never saw the gate")
	tickUntil(t, join.fired, "the failing resync never landed")
	if join.first() == nil {
		t.Fatal("failing resync landed nil, want the 500 carried")
	}
	if env.srv.gateHeardBy("b1", "anthropic", until) {
		t.Fatal("failed node marked heard, want it unmarked for the retry")
	}

	env.srv.resyncGates(clock)
	tickUntil(t, func() bool { return script.unavailableHits.Load() == 2 }, "the failing node was not retried")

	script.unavailableStatus.Store(0)
	env.srv.resyncGates(clock)
	tickUntil(t, func() bool { return script.unavailableHits.Load() == 3 }, "the recovered node never saw the gate")
	tickUntil(t, func() bool { return env.srv.gateHeardBy("b1", "anthropic", until) }, "the landed gate never marked heard")
	env.srv.resyncGates(clock)
	if got := script.unavailableHits.Load(); got != 3 {
		t.Fatalf("node hits after a heard pass = %d, want still 3", got)
	}
	if got := logs.String(); got == "" {
		t.Error("no log line for the failed resync, want the log-only failure")
	}
}
