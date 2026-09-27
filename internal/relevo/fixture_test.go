package relevo

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/store"
)

// This file holds the internal/relevo test fixtures (#303 step 3). Before
// #303 every one of them seeded its world through a fake pane client and
// BindOptions.MasterMindPane; both are gone, so each fixture now builds on
// newRuntime's mastermind registry and on the store. No pane type appears here
// at all: a local builder is a headless process relevo runs, and the tests
// drive it through fakeRunner.

// seedBound binds webshop on the agy test candidate, headless (#303): the
// runtime gets a fakeRunner, and no pane agent is added for the builder --
// there is none.
func seedBound(t *testing.T) (Runtime, store.Binding) {
	t.Helper()
	rt := newRuntime(t)
	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	return rt, b
}

// seedHeadless is seedBound with fr as the runtime's Runner, so a test can
// inspect the process a round starts.
func seedHeadless(t *testing.T, fr *fakeRunner) (Runtime, store.Binding) {
	t.Helper()
	rt := newRuntime(t)
	rt.Runner = fr
	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind --headless: %v", err)
	}
	return rt, b
}

// sentBinding puts a binding one Send into round 1, with a working builder.
func sentBinding(t *testing.T) (Runtime, store.Binding) {
	t.Helper()
	rt, _ := seedBound(t)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return rt, b
}

// seedClaudeHeadless is sentBinding with a claude builder, whose stream is
// the claude fixture's shape -- the harness whose session id arrives as
// session_id rather than as a resolved session (see usage.StreamSession).
func seedClaudeHeadless(t *testing.T, fr *fakeRunner) (Runtime, store.Binding) {
	t.Helper()
	rt := newRuntime(t)
	rt.Runner = fr
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testClaudeRef, MasterMindID: testMasterMindName, CWD: "/repo",
	}); err != nil {
		t.Fatalf("Bind --headless: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return rt, b
}

// queuedBinding is seedBound with one mastermind-bound report entry already
// queued and nothing sent: a payload is waiting on the mastermind.
func queuedBinding(t *testing.T) (Runtime, store.Binding) {
	t.Helper()
	rt, b := seedBound(t)

	entry := store.LogEntry{
		Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
		Path:    "/x/001-report.md",
		Payload: "The runner finished round 1. Report: /x/001-report.md",
	}
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		return delivery.Queue(context.Background(), deliveryDeps(rt), tx, b.Name, entry)
	})
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	return rt, b
}

// timedOutBinding is a binding whose round is well past its budget, with a
// payload already waiting on the mastermind. The waiting payload is what made the
// halt and the held-payload notice overwrite each other's state and each
// re-notify on every poll.
func timedOutBinding(t *testing.T) (Runtime, store.Binding) {
	t.Helper()
	rt, b := sentBinding(t)
	b.RoundTimeoutMS = int((30 * time.Minute).Milliseconds())
	b.RoundStartedAt = baseTime.Add(-31 * time.Minute)
	return rt, b
}

// sentSwitchable binds webshop to agy/other/m with the three-builder order
// and hands it round 1, so a rate limit on provider "other" leaves claude
// and opencode (provider "test") available to switch to.
func sentSwitchable(t *testing.T) (Runtime, store.Binding) {
	t.Helper()
	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, testTwoProviderJSON)
	rt.Policy = orderOf("builder", "agy/other/m", testClaudeRef, testOpencodeRef)
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "agy/other/m", MasterMindID: testMasterMindName, CWD: "/repo",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return rt, b
}

// startVerifyRound puts webshop one reconcile past a verify round's close:
// the round closed, the reviewer was started in its throwaway worktree, and
// the process is scripted to have exited 0. The caller writes the stream the
// reviewer left behind, then ticks the consult.
func startVerifyRound(t *testing.T) (Runtime, *fakeRunner, *fakeGit, store.Binding) {
	t.Helper()
	rt, b := sentBinding(t)
	fr := newFakeRunner()
	fg := &fakeGit{headCommitID: "head1"}
	rt.Runner = fr
	rt.Git = fg
	rt.NewID = func() string { return verifyConsultID }

	b.RoundVerify = true
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.handles) != 1 {
		t.Fatalf("verify consult processes = %d, want 1", len(fr.handles))
	}
	return rt, fr, fg, got
}

// reconcile wraps Reconcile with the state lock a daemon would hold across
// the whole read-reconcile-write, since Reconcile itself takes a *store.Tx
// rather than locking on its own.
func reconcile(t *testing.T, rt Runtime, b store.Binding) (store.Binding, error) {
	t.Helper()
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		out, err = Reconcile(context.Background(), rt, tx, b)
		return err
	})
	return out, err
}

// gateOnLimitSetup is TestReconcileHeadlessGatedKillsAndSwitches's own setup
// (two-provider set, headless bind on agy/other/m, the three-builder order,
// one Send), without the Unavailable call gateOnLimit is meant to replace.
func gateOnLimitSetup(t *testing.T, fr *fakeRunner) (Runtime, store.Binding) {
	t.Helper()
	rt := newRuntime(t)
	rt.Runner = fr
	rt.Candidates = candidateSet(t, testTwoProviderJSON)
	rt.Policy = orderOf("builder", "agy/other/m", testClaudeRef, testOpencodeRef)
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "agy/other/m", MasterMindID: testMasterMindName, CWD: "/repo", Headless: true,
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return rt, b
}

// containsAdjacentPair reports whether args contains a, b as consecutive
// elements, in that order.
func containsAdjacentPair(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}
