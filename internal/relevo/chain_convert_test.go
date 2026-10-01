package relevo

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// legacyChainRow is a chain row as a binary before the workflow columns wrote
// it: the fixed phase and step words, no state, and its members in the four
// legacies columns. awaiting names the part the row waits on and round the
// member round it awaits.
func legacyChainRow(name, status, phase, step, awaiting string, round, corrections int) db.ChainRow {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return db.ChainRow{
		ID:             db.NewID(),
		Name:           name,
		Status:         status,
		Phase:          phase,
		Step:           step,
		Plan:           1,
		Plans:          1,
		Corrections:    corrections,
		PlanPathsJSON:  []byte(`["/plans/001.md"]`),
		SettingsJSON:   []byte(`{"MaxCorrections":3}`),
		AwaitingMember: awaiting,
		AwaitingRound:  round,
		Builder:        name,
		Reviewer:       name + "-rev",
		Planner:        name + "-plan",
		Security:       name + "-sec",
		MasterMindID:   testMasterMindID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// legacyChainBindings builds the four member bindings a legacy row names: the
// builder takes the empty role, the readers their own, exactly as a start of
// that era wrote them.
func legacyChainBindings(name string) []store.Binding {
	cwd := "/trees/" + name
	return []store.Binding{
		{
			Name: name, CWD: cwd, Round: 1, State: store.StateActive, MasterMindID: testMasterMindID,
			Builder: store.Endpoint{Kind: "agy", Mode: store.ModeHeadless}, BuilderCandidate: testAgyRef,
		},
		{Name: name + "-rev", CWD: cwd, Role: "reviewer", Shape: store.ShapeReader, Round: 1, State: store.StateActive, MasterMindID: testMasterMindID},
		{Name: name + "-plan", CWD: cwd, Role: "planner", Shape: store.ShapeReader, Round: 1, State: store.StateActive, MasterMindID: testMasterMindID},
		{Name: name + "-sec", CWD: cwd, Role: "security", Shape: store.ShapeReader, Round: 1, State: store.StateActive, MasterMindID: testMasterMindID},
	}
}

// seedLegacyChain writes a legacy row and its member bindings with no
// chain_member rows, the shape a database written before the table has.
func seedLegacyChain(t *testing.T, rt Runtime, c db.ChainRow, members []store.Binding) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		for _, b := range members {
			if err := tx.Save(b); err != nil {
				return err
			}
		}
		return tx.ChainPut(c)
	}); err != nil {
		t.Fatalf("seed legacy chain: %v", err)
	}
}

// convertedState decodes the engine state a conversion wrote.
func convertedState(t *testing.T, rt Runtime, name string) (workflow.Definition, workflow.State) {
	t.Helper()
	c, err := rt.Store.Chain(name)
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	if len(c.WorkflowJSON) == 0 || len(c.StateJSON) == 0 {
		t.Fatalf("row carries workflow %d and state %d bytes, want both", len(c.WorkflowJSON), len(c.StateJSON))
	}
	def, err := workflow.Parse(c.WorkflowJSON)
	if err != nil {
		t.Fatalf("parse workflow: %v", err)
	}
	var st workflow.State
	if err := json.Unmarshal(c.StateJSON, &st); err != nil {
		t.Fatalf("parse state: %v", err)
	}
	return def, st
}

// TestConvertLegacyRunningKeepsAwaitedRound pins the running row: it converts
// with the round it awaited, so the close it is waiting for still lands.
func TestConvertLegacyRunningKeepsAwaitedRound(t *testing.T) {
	rt := newRuntime(t)
	c := legacyChainRow("x", "running", "build", "reviewing", "reviewer", 2, 0)
	seedLegacyChain(t, rt, c, legacyChainBindings("x"))

	if err := ConvertLegacyChains(rt); err != nil {
		t.Fatalf("ConvertLegacyChains: %v", err)
	}
	_, st := convertedState(t, rt, "x")
	if st.Awaiting.Step != "review" || st.Awaiting.Member != "reviewer" || st.Awaiting.Round != 2 {
		t.Errorf("awaiting = %+v, want review/reviewer round 2", st.Awaiting)
	}
}

// TestConvertLegacyIsIdempotent pins the one-time rule: a row that already
// carries a state is left alone, so a second run changes nothing.
func TestConvertLegacyIsIdempotent(t *testing.T) {
	rt := newRuntime(t)
	c := legacyChainRow("x", "running", "build", "building", "builder", 1, 0)
	seedLegacyChain(t, rt, c, legacyChainBindings("x"))

	if err := ConvertLegacyChains(rt); err != nil {
		t.Fatalf("first ConvertLegacyChains: %v", err)
	}
	first, err := rt.Store.Chain("x")
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	if err := ConvertLegacyChains(rt); err != nil {
		t.Fatalf("second ConvertLegacyChains: %v", err)
	}
	second, err := rt.Store.Chain("x")
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	if string(first.WorkflowJSON) != string(second.WorkflowJSON) || string(first.StateJSON) != string(second.StateJSON) {
		t.Error("a second conversion rewrote the workflow or the state")
	}
}

// TestConvertLegacySharedActorMapsBothBindings pins an actor named by two
// parts: both of its bindings get a member row, and a lookup by actor returns
// the first, which is the builder's part order.
func TestConvertLegacySharedActorMapsBothBindings(t *testing.T) {
	rt := newRuntime(t)
	c := legacyChainRow("x", "running", "build", "building", "builder", 1, 0)
	members := legacyChainBindings("x")
	// The planner binding runs the reviewer's actor, so one actor fills two
	// bindings.
	members[2].Role = "reviewer"
	seedLegacyChain(t, rt, c, members)

	if err := ConvertLegacyChains(rt); err != nil {
		t.Fatalf("ConvertLegacyChains: %v", err)
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		rows, err := chainFlowMembers(tx, c)
		if err != nil {
			return err
		}
		shared := 0
		for _, m := range rows {
			if m.Actor == "reviewer" {
				shared++
			}
		}
		if shared != 2 {
			t.Errorf("member rows for actor reviewer = %d, want 2", shared)
		}
		name, err := chainFlowMemberName(tx, c, "reviewer")
		if err != nil {
			return err
		}
		if name != "x-rev" {
			t.Errorf("first binding for actor reviewer = %q, want x-rev", name)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect members: %v", err)
	}
}

// TestConvertLegacyChainsEveryStepThenContinues pins the mapping: a row at
// each legacy step converts to the default workflow's step and continues when
// the close it awaits arrives.
func TestConvertLegacyChainsEveryStepThenContinues(t *testing.T) {
	cases := []struct {
		phase, step string
		corrections int
		want        string
	}{
		{"build", "building", 0, "build"},
		{"build", "building", 1, "build-fix"},
		{"build", "reviewing", 0, "review"},
		{"build", "correcting", 0, "correct"},
		{"security", "scanning", 0, "scan"},
		{"security", "planning-fixes", 0, "fix-plan"},
		{"security", "building", 0, "fix-build"},
		{"security", "building", 1, "fix-rebuild"},
		{"security", "reviewing", 0, "fix-review"},
		{"security", "correcting", 0, "fix-correct"},
	}
	rt := newRuntime(t)
	for i, tc := range cases {
		name := "chain" + string(rune('a'+i))
		c := legacyChainRow(name, "running", tc.phase, tc.step, "reviewer", 3, tc.corrections)
		seedLegacyChain(t, rt, c, legacyChainBindings(name))
	}
	if err := ConvertLegacyChains(rt); err != nil {
		t.Fatalf("ConvertLegacyChains: %v", err)
	}

	for i, tc := range cases {
		name := "chain" + string(rune('a'+i))
		def, st := convertedState(t, rt, name)
		if st.At != tc.want || st.Awaiting.Step != tc.want {
			t.Errorf("%s/%s c=%d: at/awaiting = %s/%s, want %s", tc.phase, tc.step, tc.corrections, st.At, st.Awaiting.Step, tc.want)
			continue
		}
		if st.Awaiting.Round != 3 {
			t.Errorf("%s/%s: awaiting round = %d, want 3", tc.phase, tc.step, st.Awaiting.Round)
		}
		ev := workflow.Event{
			Kind: workflow.EventStepClosed, Step: st.Awaiting.Step,
			Member: st.Awaiting.Member, Round: st.Awaiting.Round, Status: "done",
		}
		switch {
		case tc.want == "review" || tc.want == "fix-review":
			ev.Outcomes = map[string]string{"verdict": "changes"}
		case tc.want == "scan":
			ev.Outcomes = map[string]string{"findings": "0"}
		}
		_, acts := workflow.Next(def, st, ev)
		if len(acts) == 0 {
			t.Errorf("%s/%s: close from %s produced no action, want the chain to continue", tc.phase, tc.step, tc.want)
		}
	}
}
