package workflow

import (
	"reflect"
	"strings"
	"testing"
)

// resolveFixture is the definition and state TestResolveRefTable resolves
// against: a for-each over the plans input, two run steps, a check, a fork and
// one string param.
func resolveFixture() (Definition, State) {
	def := Definition{
		Name:   "resolve",
		Params: map[string]Param{"x": {Kind: ParamString, Str: "v"}},
		Steps: map[string]Step{
			"plans":  {ForEach: "plans", On: map[string]Target{"next": StepTarget("build")}},
			"build":  {Run: "builder", On: map[string]Target{"done": StepTarget("review")}},
			"review": {Run: "reviewer", On: map[string]Target{"done": DoneTarget()}},
			"mchk":   {Check: "make check", On: map[string]Target{"green": DoneTarget()}},
			"split":  {Fork: &Fork{Each: "plans"}, On: map[string]Target{"joined": DoneTarget()}},
		},
	}
	s := State{
		Iter: map[string]Iter{"plans": {Index: 0, Items: []string{"plan-1.md", "plan-2.md"}}},
		Results: map[string]Result{
			"build":  {Round: 1, Status: "done", Artifacts: map[string][]string{"report": {"build-1-report.md"}, "diff": {"build-1-diff.patch"}}},
			"review": {Round: 1, Status: "done", Artifacts: map[string][]string{"output": {"review-1-output.md"}, "findings": {"review-1-findings.md"}}},
			"mchk":   {Round: 1, Status: "done", Artifacts: map[string][]string{"log": {"check-1.log"}}},
			"split":  {Round: 1, Status: "done", Artifacts: map[string][]string{"conflict": {"left.md", "right.md"}}},
		},
	}
	return def, s
}

// TestResolveRefTable has one case per reference row the spec's reference table
// names. The task and chain rows are the chain record's own values, not
// workflow state, so a caller resolves them and this resolver reports them as
// such.
func TestResolveRefTable(t *testing.T) {
	t.Parallel()
	def, s := resolveFixture()
	cases := []struct {
		name string
		ref  Ref
		want RefTarget
		err  bool
	}{
		{"task", Ref{Root: "task"}, RefTarget{}, true},
		{"params", Ref{Root: "params", Attr: "x"}, RefTarget{Inline: "v"}, false},
		{"for-each current", Ref{Root: "plans", Attr: "current"}, RefTarget{Key: "plan-1.md"}, false},
		{"for-each all", Ref{Root: "plans", Attr: "all"}, RefTarget{List: []string{"plan-1.md", "plan-2.md"}}, false},
		{"declared artifact", Ref{Root: "review", Attr: "findings"}, RefTarget{Key: "review-1-findings.md"}, false},
		{"writer report", Ref{Root: "build", Attr: "report"}, RefTarget{Key: "build-1-report.md"}, false},
		{"writer diff", Ref{Root: "build", Attr: "diff"}, RefTarget{Key: "build-1-diff.patch"}, false},
		{"reader output", Ref{Root: "review", Attr: "output"}, RefTarget{Key: "review-1-output.md"}, false},
		{"check log", Ref{Root: "mchk", Attr: "log"}, RefTarget{Key: "check-1.log"}, false},
		{"fork conflict", Ref{Root: "split", Attr: "conflict"}, RefTarget{List: []string{"left.md", "right.md"}}, false},
		{"chain diff", Ref{Root: "chain", Attr: "diff"}, RefTarget{}, true},
		{"chain base", Ref{Root: "chain", Attr: "base"}, RefTarget{}, true},
		{"chain branch", Ref{Root: "chain", Attr: "branch"}, RefTarget{}, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveRef(def, s, tc.ref)
			if tc.err {
				if err == nil {
					t.Fatalf("ResolveRef(%v) = %+v, want an error", tc.ref, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveRef(%v): %v", tc.ref, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ResolveRef(%v) = %+v, want %+v", tc.ref, got, tc.want)
			}
		})
	}
}

// TestResolveRefRefusesWhatWorkflowStateDoesNotHold pins the four refusals that
// are about the reference itself rather than the state: an unknown param, a
// for-each with nothing current, an all on a step that is not a for-each, and a
// step that exposes no such artifact. Each says which one it is, so a chain that
// reads it can be fixed without reading the resolver.
func TestResolveRefRefusesWhatWorkflowStateDoesNotHold(t *testing.T) {
	t.Parallel()

	def, s := resolveFixture()

	for _, tc := range []struct {
		name, want string
		ref        Ref
	}{
		{"unknown param", `unknown param "nope"`, Ref{Root: "params", Attr: "nope"}},
		{"all on a plain step", "build.all is not a for-each list", Ref{Root: "build", Attr: "all"}},
		{"unknown artifact", `build exposes no artifact "report2"`, Ref{Root: "build", Attr: "report2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveRef(def, s, tc.ref)
			if err == nil {
				t.Fatalf("ResolveRef(%+v): want an error, got none", tc.ref)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestResolveRefRefusesAnIterationWithNothingCurrent pins the refusal a
// for-each reference raises before the chain has entered it: current is read
// from the iteration the walk keeps, so an empty one has no current item.
func TestResolveRefRefusesAnIterationWithNothingCurrent(t *testing.T) {
	t.Parallel()

	def, s := resolveFixture()
	s.Iter["plans"] = Iter{Index: -1, Items: nil}

	_, err := ResolveRef(def, s, Ref{Root: "plans", Attr: "current"})
	if err == nil {
		t.Fatal("plans.current before the iteration began: want an error, got none")
	}
	if !strings.Contains(err.Error(), "has no current item") {
		t.Errorf("error = %q, want it to say there is no current item", err)
	}
}

// TestResolveRefRefusesTheChainRecord pins the two roots that live on the chain
// record rather than in workflow state, so a reference to one is a workflow
// error rather than a silently empty value.
func TestResolveRefRefusesTheChainRecord(t *testing.T) {
	t.Parallel()

	def, s := resolveFixture()
	for _, tc := range []struct {
		ref  Ref
		want string
	}{
		{Ref{Root: "task", Attr: "text"}, "the task input is not workflow state"},
		{Ref{Root: "chain", Attr: "branch"}, "chain.branch is not workflow state"},
	} {
		_, err := ResolveRef(def, s, tc.ref)
		if err == nil {
			t.Fatalf("ResolveRef(%+v): want an error, got none", tc.ref)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("error = %q, want it to contain %q", err, tc.want)
		}
	}
}

// TestResolveRefUnrunStepIsMissing pins the unrun step: a reference to a step
// the state holds no result for is a miss, not an empty value.
func TestResolveRefUnrunStepIsMissing(t *testing.T) {
	t.Parallel()
	def, s := resolveFixture()
	delete(s.Results, "build")
	if _, err := ResolveRef(def, s, Ref{Root: "build", Attr: "report"}); err == nil {
		t.Fatal("ResolveRef of an unrun step: want an error")
	}
}
