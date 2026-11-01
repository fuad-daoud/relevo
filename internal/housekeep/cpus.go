package housekeep

import (
	"log/slog"
	"slices"
	"strconv"

	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// pickCPU returns the lowest core in pool that is not in held. ok is false
// when every pool core is held, or when pool is empty. held may contain cores
// outside the pool and duplicates, and both are ignored. Pure.
func pickCPU(pool, held []int) (int, bool) {
	used := make(map[int]bool, len(held))
	for _, c := range held {
		used[c] = true
	}
	var (
		best int
		ok   bool
	)
	for _, c := range pool {
		if used[c] {
			continue
		}
		if !ok || c < best {
			best, ok = c, true
		}
	}
	return best, ok
}

// HeldIn collects the cores held by live rounds other than self. Liveness, not
// "the field is set", decides: a halted, done, re-queued or crashed round
// never blocks a core, even on a path that forgets to clear the field. Pure.
func HeldIn(bindings []store.Binding, self string) []int {
	var held []int
	for _, b := range bindings {
		if b.Name == self || b.RoundCPU == nil {
			continue
		}
		if b.Builder.PID != 0 || b.GateRun != nil {
			held = append(held, *b.RoundCPU)
		}
	}
	return held
}

// localHeldCPUs is the local census: HeldIn over the caller's own transaction,
// which already holds the store's state lock.
func localHeldCPUs(tx *store.Tx, self string) ([]int, error) {
	bindings, err := tx.List()
	if err != nil {
		return nil, err
	}
	return HeldIn(bindings, self), nil
}

// CPUPinText is the core a binding's round is pinned to, as a systemd
// cpu-list, or "" when RoundCPU is nil. An empty pin runs on the whole pool.
// Pure.
func CPUPinText(b store.Binding) string {
	if b.RoundCPU == nil {
		return ""
	}
	return strconv.Itoa(*b.RoundCPU)
}

// AssignRoundCPU gives b the core its round runs on, called by startRound
// right before it builds the spec. It never fails: an empty pool or a nil tx
// (a caller with no round to pin) leaves RoundCPU nil, and a census error or
// an exhausted pool leaves it nil so the round runs unpinned rather than
// failing. A relaunch or a mid-round switch keeps its core when that core is
// still in the pool and still free. The returned binding carries RoundCPU, so
// the caller's tx.Save persists it.
func AssignRoundCPU(deps Deps, tx *store.Tx, b store.Binding) store.Binding {
	var pool []int
	if deps.AllowedCPUs != "" {
		// A parse error cannot happen after policy validated the field;
		// treat it as no pool rather than failing a round.
		if p, err := spawn.ParseCPUList(deps.AllowedCPUs); err == nil {
			pool = p
		}
	}
	if len(pool) == 0 || tx == nil {
		b.RoundCPU = nil
		return b
	}

	heldFn := deps.HeldCPUs
	if heldFn == nil {
		heldFn = localHeldCPUs
	}
	held, err := heldFn(tx, b.Name)
	if err != nil {
		slog.Warn("cpu census failed; round runs on the whole pool", "binding", b.Name, "round", b.Round, "err", err)
		b.RoundCPU = nil
		return b
	}

	// A relaunch after a restart, or a mid-round switch, keeps its core when
	// that core is still in the pool and no other live round holds it.
	if b.RoundCPU != nil && slices.Contains(pool, *b.RoundCPU) && !slices.Contains(held, *b.RoundCPU) {
		return b
	}

	cpu, ok := pickCPU(pool, held)
	if !ok {
		slog.Warn("cpu pool exhausted; round runs on the whole pool", "binding", b.Name, "round", b.Round, "pool", deps.AllowedCPUs, "held", len(held))
		b.RoundCPU = nil
		return b
	}
	b.RoundCPU = &cpu
	return b
}
