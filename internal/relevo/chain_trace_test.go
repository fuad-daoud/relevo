package relevo

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
)

// traceChain closes one whole correction cycle on a one-plan chain: the
// builder goes green, the reviewer asks for changes, the planner writes the
// correction, the builder goes green again and the reviewer passes. Five
// transitions, every step of the state machine the trace can show.
func traceChain(t *testing.T, rt Runtime) {
	t.Helper()

	startedChain(t, rt, ChainOptions{MaxCorrections: ptr(2)})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))
	chainReaderClose(t, rt, "shop-plan", chainDoneBody())
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))
}

// TestShowTraceRendersEveryStepInOrder pins the trace's whole text: one line
// per transition, in seq order, each naming the state before it, the member
// that closed, its round and what the close said.
func TestShowTraceRendersEveryStepInOrder(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	traceChain(t, rt)

	doc, err := ChainTrace(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainTrace: %v", err)
	}
	const want = "plan 1/1  build    shop r1    no check\n" +
		"plan 1/1  review   shop-rev r1  changes\n" +
		"plan 1/1  correct  shop-plan r1  correction plan\n" +
		"plan 1/1  build    shop r2    no check\n" +
		"plan 1/1  review   shop-rev r2  pass\n"
	if got := RenderTrace(doc); got != want {
		t.Errorf("RenderTrace = \n%s\nwant\n%s", got, want)
	}
}

// TestShowTraceNamesTheRoundOfEachLine pins that every rendered line names the
// member and the round whose close produced it, and that the lines keep the
// stored seq order.
func TestShowTraceNamesTheRoundOfEachLine(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	traceChain(t, rt)

	doc, err := ChainTrace(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainTrace: %v", err)
	}
	if len(doc.Events) != 5 {
		t.Fatalf("trace has %d events, want 5", len(doc.Events))
	}

	lines := strings.Split(strings.TrimRight(RenderTrace(doc), "\n"), "\n")
	if len(lines) != len(doc.Events) {
		t.Fatalf("rendered %d lines for %d events", len(lines), len(doc.Events))
	}
	lastSeq := 0
	for i, e := range doc.Events {
		if e.Seq <= lastSeq {
			t.Errorf("event %d has seq %d after %d, want the stored order", i, e.Seq, lastSeq)
		}
		lastSeq = e.Seq
		if want := fmt.Sprintf("%s r%d", e.Member, e.Round); !strings.Contains(lines[i], want) {
			t.Errorf("line %d = %q, want it to name %s", i, lines[i], want)
		}
	}
}

// TestShowTraceHaltCarriesTheReason pins the halt: the last line says what the
// reviewer's close was and why the chain stopped on it.
func TestShowTraceHaltCarriesTheReason(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainNoVerdictBody())

	doc, err := ChainTrace(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainTrace: %v", err)
	}
	if doc.Status != string(chain.StatusHalted) {
		t.Errorf("status = %q, want halted", doc.Status)
	}
	out := RenderTrace(doc)
	if !strings.Contains(out, "no verdict  reviewer gave no verdict") {
		t.Errorf("RenderTrace = %q, want the halt line to carry its reason", out)
	}
}

// TestShowRoundStillReadsTheBuilderBinding pins the no-regression rule: a name
// that is both a chain and its builder still answers `<n> --round R` from the
// builder's binding, and carries no trace.
func TestShowRoundStillReadsTheBuilderBinding(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{Plans: []string{writePlan(t, "plan one")}})

	res, err := Show(context.Background(), rt, ShowOptions{
		Name: "shop", Round: 1, Section: ShowPrompt, Peek: true,
	})
	if err != nil {
		t.Fatalf("Show --round 1: %v", err)
	}
	if res.Section != ShowPrompt || res.Text != "plan one" || res.Trace != nil {
		t.Errorf("Show --round 1 = section %q text %q trace %v, want the builder's round-1 prompt",
			res.Section, res.Text, res.Trace)
	}
}
