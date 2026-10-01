package relevo

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// storedFlowParam reads a string param from a chain row's stored workflow
// definition.
func storedFlowParam(t *testing.T, row db.ChainRow, name string) string {
	t.Helper()
	def, err := workflow.Parse(row.WorkflowJSON)
	if err != nil {
		t.Fatalf("parse stored workflow: %v", err)
	}
	p, ok := def.Params[name]
	if !ok {
		t.Fatalf("stored workflow has no param %q", name)
	}
	return p.Str
}

// startedFlowChain starts one chain on the shipped default workflow: startedChain
// with Workflow set, so a test that used to drive the fixed state machine runs on
// the engine instead. The chain's row columns stay projected, so a test keeps
// its plan and step assertions.
func startedFlowChain(t *testing.T, rt Runtime, opts ChainOptions) ChainResult {
	t.Helper()
	opts.Workflow = "default"
	return startedChain(t, rt, opts)
}

// flowCheckRun is a chain's oldest unsettled check run, failing when the chain
// has none.
func flowCheckRun(t *testing.T, rt Runtime, name string) (int, db.ChainCheckRow) {
	t.Helper()
	for run := 1; ; run++ {
		row, err := rt.Store.ChainCheck(name, run)
		if errors.Is(err, store.ErrNotFound) {
			t.Fatalf("chain %s has no unsettled check at run %d", name, run)
		}
		if err != nil {
			t.Fatalf("ChainCheck %s %d: %v", name, run, err)
		}
		if row.Result == "" {
			return run, row
		}
	}
}

// flowFinishCheck settles a chain's in-flight check run: it writes the run's
// log, ends its process with the result's exit code, and ticks the checks so the
// engine sees check_closed. An empty log body leaves the run's log unwritten,
// which the seed then words as unavailable.
func flowFinishCheck(t *testing.T, rt Runtime, name, result, logBody string) {
	t.Helper()
	_, row := flowCheckRun(t, rt, name)
	if logBody != "" {
		if err := os.WriteFile(row.Log, []byte(logBody), 0o644); err != nil {
			t.Fatalf("write the check log: %v", err)
		}
	}
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatal("the chain runtime must carry a fakeRunner")
	}
	fr.script(row.PID, false)
	code := 0
	if result == chainCheckRed {
		code = 1
	}
	fr.exit(row.PID, code)
	tickChainChecks(context.Background(), rt)
}

// chainGreenCheck settles a chain's in-flight check green.
func chainGreenCheck(t *testing.T, rt Runtime, name string) {
	t.Helper()
	flowFinishCheck(t, rt, name, chainCheckGreen, "PASS\n")
}

// chainRedCheck settles a chain's in-flight check red with the given log. Two
// reds with the same log body repeat each other, which skips the repair budget.
func chainRedCheck(t *testing.T, rt Runtime, name, logBody string) {
	t.Helper()
	flowFinishCheck(t, rt, name, chainCheckRed, logBody)
}
