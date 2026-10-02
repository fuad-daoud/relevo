package workflow

import (
	"reflect"
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
