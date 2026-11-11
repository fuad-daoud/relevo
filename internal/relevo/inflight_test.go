package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/store/storetest"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

func inflightBinding(name, tok string, mut func(*store.Binding)) store.Binding {
	b := store.Binding{
		Name:             name,
		CWD:              "/tmp/" + name,
		BuilderCandidate: tok,
		Builder:          store.Endpoint{Mode: store.ModeHeadless, PID: 100},
		State:            store.StateActive,
		Round:            1,
	}
	if mut != nil {
		mut(&b)
	}
	return b
}

func TestInFlightCountsOnlyLiveBuilders(t *testing.T) {
	const tok = "opencode/test/m"
	bs := []store.Binding{
		inflightBinding("live", tok, nil),
		inflightBinding("noproc", tok, func(b *store.Binding) { b.Builder.PID = 0 }),
		inflightBinding("done", tok, func(b *store.Binding) { b.State = store.StateDone }),
		inflightBinding("paused", tok, func(b *store.Binding) { b.State = store.StatePaused }),
		inflightBinding("remote", tok, func(b *store.Binding) { b.Builder.Mode = "" }),
	}
	if got := openRoundsByCandidate(bs)[tok]; got != 1 {
		t.Errorf("count = %d, want 1 (only the live headless builder)", got)
	}
}

func TestInFlightSpansThePool(t *testing.T) {
	set := candidateSet(t, `[
	  {"name":"a","harness":"opencode","provider":"p","model":"a","roles":["builder"],"max_concurrency":2},
	  {"name":"b","harness":"opencode","provider":"p","model":"b","roles":["builder"],"max_concurrency":2}
	]`)
	st := storetest.New(t)
	for _, b := range []store.Binding{
		inflightBinding("one", "opencode/p/a", nil),
		inflightBinding("two", "opencode/p/b", nil),
	} {
		if err := st.Save(b); err != nil {
			t.Fatal(err)
		}
	}
	rt := Runtime{Store: st, Candidates: set}
	gates := availability.Gates(AvailabilityDeps(rt))
	if len(gates) != 2 {
		t.Fatalf("gates = %+v, want both models of the pool at the cap", gates)
	}
	for _, g := range gates {
		if g.Kind != availability.ConcurrencyLimit || g.Note != "2/2" {
			t.Errorf("gate = %+v, want concurrency_limit 2/2", g)
		}
	}
}

func TestInFlightExceptSkipsTheResolvingBinding(t *testing.T) {
	st := storetest.New(t)
	for _, b := range []store.Binding{
		inflightBinding("mine", "opencode/p/a", nil),
		inflightBinding("other", "opencode/p/a", nil),
	} {
		if err := st.Save(b); err != nil {
			t.Fatal(err)
		}
	}
	in := AvailabilityDeps(Runtime{Store: st}).InFlight
	if got := in("")["opencode/p/a"]; got != 2 {
		t.Errorf("count = %d, want 2", got)
	}
	if got := in("mine")["opencode/p/a"]; got != 1 {
		t.Errorf("count excluding mine = %d, want 1", got)
	}
}

// TestInFlightCapsACandidateWhoseModelNamesAnEffort pins the count against a
// real-shaped opencode candidate: its model carries "#xhigh", so its ref does,
// and a binding running it must count against that ref's pool.
func TestInFlightCapsACandidateWhoseModelNamesAnEffort(t *testing.T) {
	set := candidateSet(t, `[
	  {"name":"qwen","harness":"opencode","provider":"alibaba-token-plan","model":"qwen3.8-flash#xhigh","roles":["builder"],"max_concurrency":1}
	]`)
	st := storetest.New(t)
	if err := st.Save(inflightBinding("one", "opencode/alibaba-token-plan/qwen3.8-flash#xhigh", nil)); err != nil {
		t.Fatal(err)
	}
	gates := availability.Gates(AvailabilityDeps(Runtime{Store: st, Candidates: set}))
	if len(gates) != 1 || gates[0].Kind != availability.ConcurrencyLimit || gates[0].Note != "1/1" {
		t.Fatalf("gates = %+v, want the effort-named candidate at its cap 1/1", gates)
	}
}

// plannerTestRegistry builds the registry the planner table reads: the
// shipped lite-planner and planner actors over the architect agent with no
// declared outputs, the two relevo planner actors with declared plan and
// design artifacts, the two morphic planner natives the same way, and every
// non-planner the table pins beside them.
func plannerTestRegistry(t *testing.T) *roles.Registry {
	t.Helper()
	native := func(name, shape string) roles.AgentEntry {
		return roles.AgentEntry{Shape: shape, Native: map[string]roles.DefRow{"claude": {Agent: name}}}
	}
	agents := map[string]roles.AgentEntry{
		"relevo-lite-planner":     native("relevo-lite-planner", "reader"),
		"relevo-planner":          native("relevo-planner", "reader"),
		"morphic-lite-planner":    native("morphic-lite-planner", "reader"),
		"morphic-planner":         native("morphic-planner", "reader"),
		"morphic-reviewer":        native("morphic-reviewer", "reader"),
		"morphic-reviewer-skills": native("morphic-reviewer-skills", "reader"),
		"relevo-builder":          native("relevo-builder", "writer"),
		"relevo-reviewer":         native("relevo-reviewer", "reader"),
	}
	verdict := workflow.Outputs{
		"verdict":  {Kind: workflow.OutputOneOf, Values: []string{"pass", "changes"}},
		"findings": {Kind: workflow.OutputArtifact},
	}
	actors := map[string]roles.Actor{
		"lite-planner":            {Agent: "architect"},
		"planner":                 {Agent: "architect"},
		"relevo-lite-planner":     {Agent: "relevo-lite-planner", Outputs: workflow.Outputs{"plan": {Kind: workflow.OutputArtifact}}},
		"relevo-planner":          {Agent: "relevo-planner", Outputs: workflow.Outputs{"design": {Kind: workflow.OutputArtifact}}},
		"morphic-lite-planner":    {Agent: "morphic-lite-planner", Outputs: workflow.Outputs{"plan": {Kind: workflow.OutputArtifact}}},
		"morphic-planner":         {Agent: "morphic-planner", Outputs: workflow.Outputs{"design": {Kind: workflow.OutputArtifact}}},
		"reviewer":                {Agent: "reviewer"},
		"relevo-reviewer":         {Agent: "relevo-reviewer", Outputs: verdict},
		"morphic-reviewer":        {Agent: "morphic-reviewer", Outputs: verdict},
		"morphic-reviewer-skills": {Agent: "morphic-reviewer-skills", Outputs: verdict, Skills: []string{"design-runbooks"}},
		"security":                {Agent: "security-reviewer"},
		"researcher":              {Agent: "researcher"},
		"builder":                 {Agent: "plan-executor"},
		"relevo-builder":          {Agent: "relevo-builder"},
	}
	f, _, err := roles.FromActors(agents, actors)
	if err != nil {
		t.Fatalf("FromActors: %v", err)
	}
	reg, err := roles.Build(f, &candidate.Set{}, policy.Policy{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return reg
}

// TestIsPlannerRoundAdmitsOnlyPlanAndDesignReaders pins the planner
// definition: the six planner actors read true -- the shipped pair through
// the architect agent's plan default, the rest through declared plan or
// design artifacts -- while reviewers, security, researcher and builders
// read false, as does an unknown role unless its name ends in "planner".
func TestIsPlannerRoundAdmitsOnlyPlanAndDesignReaders(t *testing.T) {
	t.Parallel()
	reg := plannerTestRegistry(t)
	tests := []struct {
		role string
		want bool
	}{
		{role: "lite-planner", want: true},
		{role: "planner", want: true},
		{role: "relevo-lite-planner", want: true},
		{role: "relevo-planner", want: true},
		{role: "morphic-lite-planner", want: true},
		{role: "morphic-planner", want: true},
		{role: "reviewer", want: false},
		{role: "relevo-reviewer", want: false},
		{role: "morphic-reviewer", want: false},
		{role: "morphic-reviewer-skills", want: false},
		{role: "security", want: false},
		{role: "researcher", want: false},
		{role: "builder", want: false},
		{role: "relevo-builder", want: false},
		{role: "custom-planner", want: true},
		{role: "custom-reviewer", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			t.Parallel()
			b := store.Binding{Role: tt.role}
			if got := IsPlannerRound(reg, b); got != tt.want {
				t.Errorf("IsPlannerRound(%q) = %v, want %v", tt.role, got, tt.want)
			}
		})
	}
	if got := IsPlannerRound(nil, store.Binding{Role: "custom-planner"}); !got {
		t.Errorf("IsPlannerRound(nil registry, custom-planner) = false, want true")
	}
}

// TestSwitchGatesLeaveTheSwitchingBindingsOwnSlotOut pins the mid-round
// switch's count: a binding live on a pool capped at one hands its slot over,
// so its own round must not cap the pool's other model against it.
func TestSwitchGatesLeaveTheSwitchingBindingsOwnSlotOut(t *testing.T) {
	set := candidateSet(t, `[
	  {"name":"a","harness":"opencode","provider":"p","model":"a#xhigh","roles":["builder"],"max_concurrency":1},
	  {"name":"b","harness":"opencode","provider":"p","model":"b#xhigh","roles":["builder"],"max_concurrency":1}
	]`)
	st := storetest.New(t)
	mine := inflightBinding("mine", "opencode/p/a#xhigh", nil)
	if err := st.Save(mine); err != nil {
		t.Fatal(err)
	}
	for _, g := range switchGates(Runtime{Store: st, Candidates: set}, mine) {
		if g.Kind == availability.ConcurrencyLimit {
			t.Errorf("gate = %+v: the switching binding capped its own pool", g)
		}
	}
}
