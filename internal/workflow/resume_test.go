package workflow

import (
	"strings"
	"testing"
)

// haltedRun is a stopped or halted state on a run step, with its reason set.
func haltedRun(step, member string, round int) State {
	s := awaitingRun(step, member, round)
	s.Status = StatusHalted
	s.Reason = "halted"
	return s
}

func TestResumeReentersTheHaltedStep(t *testing.T) {
	def := tdef("a", map[string]Step{
		"a": {Run: "builder", On: map[string]Target{"done": StepTarget("b")}},
		"b": {Run: "builder"},
	})
	s := haltedRun("a", "builder", 1)
	got, actions, err := Resume(def, s, ResumeOpts{})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got.Status != StatusRunning || got.Reason != "" {
		t.Fatalf("status %q reason %q", got.Status, got.Reason)
	}
	if got.At != "a" {
		t.Fatalf("at = %q", got.At)
	}
	if a := only(t, actions); a.Kind != ActionSend || a.Step != "a" || a.Actor != "builder" {
		t.Fatalf("action = %+v", a)
	}
}

func TestResumeResetsTheReenteredStepsBudget(t *testing.T) {
	def := tdef("correct", map[string]Step{
		"plans": {ForEach: "plans", On: map[string]Target{"next": StepTarget("correct"), "empty": StepTarget("correct")}},
		"correct": {Run: "planner",
			Budget: &Budget{Max: Limit{Count: 3}, Per: Per{Steps: []string{"plans"}}, Then: HaltTarget("too many corrections")},
			On:     map[string]Target{"done": StepTarget("build")}},
		"build": {Run: "builder"},
	})
	s := haltedRun("correct", "planner", 4)
	s.Reason = "too many corrections"
	s.Visits["correct"] = 4
	got, actions, err := Resume(def, s, ResumeOpts{})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if a := only(t, actions); a.Kind != ActionSend || a.Step != "correct" || a.Actor != "planner" {
		t.Fatalf("action = %+v", a)
	}
	if got.Status != StatusRunning {
		t.Fatalf("status = %q", got.Status)
	}
	if got.Visits["correct"] != 1 {
		t.Fatalf("visits = %d, want 1", got.Visits["correct"])
	}
}

func TestResumeFromAnotherStep(t *testing.T) {
	def := tdef("a", map[string]Step{
		"a": {Run: "builder", On: map[string]Target{"done": StepTarget("b")}},
		"b": {Run: "reviewer"},
	})
	got, actions, err := Resume(def, haltedRun("a", "builder", 1), ResumeOpts{From: "b"})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got.At != "b" {
		t.Fatalf("at = %q", got.At)
	}
	if a := only(t, actions); a.Kind != ActionSend || a.Step != "b" || a.Actor != "reviewer" {
		t.Fatalf("action = %+v", a)
	}
}

func TestResumeFromAnUnknownStepErrors(t *testing.T) {
	def := tdef("a", map[string]Step{"a": {Run: "builder"}})
	if got := ResumeTargets(def); len(got) != 1 || got[0] != "a" {
		t.Fatalf("targets = %v", got)
	}
	_, _, err := Resume(def, haltedRun("a", "builder", 1), ResumeOpts{From: "nope"})
	if err == nil {
		t.Fatalf("want an error for an unknown step")
	}
	if !strings.Contains(err.Error(), "nope") || !strings.Contains(err.Error(), "a") {
		t.Fatalf("error = %v", err)
	}
}

func TestResumeTreatsANewerManualRoundAsTheClose(t *testing.T) {
	def := tdef("review", map[string]Step{
		"review":  {Run: "reviewer", On: map[string]Target{"verdict=pass": DoneTarget(), "verdict=changes": StepTarget("correct")}},
		"correct": {Run: "planner"},
	})
	s := haltedRun("review", "reviewer", 2)
	closed := Event{Kind: EventStepClosed, Step: "review", Member: "reviewer", Round: 3, Status: "done",
		Outcomes: map[string]string{"verdict": "changes"}}
	got, actions, err := Resume(def, s, ResumeOpts{Closed: &closed})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got.Status != StatusRunning {
		t.Fatalf("status = %q", got.Status)
	}
	if r := got.Results["review"]; r.Round != 3 || r.Status != "done" {
		t.Fatalf("recorded = %+v", r)
	}
	if a := only(t, actions); a.Kind != ActionSend || a.Step != "correct" {
		t.Fatalf("action = %+v", a)
	}
}

func TestResumeRejectsAnOlderOrForeignClose(t *testing.T) {
	def := tdef("review", map[string]Step{
		"review":  {Run: "reviewer", On: map[string]Target{"done": DoneTarget()}},
		"correct": {Run: "planner"},
	})
	base := func() State { return haltedRun("review", "reviewer", 2) }
	cases := []struct {
		name   string
		from   string
		closed Event
	}{
		{"older round", "", Event{Kind: EventStepClosed, Step: "review", Member: "reviewer", Round: 2}},
		{"foreign member", "", Event{Kind: EventStepClosed, Step: "review", Member: "someone-else", Round: 3}},
		{"another step", "", Event{Kind: EventStepClosed, Step: "correct", Member: "reviewer", Round: 3}},
		{"with a from", "review", Event{Kind: EventStepClosed, Step: "review", Member: "reviewer", Round: 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			closed := tc.closed
			if _, _, err := Resume(def, base(), ResumeOpts{From: tc.from, Closed: &closed}); err == nil {
				t.Fatalf("want an error")
			}
		})
	}
}

func TestResumeStoppedResendsTheSameStep(t *testing.T) {
	def := tdef("b", map[string]Step{
		"b": {Run: "builder", On: map[string]Target{"done": DoneTarget()}},
	})
	s := awaitingRun("b", "builder", 2)
	s.Status = StatusStopped
	got, actions, err := Resume(def, s, ResumeOpts{})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got.Status != StatusRunning {
		t.Fatalf("status = %q", got.Status)
	}
	if a := only(t, actions); a.Kind != ActionSend || a.Step != "b" || a.Actor != "builder" {
		t.Fatalf("action = %+v", a)
	}
}

func TestResumeRefusesRunningAndDone(t *testing.T) {
	def := tdef("a", map[string]Step{"a": {Run: "builder"}})
	for _, status := range []Status{StatusRunning, StatusDone} {
		s := runningState()
		s.Status = status
		s.At = "a"
		if _, _, err := Resume(def, s, ResumeOpts{}); err == nil {
			t.Fatalf("%s: want an error", status)
		}
	}
}

func TestCurrentReportsPositionOfTotal(t *testing.T) {
	s := runningState()
	s.Iter["plans"] = Iter{Index: 1, Items: []string{"a", "b", "c"}}
	item, pos, total, ok := Current(s, "plans")
	if !ok || item != "b" || pos != 2 || total != 3 {
		t.Fatalf("current = %q %d/%d ok=%v", item, pos, total, ok)
	}
	if _, _, _, ok := Current(s, "other"); ok {
		t.Fatalf("an unknown for-each should not be ok")
	}
	s.Iter["plans"] = Iter{Index: -1, Items: []string{"a"}}
	if item, pos, _, ok := Current(s, "plans"); ok || pos != 0 || item != "" {
		t.Fatalf("between items: %q %d ok=%v", item, pos, ok)
	}
}
