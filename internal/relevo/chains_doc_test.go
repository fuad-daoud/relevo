package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

func TestReadChainsSortsHaltedAndRunningFirst(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	now := rt.Now().UTC()

	statuses := []struct {
		name   string
		status string
	}{
		{"c-done", string(chain.StatusDone)},
		{"c-stopped", string(chain.StatusStopped)},
		{"c-running", string(chain.StatusRunning)},
		{"c-halted", string(chain.StatusHalted)},
	}

	for _, s := range statuses {
		row := db.ChainRow{
			ID: db.NewID(), Name: s.name, Status: s.status, Phase: "build", Step: "building",
			Plan: 1, Plans: 1, Builder: s.name, CreatedAt: now, UpdatedAt: now,
		}
		if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.ChainPut(row) }); err != nil {
			t.Fatalf("ChainPut(%s): %v", s.name, err)
		}
	}

	doc, err := ReadChains(context.Background(), rt)
	if err != nil {
		t.Fatalf("ReadChains: %v", err)
	}

	wantOrder := []string{"c-halted", "c-running", "c-stopped", "c-done"}
	if len(doc.Chains) != len(wantOrder) {
		t.Fatalf("got %d chains, want %d", len(doc.Chains), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got := doc.Chains[i].Name; got != want {
			t.Errorf("chain[%d] = %q, want %q", i, got, want)
		}
	}
}

func TestReadChainsNestsChildrenUnderParent(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	now := rt.Now().UTC()

	chains := []db.ChainRow{
		{ID: db.NewID(), Name: "root", Status: string(chain.StatusDone), Phase: "build", Step: "building", Plan: 1, Plans: 1, CreatedAt: now, UpdatedAt: now},
		{ID: db.NewID(), Name: "child1", Parent: "root", Status: string(chain.StatusRunning), Phase: "build", Step: "building", Plan: 1, Plans: 1, CreatedAt: now, UpdatedAt: now},
		{ID: db.NewID(), Name: "child2", Parent: "child1", Status: string(chain.StatusRunning), Phase: "build", Step: "building", Plan: 1, Plans: 1, CreatedAt: now, UpdatedAt: now},
		{ID: db.NewID(), Name: "orphan", Parent: "missing", Status: string(chain.StatusRunning), Phase: "build", Step: "building", Plan: 1, Plans: 1, CreatedAt: now, UpdatedAt: now},
	}

	// Deep chain exceeding the depth cap of 8: root-deep -> d1 -> ... -> d9
	chains = append(chains, db.ChainRow{
		ID: db.NewID(), Name: "root-deep", Status: string(chain.StatusDone), Phase: "build", Step: "building", Plan: 1, Plans: 1, CreatedAt: now, UpdatedAt: now,
	})
	prev := "root-deep"
	for i := 1; i <= 9; i++ {
		name := fmt.Sprintf("d%d", i)
		chains = append(chains, db.ChainRow{
			ID: db.NewID(), Name: name, Parent: prev, Status: string(chain.StatusRunning), Phase: "build", Step: "building", Plan: 1, Plans: 1, CreatedAt: now, UpdatedAt: now,
		})
		prev = name
	}

	for _, c := range chains {
		if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.ChainPut(c) }); err != nil {
			t.Fatalf("ChainPut(%s): %v", c.Name, err)
		}
	}

	doc, err := ReadChains(context.Background(), rt)
	if err != nil {
		t.Fatalf("ReadChains: %v", err)
	}

	byName := map[string]ChainEntry{}
	for _, c := range doc.Chains {
		byName[c.Name] = c
	}

	// orphan has missing parent, so it must be treated as a root (Depth 0)
	if orphan, ok := byName["orphan"]; !ok || orphan.Depth != 0 {
		t.Errorf("orphan depth = %d, want 0", orphan.Depth)
	}

	// child1 follows root directly with Depth 1, child2 follows child1 with Depth 2
	child1 := byName["child1"]
	child2 := byName["child2"]
	if child1.Depth != 1 || child1.Parent != "root" {
		t.Errorf("child1 = depth %d parent %q, want 1, root", child1.Depth, child1.Parent)
	}
	if child2.Depth != 2 || child2.Parent != "child1" {
		t.Errorf("child2 = depth %d parent %q, want 2, child1", child2.Depth, child2.Parent)
	}

	// In the flattened doc, child1 and child2 must follow root
	rootIdx := -1
	child1Idx := -1
	child2Idx := -1
	for i, c := range doc.Chains {
		switch c.Name {
		case "root":
			rootIdx = i
		case "child1":
			child1Idx = i
		case "child2":
			child2Idx = i
		}
	}
	if child1Idx != rootIdx+1 || child2Idx != rootIdx+2 {
		t.Errorf("indices: root=%d, child1=%d, child2=%d; want adjacent", rootIdx, child1Idx, child2Idx)
	}

	// Depth cap: d9 must have depth capped at 8
	d9 := byName["d9"]
	if d9.Depth != 8 {
		t.Errorf("d9 depth = %d, want capped at 8", d9.Depth)
	}
}

func TestReadChainsStepOutcomeText(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	now := rt.Now().UTC()

	def := workflow.Definition{
		Name:   "test-wf",
		Start:  "review",
		Inputs: workflow.Inputs{Plans: workflow.InputNone, Task: workflow.InputNone},
		Steps: map[string]workflow.Step{
			"review": {
				Run: "reviewer",
				On:  map[string]workflow.Target{"done": workflow.StepTarget("check")},
			},
			"check": {
				Check: "make test",
				On:    map[string]workflow.Target{"red": workflow.StepTarget("repair"), "green": workflow.DoneTarget()},
			},
			"repair": {
				Run: "builder",
				On:  map[string]workflow.Target{"done": workflow.DoneTarget()},
			},
		},
	}
	st := workflow.State{
		Status: workflow.StatusRunning,
		At:     "repair",
		Visits: map[string]int{"review": 1, "check": 1},
		Results: map[string]workflow.Result{
			"review": {
				Round:    2,
				Status:   "done",
				Outcomes: map[string]string{"verdict": "changes"},
			},
			"check": {
				Round:  1,
				Status: "red",
			},
		},
	}
	wfJSON, _ := json.Marshal(def)
	stJSON, _ := json.Marshal(st)

	c := db.ChainRow{
		ID: db.NewID(), Name: "chain-outcomes", Status: string(chain.StatusRunning),
		Phase: "build", Step: "repair", Plan: 1, Plans: 1,
		WorkflowJSON: wfJSON, StateJSON: stJSON,
		CreatedAt: now, UpdatedAt: now,
	}

	checkEv := workflow.Event{Kind: workflow.EventCheckClosed, Step: "check", Run: 1, Result: "red"}
	checkAct := workflow.Action{Kind: workflow.ActionSend, Step: "repair"}
	trace := db.ChainEventRow{
		ChainID: c.ID, Seq: 1, TS: now, Step: "check",
		Event: workflow.EncodeEvent(checkEv), Action: workflow.EncodeAction(checkAct),
	}

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.ChainSaveWithEvent(c, trace)
	}); err != nil {
		t.Fatalf("ChainSaveWithEvent: %v", err)
	}

	doc, err := ReadChains(context.Background(), rt)
	if err != nil {
		t.Fatalf("ReadChains: %v", err)
	}
	if len(doc.Chains) != 1 {
		t.Fatalf("got %d chains, want 1", len(doc.Chains))
	}

	stepMap := map[string]ChainStep{}
	for _, s := range doc.Chains[0].Steps {
		stepMap[s.ID] = s
	}

	if rev, ok := stepMap["review"]; !ok || rev.LastOutcome != "review r2 · verdict=changes" {
		t.Errorf("review outcome = %q, want %q", rev.LastOutcome, "review r2 · verdict=changes")
	}

	if chk, ok := stepMap["check"]; !ok || chk.LastOutcome != "check · red → repair" {
		t.Errorf("check outcome = %q, want %q", chk.LastOutcome, "check · red → repair")
	}
}

func TestReadChainsInFlightStep(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	now := rt.Now().UTC()

	def := workflow.Definition{
		Name:   "inflight-wf",
		Start:  "build",
		Inputs: workflow.Inputs{Plans: workflow.InputNone, Task: workflow.InputNone},
		Steps: map[string]workflow.Step{
			"build":  {Run: "builder", On: map[string]workflow.Target{"done": workflow.StepTarget("review")}},
			"review": {Run: "reviewer", On: map[string]workflow.Target{"done": workflow.DoneTarget()}},
		},
	}
	st := workflow.State{
		Status:   workflow.StatusRunning,
		At:       "build",
		Awaiting: workflow.Awaiting{Step: "build", Round: 3, Member: "builder"},
		Visits:   map[string]int{"build": 1},
	}
	wfJSON, _ := json.Marshal(def)
	stJSON, _ := json.Marshal(st)

	c := db.ChainRow{
		ID: db.NewID(), Name: "chain-inflight", Status: string(chain.StatusRunning),
		Phase: "build", Step: "building", Plan: 1, Plans: 1,
		WorkflowJSON: wfJSON, StateJSON: stJSON,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.ChainPut(c) }); err != nil {
		t.Fatalf("ChainPut: %v", err)
	}

	doc, err := ReadChains(context.Background(), rt)
	if err != nil {
		t.Fatalf("ReadChains: %v", err)
	}
	if len(doc.Chains) != 1 {
		t.Fatalf("got %d chains, want 1", len(doc.Chains))
	}

	stepMap := map[string]ChainStep{}
	for _, s := range doc.Chains[0].Steps {
		stepMap[s.ID] = s
	}

	buildStep, ok := stepMap["build"]
	if !ok {
		t.Fatal("step build missing")
	}
	if !buildStep.InFlight {
		t.Errorf("buildStep.InFlight = false, want true")
	}
	if buildStep.Round != 3 {
		t.Errorf("buildStep.Round = %d, want 3", buildStep.Round)
	}

	reviewStep, ok := stepMap["review"]
	if !ok {
		t.Fatal("step review missing")
	}
	if reviewStep.InFlight {
		t.Errorf("reviewStep.InFlight = true, want false")
	}
}

func TestReadChainsServerChainStaleOnUnreachable(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("srv-chain", string(chain.StatusRunning), 0, 0, 0))
	fr.getChainErr = errors.New("dial tcp: connection refused")
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "srv-chain")

	doc, err := ReadChains(context.Background(), rt)
	if err != nil {
		t.Fatalf("ReadChains must not fail on unreachable server: %v", err)
	}
	if len(doc.Chains) != 1 {
		t.Fatalf("got %d chains, want 1", len(doc.Chains))
	}

	entry := doc.Chains[0]
	if entry.Stale != "dial tcp: connection refused" {
		t.Errorf("entry.Stale = %q, want error text", entry.Stale)
	}
	if entry.Status != string(chain.StatusRunning) {
		t.Errorf("entry.Status = %q, want mirror status kept", entry.Status)
	}
}

func TestReadChainsWherePlacedAndServer(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	now := rt.Now().UTC()

	// 1. Local chain
	localChain := db.ChainRow{
		ID: db.NewID(), Name: "local-chain", Status: string(chain.StatusRunning),
		Phase: "build", Step: "building", Plan: 1, Plans: 1, Builder: "local-chain",
		CreatedAt: now, UpdatedAt: now,
	}
	localBinding := store.Binding{
		Name: "local-chain", CWD: filepath.Join(t.TempDir(), "local"), Round: 1, State: store.StateActive,
	}

	// 2. Placed chain: member has Link != nil and Builder.Server = "zen"
	placedChain := db.ChainRow{
		ID: db.NewID(), Name: "placed-chain", Status: string(chain.StatusRunning),
		Phase: "build", Step: "building", Plan: 1, Plans: 1, Builder: "placed-chain",
		CreatedAt: now, UpdatedAt: now,
	}
	placedBinding := store.Binding{
		Name: "placed-chain", CWD: filepath.Join(t.TempDir(), "placed"), Round: 1, State: store.StateActive,
		Link:    &store.RemoteLink{Installation: "inst-1", ID: "rec-1"},
		Builder: store.Endpoint{Mode: store.ModeRemote, Server: "zen"},
	}

	// 3. Server chain: ChainRow.Server is set
	srvChain := db.ChainRow{
		ID: db.NewID(), Name: "server-chain", Status: string(chain.StatusRunning),
		Phase: "build", Step: "building", Plan: 1, Plans: 1, Server: "contabo",
		CreatedAt: now, UpdatedAt: now,
	}

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.CreateChain(localChain, []store.Binding{localBinding}); err != nil {
			return err
		}
		if err := tx.CreateChain(placedChain, []store.Binding{placedBinding}); err != nil {
			return err
		}
		return tx.ChainPut(srvChain)
	}); err != nil {
		t.Fatalf("seed chains: %v", err)
	}

	doc, err := ReadChains(context.Background(), rt)
	if err != nil {
		t.Fatalf("ReadChains: %v", err)
	}

	whereMap := map[string]string{}
	for _, c := range doc.Chains {
		whereMap[c.Name] = c.Where
	}

	if got := whereMap["local-chain"]; got != "local" {
		t.Errorf("local-chain where = %q, want local", got)
	}
	if got := whereMap["placed-chain"]; got != "placed zen" {
		t.Errorf("placed-chain where = %q, want placed zen", got)
	}
	if got := whereMap["server-chain"]; got != "server contabo" {
		t.Errorf("server-chain where = %q, want server contabo", got)
	}
}

func TestReadChainsDoneWithoutStateJSONDoesNotPanic(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	now := rt.Now().UTC()

	c := db.ChainRow{
		ID: db.NewID(), Name: "done-nostate", Status: string(chain.StatusDone),
		Phase: "finished", Step: "building", Plan: 1, Plans: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.ChainPut(c) }); err != nil {
		t.Fatalf("ChainPut: %v", err)
	}

	doc, err := ReadChains(context.Background(), rt)
	if err != nil {
		t.Fatalf("ReadChains: %v", err)
	}
	if len(doc.Chains) != 1 {
		t.Fatalf("got %d chains, want 1", len(doc.Chains))
	}
	entry := doc.Chains[0]
	if entry.Name != "done-nostate" || entry.Status != string(chain.StatusDone) {
		t.Errorf("entry = %+v", entry)
	}
	if len(entry.Steps) != 0 {
		t.Errorf("Steps = %v, want empty", entry.Steps)
	}
}

// TestChainsServerChainViaFakeGetChain pins the server chain's trace: it is
// read through the server's own GetChain, and a server that cannot be
// reached leaves the chain readable as stale rather than failing the read.
func TestChainsServerChainViaFakeGetChain(t *testing.T) {
	t.Parallel()

	view := chainPullView("srv-chain", string(chain.StatusRunning), 1, 1, 0)
	view.Trace = []remote.ChainEventView{{
		Seq: 1, Step: "reviewing", Member: "srv-chain", Round: 1, Plan: 1,
		Event:  `{"kind":"builder_closed","gate":"green"}`,
		Action: `{"member":"reviewer","round":1}`,
	}}
	fr := chainPullFake(view)
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "srv-chain")

	doc, err := ReadChains(context.Background(), rt)
	if err != nil {
		t.Fatalf("ReadChains: %v", err)
	}
	if len(doc.Chains) != 1 {
		t.Fatalf("got %d chains, want 1", len(doc.Chains))
	}
	if doc.Chains[0].Stale != "" {
		t.Errorf("entry.Stale = %q, want empty for a reachable server", doc.Chains[0].Stale)
	}

	trace, err := ChainTrace(context.Background(), rt, "srv-chain")
	if err != nil {
		t.Fatalf("ChainTrace: %v", err)
	}
	if len(trace.Events) != 1 || trace.Events[0].Round != 1 {
		t.Fatalf("trace events = %+v, want the server's one row at round 1", trace.Events)
	}
	if n := countCalls(fr, "GetChain:zen:srv-chain"); n == 0 {
		t.Error("the trace was not read through the server's GetChain")
	}

	// The same chain with the server gone: the chain still opens, marked stale.
	fr.getChainErr = errors.New("dial tcp: connection refused")
	doc, err = ReadChains(context.Background(), rt)
	if err != nil {
		t.Fatalf("ReadChains on an unreachable server: %v", err)
	}
	if len(doc.Chains) != 1 || doc.Chains[0].Stale == "" {
		t.Errorf("unreachable server: chains = %+v, want the chain marked stale", doc.Chains)
	}
}
