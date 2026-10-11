package relevo

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainMirrorCheckWorkflow is flowPlacedCheckWorkflow's shape -- one writer and
// one check command -- used here on a row that names a server. The check step
// is what makes the mirror look like a placed writer to the local check sweep.
const chainMirrorCheckWorkflow = `name: mirrorcheck
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: check } }
  check: { check: "make check", on: { green: done, red: { halt: "red" } } }
`

// seedMirrorChain plants the mirror a server chain's client copy wears once the
// server itself has run the gate: the seedServerChain row with the check
// workflow on it and the engine awaiting check run 1. The mirror carries the
// server's state verbatim, so nothing on this machine owns that gate.
func seedMirrorChain(t *testing.T, rt Runtime, name string) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain(name)
		if err != nil {
			return err
		}
		c.WorkflowJSON = []byte(chainMirrorCheckWorkflow)
		c.StateJSON, err = json.Marshal(workflow.State{
			Status:   workflow.StatusRunning,
			At:       "check",
			Awaiting: workflow.Awaiting{Step: "check", Run: 1},
		})
		if err != nil {
			return err
		}
		return tx.ChainPut(c)
	}); err != nil {
		t.Fatalf("seedMirrorChain: %v", err)
	}
}

// TestMirrorChainCheckTickNeverPollsOrHalts pins that a mirror's check is never
// driven from this machine: the server runs the gate as a local check row and
// never creates a served run under the id the client mints, so a GetCheck here
// would 404 and halt the mirror -- once per tick, with a false end delivery
// each time -- while the server runs the gate normally.
func TestMirrorChainCheckTickNeverPollsOrHalts(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	seedServerChain(t, rt, "shop")
	seedMirrorChain(t, rt, "shop")

	fr.calls = nil
	fr.getCheckErr = &client.HTTPError{
		Status: 404,
		Body:   remote.ErrorBody{Message: "relevo: no such check run on this binding"},
	}
	tickChainChecks(context.Background(), rt)

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(workflow.StatusRunning) || row.Reason != "" {
		t.Errorf("mirror status = %s (%q), want running with no halt reason", row.Status, row.Reason)
	}
	if step, run := chainAwaitedCheck(t, rt); step != "check" || run != 1 {
		t.Errorf("awaiting check = %q run %d, want it still awaiting check run 1", step, run)
	}
	for _, call := range fr.calls {
		if strings.HasPrefix(call, "GetCheck:") {
			t.Errorf("calls = %v, want no GetCheck: a mirror must not poll a run the server never created", fr.calls)
			break
		}
	}
	for _, m := range chainMembersOf(row) {
		if entries := chainPendingChain(t, rt, m); len(entries) != 0 {
			t.Errorf("member %s queued %d end deliveries, want none for a mirror's own tick", m, len(entries))
		}
	}
}

// TestMirrorChainCheckTickSkipsNoRunMirror pins the same guard for the shape a
// mirror wears before its check has a run id: it is still the server's gate, so
// the tick must not answer it from a pull this machine has not made.
func TestMirrorChainCheckTickSkipsNoRunMirror(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	seedServerChain(t, rt, "shop")
	seedMirrorChain(t, rt, "shop")
	chainAwaitingWithoutRun(t, rt)

	fr.calls = nil
	tickChainChecks(context.Background(), rt)

	if row := chainStoredRow(t, rt, "shop"); row.Status != string(workflow.StatusRunning) {
		t.Errorf("mirror status = %q (%q), want running", row.Status, row.Reason)
	}
	if slices.ContainsFunc(fr.calls, func(c string) bool {
		return strings.HasPrefix(c, "GetCheck:") || strings.HasPrefix(c, "RunCheck:")
	}) {
		t.Errorf("calls = %v, want no check call of any kind for a mirror", fr.calls)
	}
}

// TestMirrorChainCheckSurvivesPullTickPull pins the whole mirror cycle for a
// chain whose check the server is running: a pull, then the local tick, then
// another pull leaves the mirror running and awaiting its check, and delivers
// nothing. The ping-pong this rules out is a tick halting the mirror on a run
// the server never created and the next pull flipping it back to running -- one
// chain notice per cycle, which is the ~12 s burst in the server-run report.
func TestMirrorChainCheckSurvivesPullTickPull(t *testing.T) {
	t.Parallel()

	view := chainPullView("shop", string(chain.StatusRunning), 1, 0, 0)
	// The server's own state is what a pull installs, and it has the chain on
	// its check step awaiting the run it started there.
	view.Workflow = []byte(chainMirrorCheckWorkflow)
	state, err := json.Marshal(workflow.State{
		Status:   workflow.StatusRunning,
		At:       "check",
		Awaiting: workflow.Awaiting{Step: "check", Run: 1},
	})
	if err != nil {
		t.Fatalf("marshal the server state: %v", err)
	}
	view.State = state
	fr := chainPullFake(view)
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	// Whatever the tick finds under the id this machine would mint, it is not
	// this machine's to read: the server drives the gate.
	fr.getCheckErr = &client.HTTPError{
		Status: 404,
		Body:   remote.ErrorBody{Message: "relevo: no such check run on this binding"},
	}

	for round := 1; round <= 3; round++ {
		fr.calls = nil
		tickChainChecks(context.Background(), rt)
		if err := chainPullServers(context.Background(), rt); err != nil {
			t.Fatalf("round %d: chainPullServers: %v", round, err)
		}

		row := chainStoredRow(t, rt, "shop")
		if row.Status != string(workflow.StatusRunning) || row.Reason != "" {
			t.Fatalf("round %d: mirror status = %s (%q), want running with no halt reason", round, row.Status, row.Reason)
		}
		if step, run := mirrorAwaitedCheck(t, rt); step != "check" || run != 1 {
			t.Fatalf("round %d: awaiting check = %q run %d, want check run 1", round, step, run)
		}
		for _, call := range fr.calls {
			if strings.HasPrefix(call, "GetCheck:") || strings.HasPrefix(call, "CreateCheck:") {
				t.Fatalf("round %d: calls = %v, want no check call of any kind from a mirror", round, fr.calls)
			}
		}
		for _, m := range chainMembersOf(row) {
			if entries := chainPendingChain(t, rt, m); len(entries) != 0 {
				t.Fatalf("round %d: member %s queued %d chain deliveries, want none", round, m, len(entries))
			}
		}
	}
}

// mirrorAwaitedCheck returns the mirror's awaiting check step and run.
func mirrorAwaitedCheck(t *testing.T, rt Runtime) (string, int) {
	t.Helper()
	st, err := chainWorkflowState(chainStoredRow(t, rt, "shop"))
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	return st.Awaiting.Step, st.Awaiting.Run
}
