package relevo

import (
	"context"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// loadAlive answers liveness for every handle the report will ask about with
// one ps fork for the fleet. A runner with no batch surface, and a failed
// batch, both fall back to one Alive call per handle: that fallback is the
// correctness path, keeping an unrunnable ps from reading as dead builders
// and a pre-batch runner working unchanged.
func loadAlive(ctx context.Context, rt Runtime, handles []spawn.ProcHandle) map[spawn.ProcHandle]aliveAnswer {
	out := make(map[spawn.ProcHandle]aliveAnswer, len(handles))
	if len(handles) == 0 || rt.Runner == nil {
		return out
	}

	if facts, ok := batchAlive(ctx, rt, handles); ok {
		for _, h := range handles {
			// An unlisted pid reads as a missing process does: known, and not
			// alive. A fact is only ever read for a handle this batch asked
			// about, so it never answers for a process it did not see.
			out[h] = aliveAnswer{alive: aliveFromFact(facts[h.PID], h), known: true}
		}
		return out
	}

	for _, h := range handles {
		alive, err := rt.Runner.Alive(ctx, h)
		if err != nil {
			// Alive's own rule, so the fallback answers as the batch does: an
			// unanswerable probe is unknown, not dead, and the row reads
			// "unknown".
			out[h] = aliveAnswer{}
			continue
		}
		out[h] = aliveAnswer{alive: alive, known: true}
	}
	return out
}

// aliveAnswer is one handle's liveness reading and whether it could be read at
// all. The two stay apart because they are different claims: an unlisted
// process is known dead, and an unrunnable ps is known nothing. Collapsing
// the second into the first would let a broken probe retire a fleet of
// builders.
type aliveAnswer struct {
	alive bool
	known bool
}

// batchAlive is the one batched probe, and whether there was one at all. ok is
// false when the runner offers no batch surface or the probe failed, which
// sends the caller to the per-handle path. There is deliberately no cache
// between refreshes: a stale reading could report "working" after its builder
// died.
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

// aliveFromFact applies Alive's rule to one batched fact: unlisted or zombie
// is not alive, and a start time within a second of the handle's is. A zero
// fact is what an absent pid reads as, and it is not alive -- which is why a
// fact is only ever consulted for a handle the batch was asked about.
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

// headlessHandles is every report handle whose liveness a row will ask about:
// headless builders with a process. A row with no pid asks nothing, so
// probing it would spend a fork on a question nobody asked.
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
