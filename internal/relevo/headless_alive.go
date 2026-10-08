package relevo

import (
	"context"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// loadAlive answers "is this handle's process alive" for every handle the report
// will ask about, with one ps fork for the whole fleet rather than one per
// row. A status refresh paints every headless row at once, so the per-row fork
// was the cost; batching them is what removes it.
//
// A Runner that offers no batch surface, and a batch that fails, both fall back
// to one Alive call per handle. That fallback is not a slower path, it is the
// correctness path: it is what keeps a ps that could not run from reading as a
// fleet of dead builders, and what keeps a Runner written before this surface
// working unchanged.
func loadAlive(ctx context.Context, rt Runtime, handles []spawn.ProcHandle) map[spawn.ProcHandle]aliveAnswer {
	out := make(map[spawn.ProcHandle]aliveAnswer, len(handles))
	if len(handles) == 0 || rt.Runner == nil {
		return out
	}

	if facts, ok := batchAlive(ctx, rt, handles); ok {
		for _, h := range handles {
			// A pid ps did not list is absent from the facts, which is the
			// same answer Alive gives for a missing process: known, and not
			// alive. The fact is only ever read for a handle this batch asked
			// about, so it can never answer for a process it did not see.
			out[h] = aliveAnswer{alive: aliveFromFact(facts[h.PID], h), known: true}
		}
		return out
	}

	for _, h := range handles {
		alive, err := rt.Runner.Alive(ctx, h)
		if err != nil {
			// Alive's own rule, kept here so the fallback cannot answer
			// differently from the batch: an unanswerable probe is not a dead
			// process, it is an unknown one, and the row reads "unknown".
			out[h] = aliveAnswer{}
			continue
		}
		out[h] = aliveAnswer{alive: alive, known: true}
	}
	return out
}

// aliveAnswer is one handle's liveness reading and whether it could be read at
// all. The two are kept apart because they are different claims: a process ps
// does not list is known dead, and a ps that could not run is known nothing.
// Collapsing the second into the first is what would let a broken probe retire
// a fleet of builders.
type aliveAnswer struct {
	alive bool
	known bool
}

// batchAlive is the one batched probe, and whether there was one at all. ok is
// false when the runner offers no batch surface or the probe failed, which
// sends the caller to the per-handle path.
//
// There is deliberately no cache between refreshes. A reading from an earlier
// refresh is a claim about processes that may since have died, and a cached one
// has to be keyed well enough not to answer for a process it never saw -- which
// is the same bookkeeping liveStat does for the diff, for a figure whose
// staleness costs nothing next to a word that decides whether a builder is
// running. One fork per refresh is the cost this removes; a window over it
// would trade that for a row that can read "working" after its builder died.
func batchAlive(ctx context.Context, rt Runtime, handles []spawn.ProcHandle) (map[int]spawn.AliveFact, bool) {
	prober, ok := rt.Runner.(spawn.AliveBatchProber)
	if !ok {
		return nil, false
	}
	facts, err := prober.AliveBatch(ctx, handles)
	if err != nil {
		return nil, false
	}
	return facts, true
}

// aliveFromFact applies Alive's rule to one batched fact: not listed or a
// zombie is not alive, and a start time within a second of the handle's is.
// A zero fact is what an absent pid reads as, and it is not alive -- which is
// why a fact is only ever consulted for a handle the batch was asked about.
func aliveFromFact(f spawn.AliveFact, h spawn.ProcHandle) bool {
	if f.State == "" {
		return false
	}
	if f.State[0] == 'Z' {
		return false
	}
	diff := f.StartedAt.Sub(h.StartedAt)
	if diff < 0 {
		diff = -diff
	}
	return diff <= time.Second
}

// headlessHandles is every handle in the report whose liveness a row will ask
// about: the headless builders with a process. A row with no pid asks nothing,
// so probing it would be a fork spent on a question nobody asked.
func headlessHandles(bindings []store.Binding) []spawn.ProcHandle {
	var out []spawn.ProcHandle
	for _, b := range bindings {
		if !b.Builder.Headless() || b.Builder.PID == 0 {
			continue
		}
		out = append(out, handleOf(b.Builder))
	}
	return out
}
