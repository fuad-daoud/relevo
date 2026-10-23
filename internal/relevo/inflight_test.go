package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/store"
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
	st := store.New(t.TempDir())
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
	st := store.New(t.TempDir())
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
	st := store.New(t.TempDir())
	if err := st.Save(inflightBinding("one", "opencode/alibaba-token-plan/qwen3.8-flash#xhigh", nil)); err != nil {
		t.Fatal(err)
	}
	gates := availability.Gates(AvailabilityDeps(Runtime{Store: st, Candidates: set}))
	if len(gates) != 1 || gates[0].Kind != availability.ConcurrencyLimit || gates[0].Note != "1/1" {
		t.Fatalf("gates = %+v, want the effort-named candidate at its cap 1/1", gates)
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
	st := store.New(t.TempDir())
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
