package workflow

import (
	"reflect"
	"strings"
	"testing"
)

// tdef builds a definition with the given start and steps, and a required
// plans input so a for-each over plans is well formed.
func tdef(start string, steps map[string]Step) Definition {
	return Definition{
		Name:   "t",
		Inputs: Inputs{Plans: InputRequired},
		Start:  start,
		Steps:  steps,
	}
}

// runningState is a fresh running state with the maps a transition writes.
func runningState() State {
	return State{
		Status:  StatusRunning,
		Visits:  map[string]int{},
		Iter:    map[string]Iter{},
		Results: map[string]Result{},
	}
}

// awaitingRun is a running state waiting on a run step.
func awaitingRun(step, member string, round int) State {
	s := runningState()
	s.At = step
	s.Awaiting = Awaiting{Step: step, Member: member, Round: round}
	return s
}

// awaitingCheck is a running state waiting on a check step.
func awaitingCheck(step string, run int) State {
	s := runningState()
	s.At = step
	s.Awaiting = Awaiting{Step: step, Run: run}
	return s
}

// only returns the single action, failing when there is not exactly one.
func only(t *testing.T, actions []Action) Action {
	t.Helper()
	if len(actions) != 1 {
		t.Fatalf("want one action, got %d: %+v", len(actions), actions)
	}
	return actions[0]
}

func TestStartSendsTheBuilderTheFirstPlan(t *testing.T) {
	s, actions := Start(Default(), StartInputs{Plans: []string{"plan-1.md"}})
	a := only(t, actions)
	if a.Kind != ActionSend || a.Step != "build" || a.Actor != "builder" || a.Seed != "{{plans.current}}" {
		t.Fatalf("action = %+v", a)
	}
	if s.At != "build" {
		t.Fatalf("at = %q", s.At)
	}
	if want := (Awaiting{Step: "build", Member: "builder"}); !reflect.DeepEqual(s.Awaiting, want) {
		t.Fatalf("awaiting = %+v, want %+v", s.Awaiting, want)
	}
	if got := s.Iter["plans"]; got.Index != 0 || !reflect.DeepEqual(got.Items, []string{"plan-1.md"}) {
		t.Fatalf("iter = %+v", got)
	}
}

func TestStartWithNoPlansGoesToScan(t *testing.T) {
	s, actions := Start(Default(), StartInputs{})
	a := only(t, actions)
	if a.Kind != ActionSend || a.Step != "scan" || a.Actor != "security" {
		t.Fatalf("action = %+v", a)
	}
	if s.At != "scan" {
		t.Fatalf("at = %q", s.At)
	}
}

func TestNextRunDoneFollowsDone(t *testing.T) {
	def := tdef("a", map[string]Step{
		"a": {Run: "builder", On: map[string]Target{"done": StepTarget("b")}},
		"b": {Run: "builder"},
	})
	e := Event{Kind: EventStepClosed, Step: "a", Member: "builder", Round: 1, Status: "done"}
	s, actions := Next(def, awaitingRun("a", "builder", 1), e)
	if s.At != "b" {
		t.Fatalf("at = %q", s.At)
	}
	if a := only(t, actions); a.Kind != ActionSend || a.Step != "b" {
		t.Fatalf("action = %+v", a)
	}
}

func TestNextRunOneOfFollowsTheValue(t *testing.T) {
	def := tdef("r", map[string]Step{
		"r": {Run: "reviewer", On: map[string]Target{"verdict=pass": StepTarget("p"), "verdict=changes": StepTarget("c")}},
		"p": {Run: "builder"},
		"c": {Run: "builder"},
	})
	e := Event{Kind: EventStepClosed, Step: "r", Member: "reviewer", Round: 2, Status: "done",
		Outcomes: map[string]string{"verdict": "changes"}}
	s, _ := Next(def, awaitingRun("r", "reviewer", 2), e)
	if s.At != "c" {
		t.Fatalf("at = %q", s.At)
	}
	if got := s.Results["r"].Outcomes["verdict"]; got != "changes" {
		t.Fatalf("recorded verdict = %q", got)
	}
}

func TestNextRunCountZeroAndAboveZero(t *testing.T) {
	def := tdef("r", map[string]Step{
		"r": {Run: "security", On: map[string]Target{"findings=0": StepTarget("z"), "findings>0": StepTarget("g")}},
		"z": {Run: "builder"},
		"g": {Run: "builder"},
	})
	for _, tc := range []struct{ count, want string }{{"0", "z"}, {"3", "g"}} {
		e := Event{Kind: EventStepClosed, Step: "r", Member: "security", Round: 1, Status: "done",
			Outcomes: map[string]string{"findings": tc.count}}
		s, _ := Next(def, awaitingRun("r", "security", 1), e)
		if s.At != tc.want {
			t.Fatalf("count %s: at = %q, want %q", tc.count, s.At, tc.want)
		}
	}
}

func TestNextRunUnmatchedStatusHaltsWithTheRunnersReason(t *testing.T) {
	def := tdef("r", map[string]Step{
		"r": {Run: "builder", On: map[string]Target{"status=halted": StepTarget("h"), "done": StepTarget("d")}},
		"h": {Run: "builder"},
		"d": {Run: "builder"},
	})
	e := Event{Kind: EventStepClosed, Step: "r", Member: "builder", Round: 1, Status: "blocked", Reason: "out of quota"}
	s, actions := Next(def, awaitingRun("r", "builder", 1), e)
	if s.Status != StatusHalted || s.Reason != "r blocked: out of quota" {
		t.Fatalf("status %q reason %q", s.Status, s.Reason)
	}
	if a := only(t, actions); a.Kind != ActionHalt || a.Reason != "r blocked: out of quota" {
		t.Fatalf("action = %+v", a)
	}
}

func TestNextRunMatchedStatusIsFollowed(t *testing.T) {
	def := tdef("r", map[string]Step{
		"r": {Run: "builder", On: map[string]Target{"status=halted": StepTarget("h"), "done": StepTarget("d")}},
		"h": {Run: "builder"},
		"d": {Run: "builder"},
	})
	e := Event{Kind: EventStepClosed, Step: "r", Member: "builder", Round: 1, Status: "halted", Reason: "x"}
	s, _ := Next(def, awaitingRun("r", "builder", 1), e)
	if s.At != "h" {
		t.Fatalf("at = %q", s.At)
	}
}

func TestNextCheckGreenAndRed(t *testing.T) {
	def := tdef("c", map[string]Step{
		"c": {Check: "make check", On: map[string]Target{"green": StepTarget("g"), "red": StepTarget("r")}},
		"g": {Run: "builder"},
		"r": {Run: "builder"},
	})
	for _, tc := range []struct{ result, want string }{{"green", "g"}, {"red", "r"}} {
		e := Event{Kind: EventCheckClosed, Step: "c", Run: 4, Result: tc.result, Log: "log-4"}
		s, _ := Next(def, awaitingCheck("c", 4), e)
		if s.At != tc.want {
			t.Fatalf("result %s: at = %q, want %q", tc.result, s.At, tc.want)
		}
		if got := s.Results["c"].Artifacts["log"]; len(got) != 1 || got[0] != "log-4" {
			t.Fatalf("result %s: log = %v", tc.result, got)
		}
	}
}

func TestNextEmptyCheckIsGreenWithoutAnAction(t *testing.T) {
	def := tdef("c", map[string]Step{
		"c": {Check: "{{params.gate}}", On: map[string]Target{"green": StepTarget("g"), "red": StepTarget("r")}},
		"g": {Run: "builder"},
		"r": {Run: "builder"},
	})
	def.Params = map[string]Param{"gate": {Kind: ParamString, Str: ""}}
	s, actions := Start(def, StartInputs{})
	if a := only(t, actions); a.Kind != ActionSend || a.Step != "g" {
		t.Fatalf("action = %+v", a)
	}
	if _, ok := s.Results["c"]; ok {
		t.Fatalf("the empty check recorded a result: %+v", s.Results["c"])
	}
}

func TestNextForEachNextAndEmpty(t *testing.T) {
	def := tdef("a", map[string]Step{
		"a": {Run: "builder", On: map[string]Target{"done": StepTarget("f")}},
		"f": {ForEach: "plans", On: map[string]Target{"next": StepTarget("n"), "empty": StepTarget("e")}},
		"n": {Run: "builder", On: map[string]Target{"done": StepTarget("f")}},
		"e": {Run: "builder"},
	})
	s, _ := Start(def, StartInputs{Plans: []string{"i1", "i2"}})
	closed := func(step string) Event {
		return Event{Kind: EventStepClosed, Step: step, Member: "builder", Round: 0, Status: "done"}
	}
	s, _ = Next(def, s, closed("a"))
	if s.At != "n" || s.Iter["f"].Index != 0 {
		t.Fatalf("first: at = %q index = %d", s.At, s.Iter["f"].Index)
	}
	s, _ = Next(def, s, closed("n"))
	if s.At != "n" || s.Iter["f"].Index != 1 {
		t.Fatalf("second: at = %q index = %d", s.At, s.Iter["f"].Index)
	}
	s, _ = Next(def, s, closed("n"))
	if s.At != "e" || s.Iter["f"].Index != -1 {
		t.Fatalf("empty: at = %q index = %d", s.At, s.Iter["f"].Index)
	}
}

func TestNextWhenTrueAndFalse(t *testing.T) {
	steps := map[string]Step{
		"w": {When: "{{params.x}}", On: map[string]Target{"true": StepTarget("t"), "false": StepTarget("f")}},
		"t": {Run: "builder"},
		"f": {Run: "builder"},
	}
	for _, tc := range []struct {
		x    bool
		want string
	}{{true, "t"}, {false, "f"}} {
		def := tdef("w", steps)
		def.Params = map[string]Param{"x": {Kind: ParamBool, Bool: tc.x}}
		s, _ := Start(def, StartInputs{})
		if s.At != tc.want {
			t.Fatalf("x=%v: at = %q, want %q", tc.x, s.At, tc.want)
		}
	}
}

func TestNextHaltTargetRendersParams(t *testing.T) {
	def := tdef("a", map[string]Step{
		"a": {Run: "builder", On: map[string]Target{"done": HaltTarget("gave up after {{params.tries}} tries")}},
	})
	def.Params = map[string]Param{"tries": {Kind: ParamInt, Int: 3}}
	e := Event{Kind: EventStepClosed, Step: "a", Member: "builder", Round: 0, Status: "done"}
	s, actions := Next(def, awaitingRun("a", "builder", 0), e)
	if s.Reason != "gave up after 3 tries" {
		t.Fatalf("reason = %q", s.Reason)
	}
	if a := only(t, actions); a.Kind != ActionHalt || a.Reason != "gave up after 3 tries" {
		t.Fatalf("action = %+v", a)
	}
}

func TestNextDoneFinishes(t *testing.T) {
	def := tdef("a", map[string]Step{
		"a": {Run: "builder", On: map[string]Target{"done": DoneTarget()}},
	})
	e := Event{Kind: EventStepClosed, Step: "a", Member: "builder", Round: 0, Status: "done"}
	s, actions := Next(def, awaitingRun("a", "builder", 0), e)
	if s.Status != StatusDone {
		t.Fatalf("status = %q", s.Status)
	}
	if a := only(t, actions); a.Kind != ActionFinish {
		t.Fatalf("action = %+v", a)
	}
}

// budgetDef is a definition whose only run, "a", carries the budget and whose
// done edge loops back to "a"; the start, "p", reaches it.
func budgetDef(b Budget) Definition {
	return tdef("p", map[string]Step{
		"p": {Run: "builder", On: map[string]Target{"done": StepTarget("a")}},
		"a": {Run: "builder", Budget: &b, On: map[string]Target{"done": StepTarget("a")}},
	})
}

func TestNextBudgetPastMaxGoesToThen(t *testing.T) {
	def := budgetDef(Budget{Max: Limit{Count: 1}, Per: Per{Chain: true}, Then: DoneTarget()})
	s := awaitingRun("p", "builder", 0)
	s.Visits["a"] = 1
	s, actions := Next(def, s, Event{Kind: EventStepClosed, Step: "p", Member: "builder", Round: 0, Status: "done"})
	if s.Status != StatusDone {
		t.Fatalf("status = %q", s.Status)
	}
	if a := only(t, actions); a.Kind != ActionFinish {
		t.Fatalf("action = %+v", a)
	}
}

func TestNextBudgetMaxZeroGoesStraightToThen(t *testing.T) {
	def := budgetDef(Budget{Max: Limit{Count: 0}, Per: Per{Chain: true}, Then: DoneTarget()})
	e := Event{Kind: EventStepClosed, Step: "p", Member: "builder", Round: 0, Status: "done"}
	s, actions := Next(def, awaitingRun("p", "builder", 0), e)
	if s.Status != StatusDone {
		t.Fatalf("status = %q", s.Status)
	}
	if a := only(t, actions); a.Kind != ActionFinish {
		t.Fatalf("action = %+v", a)
	}
}

func TestNextBudgetResetsWhenForEachAdvances(t *testing.T) {
	def := tdef("f", map[string]Step{
		"f": {ForEach: "plans", On: map[string]Target{"next": StepTarget("a"), "empty": DoneTarget()}},
		"a": {Run: "builder", Budget: &Budget{Max: Limit{Count: 1}, Per: Per{Steps: []string{"f"}}, Then: StepTarget("t")},
			On: map[string]Target{"done": StepTarget("f")}},
		"t": {Run: "builder"},
	})
	s, _ := Start(def, StartInputs{Plans: []string{"i1", "i2"}})
	if s.At != "a" || s.Visits["a"] != 1 {
		t.Fatalf("start: at = %q visits = %d", s.At, s.Visits["a"])
	}
	e := Event{Kind: EventStepClosed, Step: "a", Member: "builder", Round: 0, Status: "done"}
	s, _ = Next(def, s, e)
	if s.Visits["a"] != 1 || s.At != "a" {
		t.Fatalf("after advancing: visits = %d at = %q, want 1 and a", s.Visits["a"], s.At)
	}
}

// TestBudgetPerForEachKeepsTheCountWhenEmptied pins the scope reset to an
// advance: entering the for-each on its empty edge leaves the budgets it names
// as they were, so the count the last plan spent survives into the walk's end.
func TestBudgetPerForEachKeepsTheCountWhenEmptied(t *testing.T) {
	def := tdef("f", map[string]Step{
		"f": {ForEach: "plans", On: map[string]Target{"next": StepTarget("a"), "empty": StepTarget("e")}},
		"a": {Run: "builder", Budget: &Budget{Max: Limit{Count: 3}, Per: Per{Steps: []string{"f"}}, Then: StepTarget("t")},
			On: map[string]Target{"done": StepTarget("f")}},
		"e": {Run: "builder"},
		"t": {Run: "builder"},
	})
	s, _ := Start(def, StartInputs{Plans: []string{"i1"}})
	if s.At != "a" || s.Visits["a"] != 1 {
		t.Fatalf("start: at = %q visits = %d, want a and 1", s.At, s.Visits["a"])
	}
	s, _ = Next(def, s, Event{Kind: EventStepClosed, Step: "a", Member: "builder", Round: 0, Status: "done"})
	if s.At != "e" {
		t.Fatalf("at = %q, want e: the single plan is exhausted", s.At)
	}
	if s.Visits["a"] != 1 {
		t.Errorf("visits = %d, want 1: emptying the for-each must not reset the scope it names", s.Visits["a"])
	}
}

func TestNextBudgetResetsWhenAPerStepIsEntered(t *testing.T) {
	def := tdef("q", map[string]Step{
		"q": {Run: "builder", On: map[string]Target{"done": StepTarget("p")}},
		"p": {Run: "builder", On: map[string]Target{"done": StepTarget("a")}},
		"a": {Run: "builder", Budget: &Budget{Max: Limit{Count: 5}, Per: Per{Steps: []string{"p"}}, Then: DoneTarget()},
			On: map[string]Target{"done": StepTarget("a")}},
	})
	s := awaitingRun("q", "builder", 0)
	s.Visits["a"] = 3
	s, _ = Next(def, s, Event{Kind: EventStepClosed, Step: "q", Member: "builder", Round: 0, Status: "done"})
	if s.Visits["a"] != 0 {
		t.Fatalf("visits = %d, want 0", s.Visits["a"])
	}
	if s.At != "p" {
		t.Fatalf("at = %q, want p", s.At)
	}
}

func TestNextBudgetPerChainNeverResets(t *testing.T) {
	def := budgetDef(Budget{Max: Limit{Count: 10}, Per: Per{Chain: true}, Then: DoneTarget()})
	s := awaitingRun("p", "builder", 0)
	s.Visits["a"] = 3
	s, _ = Next(def, s, Event{Kind: EventStepClosed, Step: "p", Member: "builder", Round: 0, Status: "done"})
	if s.Visits["a"] != 4 {
		t.Fatalf("visits = %d, want 4", s.Visits["a"])
	}
	if s.At != "a" {
		t.Fatalf("at = %q, want a", s.At)
	}
}

// TestNextBudgetRedirectLoopHalts pins the cap against the runaway the review
// found: two run steps whose chain-scoped budgets are permanently exhausted
// redirect to each other forever. Validate accepts the definition, so the
// engine itself must stop it.
func TestNextBudgetRedirectLoopHalts(t *testing.T) {
	def := tdef("c", map[string]Step{
		"c": {Run: "builder", On: map[string]Target{"done": StepTarget("d")}},
		"d": {Run: "builder", Budget: &Budget{Max: Limit{Count: 0}, Per: Per{Chain: true}, Then: StepTarget("e")},
			On: map[string]Target{"done": DoneTarget()}},
		"e": {Run: "builder", Budget: &Budget{Max: Limit{Count: 0}, Per: Per{Chain: true}, Then: StepTarget("d")},
			On: map[string]Target{"done": DoneTarget()}},
	})
	e := Event{Kind: EventStepClosed, Step: "c", Member: "builder", Round: 0, Status: "done"}
	s, actions := Next(def, awaitingRun("c", "builder", 0), e)
	if s.Status != StatusHalted {
		t.Fatalf("status = %q", s.Status)
	}
	if !strings.HasPrefix(s.Reason, "control walk exceeded") {
		t.Fatalf("reason = %q, want the cap reason", s.Reason)
	}
	if !strings.Contains(s.Reason, "d") && !strings.Contains(s.Reason, "e") {
		t.Fatalf("reason = %q, want it to name the step", s.Reason)
	}
	if a := only(t, actions); a.Kind != ActionHalt {
		t.Fatalf("action = %+v", a)
	}
}

// TestNextBudgetPastMaxRedirectsToAStep guards the other side of the cap: a
// single exhausted budget that redirects to a step must still enter and run it,
// not halt.
func TestNextBudgetPastMaxRedirectsToAStep(t *testing.T) {
	def := tdef("q", map[string]Step{
		"q": {Run: "builder", On: map[string]Target{"done": StepTarget("a")}},
		"a": {Run: "builder", Budget: &Budget{Max: Limit{Count: 1}, Per: Per{Chain: true}, Then: StepTarget("b")},
			On: map[string]Target{"done": DoneTarget()}},
		"b": {Run: "builder"},
	})
	s := awaitingRun("q", "builder", 0)
	s.Visits["a"] = 1
	s, actions := Next(def, s, Event{Kind: EventStepClosed, Step: "q", Member: "builder", Round: 0, Status: "done"})
	if s.Status != StatusRunning {
		t.Fatalf("status = %q", s.Status)
	}
	if a := only(t, actions); a.Kind != ActionSend || a.Step != "b" {
		t.Fatalf("action = %+v, want a send to b", a)
	}
}

// TestNextEmptyCheckRouteConsumesTheWalk pins the second unguarded route: an
// empty check whose green edge loops back to itself must halt at the cap, not
// recurse. The definition is deliberately not validated, as Next is exported.
func TestNextEmptyCheckRouteConsumesTheWalk(t *testing.T) {
	def := tdef("c", map[string]Step{
		"c": {Check: "{{params.gate}}", On: map[string]Target{"green": StepTarget("c")}},
	})
	def.Params = map[string]Param{"gate": {Kind: ParamString, Str: ""}}
	s, actions := Start(def, StartInputs{})
	if s.Status != StatusHalted {
		t.Fatalf("status = %q", s.Status)
	}
	if !strings.Contains(s.Reason, "control walk exceeded") {
		t.Fatalf("reason = %q, want the cap reason", s.Reason)
	}
	if a := only(t, actions); a.Kind != ActionHalt {
		t.Fatalf("action = %+v", a)
	}
}

func TestNextIgnoresAnotherStepMemberOrRound(t *testing.T) {
	def := tdef("a", map[string]Step{
		"a": {Run: "builder", On: map[string]Target{"done": StepTarget("b")}},
		"b": {Run: "builder"},
	})
	before := awaitingRun("a", "builder", 1)
	events := []Event{
		{Kind: EventStepClosed, Step: "x", Member: "builder", Round: 1, Status: "done"},
		{Kind: EventStepClosed, Step: "a", Member: "reviewer", Round: 1, Status: "done"},
		{Kind: EventStepClosed, Step: "a", Member: "builder", Round: 2, Status: "done"},
	}
	for _, e := range events {
		s, actions := Next(def, before, e)
		if len(actions) != 0 {
			t.Fatalf("%+v: actions = %+v", e, actions)
		}
		if !reflect.DeepEqual(s, before) {
			t.Fatalf("%+v: state changed to %+v", e, s)
		}
	}
}

func TestNextIgnoresAnotherCheckRun(t *testing.T) {
	def := tdef("c", map[string]Step{
		"c": {Check: "make check", On: map[string]Target{"green": DoneTarget()}},
	})
	before := awaitingCheck("c", 7)
	s, actions := Next(def, before, Event{Kind: EventCheckClosed, Step: "c", Run: 8, Result: "green"})
	if len(actions) != 0 || !reflect.DeepEqual(s, before) {
		t.Fatalf("state = %+v actions = %+v", s, actions)
	}
}

func TestNextReplayedCloseDoesNotAdvanceTwice(t *testing.T) {
	def := tdef("a", map[string]Step{
		"a": {Run: "builder", On: map[string]Target{"done": StepTarget("b")}},
		"b": {Run: "builder"},
	})
	e := Event{Kind: EventStepClosed, Step: "a", Member: "builder", Round: 1, Status: "done"}
	s, _ := Next(def, awaitingRun("a", "builder", 1), e)
	again, actions := Next(def, s, e)
	if len(actions) != 0 {
		t.Fatalf("replay produced %+v", actions)
	}
	if !reflect.DeepEqual(again, s) {
		t.Fatalf("replay changed the state:\n got %+v\nwant %+v", again, s)
	}
}

func TestNextTerminalIsInert(t *testing.T) {
	def := tdef("a", map[string]Step{
		"a": {Run: "builder", On: map[string]Target{"done": StepTarget("b")}},
		"b": {Run: "builder"},
	})
	before := awaitingRun("a", "builder", 1)
	before.Status = StatusHalted
	s, actions := Next(def, before, Event{Kind: EventStepClosed, Step: "a", Member: "builder", Round: 1, Status: "done"})
	if len(actions) != 0 || !reflect.DeepEqual(s, before) {
		t.Fatalf("state = %+v actions = %+v", s, actions)
	}
}

func TestNextSendZeroesTheAwaitingRound(t *testing.T) {
	def := tdef("a", map[string]Step{
		"a": {Run: "builder", On: map[string]Target{"done": StepTarget("b")}},
		"b": {Run: "builder"},
	})
	e := Event{Kind: EventStepClosed, Step: "a", Member: "builder", Round: 5, Status: "done"}
	s, actions := Next(def, awaitingRun("a", "builder", 5), e)
	if s.Awaiting.Round != 0 {
		t.Fatalf("round = %d, want 0", s.Awaiting.Round)
	}
	if a := only(t, actions); a.Kind != ActionSend {
		t.Fatalf("action = %+v", a)
	}
}

func TestNextControlWalkCapHalts(t *testing.T) {
	def := tdef("a", map[string]Step{
		"a": {When: "{{params.x}}", On: map[string]Target{"true": StepTarget("b")}},
		"b": {When: "{{params.x}}", On: map[string]Target{"true": StepTarget("a")}},
	})
	def.Params = map[string]Param{"x": {Kind: ParamBool, Bool: true}}
	s, actions := Start(def, StartInputs{})
	if s.Status != StatusHalted {
		t.Fatalf("status = %q", s.Status)
	}
	if !strings.Contains(s.Reason, "b") {
		t.Fatalf("reason = %q, want it to name the step", s.Reason)
	}
	if a := only(t, actions); a.Kind != ActionHalt {
		t.Fatalf("action = %+v", a)
	}
}

func TestNextNeedsYouHalts(t *testing.T) {
	def := tdef("a", map[string]Step{"a": {Run: "builder"}})
	e := Event{Kind: EventNeedsYou, Step: "a", Member: "builder", Round: 1, Reason: "needs a decision"}
	s, actions := Next(def, awaitingRun("a", "builder", 1), e)
	if s.Status != StatusHalted || s.Reason != "needs a decision" {
		t.Fatalf("status %q reason %q", s.Status, s.Reason)
	}
	if a := only(t, actions); a.Kind != ActionHalt || a.Reason != "needs a decision" {
		t.Fatalf("action = %+v", a)
	}
}

func TestNextStoppedStops(t *testing.T) {
	def := tdef("a", map[string]Step{"a": {Run: "builder"}})
	e := Event{Kind: EventStopped, Step: "a", Member: "builder", Round: 1}
	s, actions := Next(def, awaitingRun("a", "builder", 1), e)
	if s.Status != StatusStopped {
		t.Fatalf("status = %q", s.Status)
	}
	if a := only(t, actions); a.Kind != ActionStop {
		t.Fatalf("action = %+v", a)
	}
}
