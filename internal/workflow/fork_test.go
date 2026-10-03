package workflow

import (
	"reflect"
	"testing"
)

// forkDef builds a definition with a fork step and a builder writer step so
// rule 7 validation and execution succeed.
func forkDef(f Step) Definition {
	return tdef("split", map[string]Step{
		"split": f,
		"build": {Run: "builder", On: map[string]Target{"done": DoneTarget()}},
	})
}

// awaitingFork builds a running state awaiting a fork step's children.
func awaitingFork(step string, children []string) State {
	s := runningState()
	s.At = step
	s.Awaiting = Awaiting{Step: step, Children: children}
	return s
}

func assertForkAwaitingAction(t *testing.T, s State, actions []Action, wantKeys []string, wantSpecs []ChildSpec) {
	t.Helper()
	if s.At != "split" || s.Awaiting.Step != "split" {
		t.Fatalf("at = %q, awaiting = %q", s.At, s.Awaiting.Step)
	}
	if !reflect.DeepEqual(s.Awaiting.Children, wantKeys) {
		t.Fatalf("awaiting children = %v, want %v", s.Awaiting.Children, wantKeys)
	}
	a := only(t, actions)
	if a.Kind != ActionFork || a.Step != "split" {
		t.Fatalf("action = %+v, want fork on split", a)
	}
	if !reflect.DeepEqual(a.Children, wantSpecs) {
		t.Fatalf("children specs = %+v, want %+v", a.Children, wantSpecs)
	}
}

func TestForkStartChildren(t *testing.T) {
	step := Step{
		Fork: &Fork{
			Children: []ForkChild{
				{Workflow: "w1", Task: "t1", Plans: []string{"p1.md"}},
				{Workflow: "w2", Task: "t2", Plans: []string{"p2.md"}},
			},
		},
		On: map[string]Target{"joined": DoneTarget(), "conflict": DoneTarget()},
	}
	s, actions := Start(forkDef(step), StartInputs{})
	assertForkAwaitingAction(t, s, actions, []string{"1", "2"}, []ChildSpec{
		{Key: "1", Workflow: "w1", Task: "t1", Plans: []string{"p1.md"}},
		{Key: "2", Workflow: "w2", Task: "t2", Plans: []string{"p2.md"}},
	})
}

func TestForkStartEachPlans(t *testing.T) {
	step := Step{
		Fork: &Fork{Each: "plans", Workflow: "w"},
		On:   map[string]Target{"joined": DoneTarget(), "conflict": DoneTarget()},
	}
	s, actions := Start(forkDef(step), StartInputs{Plans: []string{"a.md", "b.md"}})
	assertForkAwaitingAction(t, s, actions, []string{"1", "2"}, []ChildSpec{
		{Key: "1", Workflow: "w", Plans: []string{"a.md"}},
		{Key: "2", Workflow: "w", Plans: []string{"b.md"}},
	})
}

func TestForkStartEachArtifact(t *testing.T) {
	step := Step{
		Fork: &Fork{Each: "{{produce.items}}", Workflow: "w"},
		On:   map[string]Target{"joined": DoneTarget(), "conflict": DoneTarget()},
	}
	s := runningState()
	s.Results = ensureResults(s.Results)
	s.Results["produce"] = Result{Artifacts: map[string][]string{"items": {"x.md", "y.md"}}}
	s, actions := enter(forkDef(step), s, "split", 0)
	assertForkAwaitingAction(t, s, actions, []string{"1", "2"}, []ChildSpec{
		{Key: "1", Workflow: "w", Plans: []string{"x.md"}},
		{Key: "2", Workflow: "w", Plans: []string{"y.md"}},
	})
}

func TestForkEmptyEachListRoutesJoined(t *testing.T) {
	step := Step{
		Fork: &Fork{Each: "plans", Workflow: "w"},
		On:   map[string]Target{"joined": DoneTarget(), "conflict": DoneTarget()},
	}
	s, actions := Start(forkDef(step), StartInputs{Plans: []string{}})
	if s.Status != StatusDone {
		t.Fatalf("status = %q, want done", s.Status)
	}
	if a := only(t, actions); a.Kind != ActionFinish {
		t.Fatalf("action = %+v, want finish", a)
	}
	if s.Awaiting.Step != "" || len(s.Awaiting.Children) != 0 {
		t.Fatalf("awaiting = %+v, want cleared", s.Awaiting)
	}
	res, ok := s.Results["split"]
	if !ok || res.Status != "joined" || res.Outcomes["result"] != "joined" {
		t.Fatalf("results = %+v, want joined", res)
	}
}

func TestForkChildEndedOrder(t *testing.T) {
	def := forkDef(Step{
		Fork: &Fork{Each: "plans", Workflow: "w"},
		On:   map[string]Target{"joined": DoneTarget(), "conflict": DoneTarget()},
	})

	t.Run("A then B emits ActionMerge", func(t *testing.T) {
		s := awaitingFork("split", []string{"1", "2"})
		e1 := Event{Kind: EventChildEnded, Step: "split", Child: "1", Status: "done"}
		s1, acts1 := Next(def, s, e1)
		if len(acts1) != 0 || s1.Awaiting.Merging || s1.Awaiting.Ended["1"].Status != "done" {
			t.Fatalf("unexpected state after child 1 ends: acts=%+v awaiting=%+v", acts1, s1.Awaiting)
		}
		e2 := Event{Kind: EventChildEnded, Step: "split", Child: "2", Status: "done"}
		s2, acts2 := Next(def, s1, e2)
		a := only(t, acts2)
		if a.Kind != ActionMerge || a.Step != "split" || !s2.Awaiting.Merging {
			t.Fatalf("action = %+v, merging = %v", a, s2.Awaiting.Merging)
		}
	})

	t.Run("B then A emits ActionMerge", func(t *testing.T) {
		s := awaitingFork("split", []string{"1", "2"})
		e2 := Event{Kind: EventChildEnded, Step: "split", Child: "2", Status: "done"}
		s1, acts1 := Next(def, s, e2)
		if len(acts1) != 0 {
			t.Fatalf("got actions on first child end: %+v", acts1)
		}
		e1 := Event{Kind: EventChildEnded, Step: "split", Child: "1", Status: "done"}
		_, acts2 := Next(def, s1, e1)
		if only(t, acts2).Kind != ActionMerge {
			t.Fatalf("action = %+v, want ActionMerge", acts2)
		}
	})
}

func TestForkChildEndedReplayAndStrangers(t *testing.T) {
	def := forkDef(Step{
		Fork: &Fork{Each: "plans", Workflow: "w"},
		On:   map[string]Target{"joined": DoneTarget(), "conflict": DoneTarget()},
	})

	t.Run("duplicate child_ended is ignored", func(t *testing.T) {
		s := awaitingFork("split", []string{"1", "2"})
		s1, _ := Next(def, s, Event{Kind: EventChildEnded, Step: "split", Child: "1", Status: "done"})
		dup := Event{Kind: EventChildEnded, Step: "split", Child: "1", Status: "stopped"}
		sDup, actsDup := Next(def, s1, dup)
		if len(actsDup) != 0 || !reflect.DeepEqual(sDup, s1) {
			t.Fatalf("state changed on duplicate child_ended: got %+v, want %+v", sDup, s1)
		}
	})

	t.Run("stranger child_ended is ignored", func(t *testing.T) {
		s := awaitingFork("split", []string{"1", "2"})
		stranger := Event{Kind: EventChildEnded, Step: "split", Child: "stranger", Status: "done"}
		sSt, actsSt := Next(def, s, stranger)
		if len(actsSt) != 0 || !reflect.DeepEqual(sSt, s) {
			t.Fatalf("stranger child_ended advanced run: acts=%+v", actsSt)
		}
	})

	t.Run("stranger early merge_closed is ignored", func(t *testing.T) {
		s := awaitingFork("split", []string{"1", "2"})
		early := Event{Kind: EventMergeClosed, Step: "split", Result: "joined"}
		sEarly, actsEarly := Next(def, s, early)
		if len(actsEarly) != 0 || !reflect.DeepEqual(sEarly, s) {
			t.Fatalf("early merge_closed advanced run: acts=%+v", actsEarly)
		}
	})
}

func TestForkChildHaltedWiredAndUnwired(t *testing.T) {
	t.Run("one child halted with on.halted wired", func(t *testing.T) {
		def := forkDef(Step{
			Fork: &Fork{Children: []ForkChild{{Workflow: "w1"}, {Workflow: "w2"}}},
			On: map[string]Target{
				"joined":   DoneTarget(),
				"conflict": DoneTarget(),
				"halted":   StepTarget("build"),
			},
		})
		s := awaitingFork("split", []string{"1", "2"})
		s, _ = Next(def, s, Event{Kind: EventChildEnded, Step: "split", Child: "1", Status: "halted", Reason: "bad test"})
		s, actions := Next(def, s, Event{Kind: EventChildEnded, Step: "split", Child: "2", Status: "done"})
		if s.At != "build" || s.Results["split"].Outcomes["result"] != "halted" {
			t.Fatalf("at = %q, outcomes = %+v", s.At, s.Results["split"].Outcomes)
		}
		a := only(t, actions)
		if a.Kind != ActionSend || a.Step != "build" {
			t.Fatalf("action = %+v, want send build", a)
		}
	})

	t.Run("one child halted with on.halted unwired", func(t *testing.T) {
		def := forkDef(Step{
			Fork: &Fork{Children: []ForkChild{{Workflow: "w1"}, {Workflow: "w2"}}},
			On:   map[string]Target{"joined": DoneTarget(), "conflict": DoneTarget()},
		})
		s := awaitingFork("split", []string{"1", "2"})
		s, _ = Next(def, s, Event{Kind: EventChildEnded, Step: "split", Child: "1", Status: "halted", Reason: "bad lint"})
		s, actions := Next(def, s, Event{Kind: EventChildEnded, Step: "split", Child: "2", Status: "done"})
		if s.Status != StatusHalted || s.Reason != "bad lint" {
			t.Fatalf("status = %q, reason = %q", s.Status, s.Reason)
		}
		if a := only(t, actions); a.Kind != ActionHalt || a.Reason != "bad lint" {
			t.Fatalf("action = %+v", a)
		}
	})
}

func TestForkChildStoppedAndMultipleHalted(t *testing.T) {
	t.Run("stopped child reason formats child k stopped", func(t *testing.T) {
		def := forkDef(Step{
			Fork: &Fork{Children: []ForkChild{{Workflow: "w1"}, {Workflow: "w2"}}},
			On:   map[string]Target{"joined": DoneTarget(), "conflict": DoneTarget()},
		})
		s := awaitingFork("split", []string{"1", "2"})
		s, _ = Next(def, s, Event{Kind: EventChildEnded, Step: "split", Child: "1", Status: "stopped"})
		s, actions := Next(def, s, Event{Kind: EventChildEnded, Step: "split", Child: "2", Status: "done"})
		if s.Reason != "child 1 stopped" {
			t.Fatalf("reason = %q, want 'child 1 stopped'", s.Reason)
		}
		if a := only(t, actions); a.Reason != "child 1 stopped" {
			t.Fatalf("action reason = %q", a.Reason)
		}
	})

	t.Run("first halted child reason in key order when two halt", func(t *testing.T) {
		def := forkDef(Step{
			Fork: &Fork{Children: []ForkChild{{Workflow: "w1"}, {Workflow: "w2"}}},
			On:   map[string]Target{"joined": DoneTarget(), "conflict": DoneTarget()},
		})
		s := awaitingFork("split", []string{"1", "2"})
		s, _ = Next(def, s, Event{Kind: EventChildEnded, Step: "split", Child: "2", Status: "halted", Reason: "reason-2"})
		s, actions := Next(def, s, Event{Kind: EventChildEnded, Step: "split", Child: "1", Status: "halted", Reason: "reason-1"})
		if s.Reason != "reason-1" {
			t.Fatalf("reason = %q, want 'reason-1'", s.Reason)
		}
		if a := only(t, actions); a.Reason != "reason-1" {
			t.Fatalf("action reason = %q", a.Reason)
		}
	})
}

func TestForkMergeClosed(t *testing.T) {
	def := forkDef(Step{
		Fork: &Fork{Children: []ForkChild{{Workflow: "w1"}}},
		On:   map[string]Target{"joined": DoneTarget(), "conflict": StepTarget("build")},
	})

	t.Run("joined clears awaiting and completes", func(t *testing.T) {
		s := awaitingFork("split", []string{"1"})
		s.Awaiting.Merging = true
		s, actions := Next(def, s, Event{Kind: EventMergeClosed, Step: "split", Result: "joined"})
		if s.Status != StatusDone || s.Awaiting.Step != "" || s.Awaiting.Merging {
			t.Fatalf("status = %q, awaiting = %+v", s.Status, s.Awaiting)
		}
		if a := only(t, actions); a.Kind != ActionFinish {
			t.Fatalf("action = %+v, want finish", a)
		}
	})

	t.Run("conflict preserves results and routes", func(t *testing.T) {
		s := awaitingFork("split", []string{"1"})
		s.Awaiting.Merging = true
		e := Event{
			Kind:      EventMergeClosed,
			Step:      "split",
			Result:    "conflict",
			Artifacts: map[string][]string{"conflict": {"conflict.diff"}},
		}
		s, actions := Next(def, s, e)
		if s.At != "build" {
			t.Fatalf("at = %q, want build", s.At)
		}
		res := s.Results["split"]
		if res.Status != "conflict" || res.Outcomes["result"] != "conflict" {
			t.Fatalf("results = %+v", res)
		}
		if !reflect.DeepEqual(res.Artifacts["conflict"], []string{"conflict.diff"}) {
			t.Fatalf("conflict artifacts = %+v", res.Artifacts)
		}
		if a := only(t, actions); a.Kind != ActionSend {
			t.Fatalf("action = %+v, want send build", a)
		}
	})

	t.Run("unmatched result halts", func(t *testing.T) {
		s := awaitingFork("split", []string{"1"})
		s.Awaiting.Merging = true
		s, actions := Next(def, s, Event{Kind: EventMergeClosed, Step: "split", Result: "strange"})
		if s.Status != StatusHalted || s.Reason != "split strange: no edge matches" {
			t.Fatalf("status = %q, reason = %q", s.Status, s.Reason)
		}
		if a := only(t, actions); a.Kind != ActionHalt || a.Reason != "split strange: no edge matches" {
			t.Fatalf("action = %+v", a)
		}
	})
}

func reopenBaseState() State {
	s := runningState()
	s.Status = StatusHalted
	s.Reason = "child 1 failed"
	s.At = "split"
	s.Awaiting = Awaiting{
		Step:     "split",
		Children: []string{"1", "2"},
		Ended: map[string]ChildEnd{
			"1": {Status: "halted", Reason: "child 1 failed"},
			"2": {Status: "done"},
		},
		Merging: false,
	}
	return s
}

func TestReopenForkAllowed(t *testing.T) {
	s := reopenBaseState()
	reopened, ok := ReopenFork(s, "1")
	if !ok {
		t.Fatal("ReopenFork failed, want ok")
	}
	if reopened.Status != StatusRunning || reopened.Reason != "" {
		t.Fatalf("status = %q, reason = %q", reopened.Status, reopened.Reason)
	}
	if _, exists := reopened.Awaiting.Ended["1"]; exists {
		t.Fatal("child 1 still in Ended map")
	}
	if reopened.Awaiting.Ended["2"].Status != "done" || reopened.Awaiting.Merging {
		t.Fatalf("child 2 ended: %+v, merging: %v", reopened.Awaiting.Ended["2"], reopened.Awaiting.Merging)
	}
}

func TestReopenForkRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(State) State
		child string
	}{
		{"not halted", func(s State) State { s.Status = StatusRunning; return s }, "1"},
		{"wrong step", func(s State) State { s.At = "other"; return s }, "1"},
		{"done child", func(s State) State { return s }, "2"},
		{"missing child", func(s State) State { return s }, "ghost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.setup(reopenBaseState())
			if _, ok := ReopenFork(s, tc.child); ok {
				t.Fatalf("ReopenFork succeeded for %s", tc.name)
			}
		})
	}
}

func TestForkEventsAndActionsEncodeDecode(t *testing.T) {
	eMerge := Event{
		Kind:      EventMergeClosed,
		Step:      "split",
		Result:    "conflict",
		Artifacts: map[string][]string{"conflict": {"diff.patch"}},
	}
	if dec, err := DecodeEvent(EncodeEvent(eMerge)); err != nil || !reflect.DeepEqual(dec, eMerge) {
		t.Fatalf("DecodeEvent(merge_closed): got %+v, err %v", dec, err)
	}

	eChild := Event{Kind: EventChildEnded, Step: "split", Child: "1", Status: "halted", Reason: "boom"}
	if dec, err := DecodeEvent(EncodeEvent(eChild)); err != nil || !reflect.DeepEqual(dec, eChild) {
		t.Fatalf("DecodeEvent(child_ended): got %+v, err %v", dec, err)
	}

	aFork := Action{
		Kind: ActionFork,
		Step: "split",
		Children: []ChildSpec{
			{Key: "1", Workflow: "w1", Task: "t1", Plans: []string{"p1.md"}},
		},
	}
	if dec, err := DecodeAction(EncodeAction(aFork)); err != nil || !reflect.DeepEqual(dec, aFork) {
		t.Fatalf("DecodeAction(fork): got %+v, err %v", dec, err)
	}

	aMerge := Action{Kind: ActionMerge, Step: "split"}
	if dec, err := DecodeAction(EncodeAction(aMerge)); err != nil || !reflect.DeepEqual(dec, aMerge) {
		t.Fatalf("DecodeAction(merge): got %+v, err %v", dec, err)
	}
}

func TestForkValidation(t *testing.T) {
	env := shippedEnv()
	env.Given = &Given{Plans: true}
	env.Workflow = func(name string) (Definition, bool) {
		return mustParse(t, "name: child\ninputs: { plans: required }\nstart: a\nsteps:\n  a: { run: builder, on: { done: done } }"), true
	}

	t.Run("fork without a writer is refused", func(t *testing.T) {
		raw := `name: sample
inputs: { plans: required }
start: f
steps:
  f: { fork: { each: plans, workflow: child }, on: { joined: done, conflict: done } }`
		problems := Validate(mustParse(t, raw), env)
		if !hasProblem(problems, RuleFork, "f") {
			t.Fatalf("Validate = %v, want RuleFork on f due to missing writer", problems)
		}
	})

	t.Run("each with a bad source is refused", func(t *testing.T) {
		raw := `name: sample
inputs: { plans: required }
start: b
steps:
  b: { run: builder, on: { done: f } }
  f: { fork: { each: "bad-source", workflow: child }, on: { joined: done, conflict: done } }`
		problems := Validate(mustParse(t, raw), env)
		if !hasProblem(problems, RuleKind, "f") {
			t.Fatalf("Validate = %v, want RuleKind on f due to bad each source", problems)
		}
	})

	t.Run("halted outcome is accepted on fork", func(t *testing.T) {
		raw := `name: sample
inputs: { plans: required }
start: b
steps:
  b: { run: builder, on: { done: f } }
  f: { fork: { each: plans, workflow: child }, on: { joined: done, conflict: done, halted: done } }`
		problems := Validate(mustParse(t, raw), env)
		if len(problems) != 0 {
			t.Fatalf("Validate = %v, want no problems when halted is declared", problems)
		}
	})
}

func TestForkNeedsYouAndStopped(t *testing.T) {
	def := forkDef(Step{
		Fork: &Fork{Children: []ForkChild{{Workflow: "w1"}, {Workflow: "w2"}}},
		On:   map[string]Target{"joined": DoneTarget(), "conflict": DoneTarget()},
	})
	s := awaitingFork("split", []string{"1", "2"})
	sHalt, actsHalt := Next(def, s, Event{Kind: EventNeedsYou, Step: "split", Reason: "need manual input"})
	if sHalt.Status != StatusHalted || sHalt.Reason != "need manual input" || only(t, actsHalt).Kind != ActionHalt {
		t.Fatalf("needs_you: status %q acts %+v", sHalt.Status, actsHalt)
	}
	sStop, actsStop := Next(def, s, Event{Kind: EventStopped, Step: "split"})
	if sStop.Status != StatusStopped || only(t, actsStop).Kind != ActionStop {
		t.Fatalf("stopped: status %q acts %+v", sStop.Status, actsStop)
	}
}

func TestForkMatchEdgeCoverage(t *testing.T) {
	step := Step{
		Fork: &Fork{Children: []ForkChild{{Workflow: "w1"}}},
		On: map[string]Target{
			"result=joined":   StepTarget("build"),
			"result=conflict": StepTarget("build"),
			"result=halted":   StepTarget("build"),
			"else":            DoneTarget(),
		},
	}
	def := forkDef(step)
	s := awaitingFork("split", []string{"1"})
	s.Awaiting.Merging = true

	sJoin, _ := Next(def, s, Event{Kind: EventMergeClosed, Step: "split", Result: "joined"})
	if sJoin.At != "build" {
		t.Fatalf("result=joined at = %q", sJoin.At)
	}

	sElse, _ := Next(def, s, Event{Kind: EventMergeClosed, Step: "split", Result: "other"})
	if sElse.Status != StatusDone {
		t.Fatalf("else status = %q", sElse.Status)
	}

	sChild := awaitingFork("split", []string{"1"})
	sHalt, _ := Next(def, sChild, Event{Kind: EventChildEnded, Step: "split", Child: "1", Status: "halted", Reason: "bad"})
	if sHalt.At != "build" {
		t.Fatalf("result=halted at = %q", sHalt.At)
	}
}

func TestForkEdgeCasesCoverage(t *testing.T) {
	sNoChildren := runningState()
	sNoChildren.Status = StatusHalted
	sNoChildren.At = "split"
	sNoChildren.Awaiting = Awaiting{Step: "split"}
	if _, ok := ReopenFork(sNoChildren, "1"); ok {
		t.Fatal("ReopenFork succeeded with no children")
	}

	unmatchedStep := Step{
		Fork: &Fork{Each: "plans", Workflow: "w"},
		On:   map[string]Target{"conflict": DoneTarget()},
	}
	sEmpty, actsEmpty := Start(forkDef(unmatchedStep), StartInputs{Plans: []string{}})
	if sEmpty.Status != StatusHalted || sEmpty.Reason != "split joined: no edge matches" {
		t.Fatalf("status %q reason %q", sEmpty.Status, sEmpty.Reason)
	}
	if only(t, actsEmpty).Kind != ActionHalt {
		t.Fatalf("action = %+v", actsEmpty)
	}

	artifactStep := Step{
		Fork: &Fork{Each: "{{ghost.items}}", Workflow: "w"},
		On:   map[string]Target{"joined": DoneTarget(), "conflict": DoneTarget()},
	}
	sArt, _ := enter(forkDef(artifactStep), runningState(), "split", 0)
	if sArt.Status != StatusDone {
		t.Fatalf("missing artifact items status = %q", sArt.Status)
	}
}

func TestForkValidationBranches(t *testing.T) {
	env := shippedEnv()
	env.Given = &Given{Plans: true}
	env.Workflow = func(name string) (Definition, bool) {
		return mustParse(t, "name: child\ninputs: { plans: required }\nstart: a\nsteps:\n  a: { run: builder, on: { done: done } }"), true
	}
	rawInvalid := `name: sample
inputs: { plans: required }
start: b
steps:
  b: { run: builder, on: { done: f } }
  f: { fork: { each: plans, workflow: child }, on: { joined: done, conflict: done, bogus: done } }`
	if !hasProblem(Validate(mustParse(t, rawInvalid), env), RuleMatch, "f") {
		t.Fatal("want RuleMatch on bogus match key")
	}

	rawElse := `name: sample
inputs: { plans: required }
start: b
steps:
  b: { run: builder, on: { done: f } }
  f: { fork: { each: plans, workflow: child }, on: { else: done } }`
	if probs := Validate(mustParse(t, rawElse), env); len(probs) != 0 {
		t.Fatalf("want no problems on else match, got: %v", probs)
	}

	rawNoJoined := `name: sample
inputs: { plans: required }
start: b
steps:
  b: { run: builder, on: { done: f } }
  f: { fork: { each: plans, workflow: child }, on: { conflict: done } }`
	if !hasProblem(Validate(mustParse(t, rawNoJoined), env), RuleMatch, "f") {
		t.Fatal("want RuleMatch on uncovered joined")
	}

	rawNoConflict := `name: sample
inputs: { plans: required }
start: b
steps:
  b: { run: builder, on: { done: f } }
  f: { fork: { each: plans, workflow: child }, on: { joined: done } }`
	if !hasProblem(Validate(mustParse(t, rawNoConflict), env), RuleMatch, "f") {
		t.Fatal("want RuleMatch on uncovered conflict")
	}
}
