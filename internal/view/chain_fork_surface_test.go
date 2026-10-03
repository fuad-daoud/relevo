package view

import (
	"strings"
	"testing"
	"time"
)

// forkParentFacts is a parent chain's facts with a two-child split, one done.
func forkParentFacts() ChainFacts {
	return ChainFacts{
		Status: "running", StepAt: "fork", Round: 1,
		Children: []string{"cc.1", "cc.2"}, ChildrenDone: 1,
	}
}

func TestChainForkSegmentCountsTheDoneChildren(t *testing.T) {
	if got, want := ChainForkSegment(forkParentFacts()), "fork split · 1/2 done"; got != want {
		t.Errorf("fork segment = %q, want %q", got, want)
	}
}

func TestChainForkSegmentIsEmptyWithoutChildren(t *testing.T) {
	if got := ChainForkSegment(ChainFacts{Status: "running", StepAt: "build"}); got != "" {
		t.Errorf("a chain with no fork got segment %q, want empty", got)
	}
}

// A chain with no fork must render byte-identically to the text it had before
// fork facts existed, so the golden of every existing row still holds.
func TestChainSegmentWithoutAForkIsUnchanged(t *testing.T) {
	f := ChainFacts{Status: "running", StepAt: "reviewing", Round: 5, Check: true, PlanPos: 2, PlanTotal: 3}
	if got, want := ChainSegment(f), "reviewing check run 5 · plans 2/3"; got != want {
		t.Errorf("segment = %q, want %q", got, want)
	}
}

func TestChainFactsCarryTheForkProgress(t *testing.T) {
	rep := Report{Bindings: []BindingStatus{{
		Name:    "cc",
		Display: "ACTIVE",
		Role:    "chain",
		Chain:   ptrChain(forkParentFacts()),
	}}}
	lines := PlainStatusLineRows(StatusLineRows(rep, time.Time{}), 100)
	if len(lines) != 1 {
		t.Fatalf("parent with children produced %d rows, want 1", len(lines))
	}
	if !strings.Contains(lines[0], "split 1/2") {
		t.Errorf("collapsed parent row %q does not carry the split", lines[0])
	}
}

// A halted child is the row a human has to act on, so the collapsed statusline
// must show it; a running child is the parent's "split 1/2" and adds nothing.
func TestStatuslineExpandsAHaltedChildAndCollapsesARunningOne(t *testing.T) {
	parent := BindingStatus{Name: "cc", Display: "ACTIVE", Role: "chain", Chain: ptrChain(forkParentFacts())}
	halted := ChainFacts{Status: "halted", StepAt: "build", Round: 2, Parent: "cc"}
	running := ChainFacts{Status: "running", StepAt: "build", Round: 2, Parent: "cc"}

	rep := Report{Bindings: []BindingStatus{parent, {Name: "cc.2", Display: "NEEDS YOU", Role: "chain", Chain: &halted}}}
	lines := PlainStatusLineRows(StatusLineRows(rep, time.Time{}), 110)
	if len(lines) != 2 {
		t.Fatalf("a halted child produced %d rows, want 2 (parent + child): %q", len(lines), lines)
	}
	if !strings.Contains(lines[1], "cc.2") || !strings.Contains(lines[1], "NEEDS YOU") {
		t.Errorf("child row %q does not name the halted child", lines[1])
	}

	rep.Bindings = []BindingStatus{parent, {Name: "cc.2", Display: "ACTIVE", Role: "chain", Chain: &running}}
	lines = PlainStatusLineRows(StatusLineRows(rep, time.Time{}), 110)
	if len(lines) != 1 {
		t.Fatalf("a running child produced %d rows, want 1 (collapsed into the parent)", len(lines))
	}
}

// A child is not a top-level row: it prints indented under the parent that
// forked it, each with its own step and status.
func TestChildChainRowIsIndentedUnderItsParent(t *testing.T) {
	parent := BindingStatus{Name: "cc", Display: "ACTIVE", Role: "chain", Chain: ptrChain(forkParentFacts())}
	child := BindingStatus{Name: "cc.2", Display: "NEEDS YOU", Role: "chain", Chain: ptrChain(ChainFacts{Status: "running", StepAt: "build", Round: 2, Parent: "cc"})}

	var sb strings.Builder
	writeChainRow(&sb, parent)
	writeChainRow(&sb, child)
	out := sb.String()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("rendered %d chain rows, want 2:\n%s", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "cc ") {
		t.Errorf("parent row %q is not at the left margin", lines[0])
	}
	if !strings.HasPrefix(lines[1], "  cc.2") {
		t.Errorf("child row %q is not indented under its parent", lines[1])
	}
	if !strings.Contains(lines[1], "build r2") {
		t.Errorf("child row %q does not carry its own step and round", lines[1])
	}
}

func ptrChain(f ChainFacts) *ChainFacts { return &f }
