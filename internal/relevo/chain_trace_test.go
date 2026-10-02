package relevo

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
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
// per transition, in seq order, each naming the step it moved from, its round
// and the target it chose.
func TestShowTraceRendersEveryStepInOrder(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	traceChain(t, rt)

	doc, err := ChainTrace(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainTrace: %v", err)
	}
	const want = "build r1 → review\n" +
		"review r1  verdict=changes → correct\n" +
		"correct r1 → build-fix\n" +
		"build-fix r2 → review\n" +
		"review r2  verdict=pass → done\n"
	if got := RenderTrace(doc); got != want {
		t.Errorf("RenderTrace = \n%s\nwant\n%s", got, want)
	}
}

// TestShowTraceNamesTheRoundOfEachLine pins that every rendered line names the
// step it moved from and the round whose close produced it, and that the lines
// keep the stored seq order.
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
		if want := fmt.Sprintf("%s r%d", e.Step, e.Round); !strings.Contains(lines[i], want) {
			t.Errorf("line %d = %q, want it to name %s", i, lines[i], want)
		}
	}
}

// TestShowTraceHaltCarriesTheReason pins the halt: the last line names the
// review's halt and the row carries why the chain stopped on it.
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
	last := doc.Events[len(doc.Events)-1]
	if !strings.Contains(last.Reason, "no relevo block carries it") {
		t.Errorf("halt row reason = %q, want the missing-verdict reason", last.Reason)
	}
	if out := RenderTrace(doc); !strings.HasSuffix(out, "review r1 → halt\n") {
		t.Errorf("RenderTrace = %q, want the halt line to name the review's halt", out)
	}
}

// TestRenderTraceLegacyRowUnchanged pins the no-regression rule for the trace:
// a row the fixed state machine wrote -- no decoded workflow event -- renders
// exactly the line it always did.
func TestRenderTraceLegacyRowUnchanged(t *testing.T) {
	t.Parallel()

	doc := ChainTraceDoc{
		Name: "shop", Status: string(chain.StatusDone), Plan: 1, Plans: 1,
		Events: []ChainTraceEvent{{
			Seq: 1, Step: string(chain.StepBuilding), Member: "shop", Round: 2, Plan: 1,
			Event:  chain.Event{Kind: chain.EventBuilderClosed, Gate: chain.GateGreen},
			Action: chain.Action{Kind: chain.ActionFinish},
		}},
	}
	const want = "plan 1/1  build    shop r2    check green\n"
	if got := RenderTrace(doc); got != want {
		t.Errorf("RenderTrace = %q, want %q", got, want)
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

// TestChainTraceOfAConvertedChainRendersItsLegacyRows pins the per-row decode:
// a chain converted onto the engine keeps the trace rows the fixed state machine
// wrote, and ChainTrace renders them instead of failing to decode a builder
// close as a workflow event.
func TestChainTraceOfAConvertedChainRendersItsLegacyRows(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	c := legacyChainRow("x", "running", "build", "building", "builder", 1, 0)
	seedLegacyChain(t, rt, c, legacyChainBindings("x"))
	ev := chain.Event{
		Kind: chain.EventBuilderClosed, Member: chain.MemberBuilder, Round: 1,
		Outcome: reporttail.OutcomeDone, Gate: chain.GateGreen,
	}
	act := chain.Action{Kind: chain.ActionSend, Member: chain.MemberReviewer, Seed: chain.SeedReviewer}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.ChainEventAppend("x", db.ChainEventRow{
			Phase: string(chain.PhaseBuild), Step: string(chain.StepBuilding),
			Member: "x", Round: 1, Plan: 1,
			Event: ev.Encode(), Action: act.Encode(),
		})
	}); err != nil {
		t.Fatalf("plant the legacy row: %v", err)
	}

	if err := ConvertLegacyChains(rt); err != nil {
		t.Fatalf("ConvertLegacyChains: %v", err)
	}

	doc, err := ChainTrace(context.Background(), rt, "x")
	if err != nil {
		t.Fatalf("ChainTrace: %v", err)
	}
	if len(doc.Events) != 1 {
		t.Fatalf("trace events = %d, want 1", len(doc.Events))
	}
	if doc.Events[0].Flow != nil {
		t.Errorf("the legacy row decoded as a workflow event: %+v", doc.Events[0].Flow)
	}
	if doc.Events[0].Event.Kind != chain.EventBuilderClosed || doc.Events[0].Action.Kind != chain.ActionSend {
		t.Errorf("event/action = %+v/%+v, want the legacy row decoded",
			doc.Events[0].Event, doc.Events[0].Action)
	}
	if out := RenderTrace(doc); !strings.Contains(out, "check green") {
		t.Errorf("RenderTrace = %q, want the legacy builder close line", out)
	}
}
