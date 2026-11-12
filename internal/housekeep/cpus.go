package housekeep

import (
	"cmp"
	"log/slog"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// onlineCPUs is the machine's online CPU count; a variable so tests fake a
// smaller machine without touching the host.
var onlineCPUs = runtime.NumCPU

// roundPool is the cores a round's set is picked from: the template's
// allowed_cpus pool, or every online CPU when the template names none, so a
// host without delegated cpuset still spreads its rounds. A parse error
// cannot happen after policy validated the field; it reads as no pool
// rather than failing a round.
func roundPool(allowed string) []int {
	if allowed == "" {
		var pool []int
		for c := 0; c < onlineCPUs(); c++ {
			pool = append(pool, c)
		}
		return pool
	}
	if p, err := spawn.ParseCPUList(allowed); err == nil {
		return p
	}
	return nil
}

// pickCPUSet returns the least-used size-sized set of pool cores: the lowest
// (max, then sum) load, with ties to the lowest cores, returned sorted
// ascending. held is a multiset: duplicates are the per-core counts, and
// cores outside the pool are ignored. ok is false when the pool is empty,
// size < 1, size exceeds the pool, or every pool core is held. Pure.
func pickCPUSet(pool, held []int, size int) ([]int, bool) {
	if len(pool) == 0 || size < 1 || size > len(pool) {
		return nil, false
	}
	count := make(map[int]int, len(held))
	for _, c := range held {
		count[c]++
	}
	free := false
	for _, c := range pool {
		if count[c] == 0 {
			free = true
			break
		}
	}
	if !free {
		return nil, false
	}
	ordered := append([]int(nil), pool...)
	slices.SortFunc(ordered, func(a, b int) int {
		if d := cmp.Compare(count[a], count[b]); d != 0 {
			return d
		}
		return cmp.Compare(a, b)
	})
	set := append([]int(nil), ordered[:size]...)
	slices.Sort(set)
	return set, true
}

// keptSet reports whether a stored set survives a relaunch: every core still
// in the pool and held by no other live round.
func keptSet(pool, held, set []int) bool {
	if len(set) == 0 {
		return false
	}
	for _, c := range set {
		if !slices.Contains(pool, c) || slices.Contains(held, c) {
			return false
		}
	}
	return true
}

// HeldIn collects the cores held by live rounds other than self: every core
// of a round's set, or its single RoundCPU when the list is empty. Liveness,
// not "the field is set", decides: a halted, done, re-queued or crashed round
// never blocks a core, even on a path that forgets to clear the field.
// Duplicates across rounds are the per-core counts. Pure.
func HeldIn(bindings []store.Binding, self string) []int {
	var held []int
	for _, b := range bindings {
		if b.Name == self {
			continue
		}
		if b.Builder.PID != 0 || b.GateRun != nil {
			if len(b.RoundCPUs) > 0 {
				held = append(held, b.RoundCPUs...)
			} else if b.RoundCPU != nil {
				held = append(held, *b.RoundCPU)
			}
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

// RoundPinText is the comma-joined cores a binding's round runs on; the
// single-core pin when only RoundCPU is set, or "" when neither is. Pure.
func RoundPinText(b store.Binding) string {
	if len(b.RoundCPUs) > 0 {
		parts := make([]string, len(b.RoundCPUs))
		for i, c := range b.RoundCPUs {
			parts[i] = strconv.Itoa(c)
		}
		return strings.Join(parts, ",")
	}
	return CPUPinText(b)
}

// AssignRoundCPU gives b the set of cores its round runs on, called by
// startRound right before it builds the spec. It never fails: an empty pool
// or a nil tx (a caller with no round to pin) clears both pin fields, and a
// census error or an exhausted pool clears them so the round runs unpinned
// rather than failing. A relaunch or a mid-round switch keeps its set when
// every core is still in the pool and still free; a legacy single pin keeps
// as a size-1 set. Without a template pool the set comes from every online
// CPU and the single pin stays nil, so gates and consults read unpinned.
// The returned binding carries RoundCPUs and RoundCPU, so the caller's
// tx.Save persists them.
func AssignRoundCPU(deps Deps, tx *store.Tx, b store.Binding) store.Binding {
	size := deps.RoundCPUs
	if size < 1 {
		size = 1
	}
	pool := roundPool(deps.AllowedCPUs)
	if len(pool) == 0 || tx == nil {
		b.RoundCPU = nil
		b.RoundCPUs = nil
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
		b.RoundCPUs = nil
		return b
	}

	// A relaunch after a restart, or a mid-round switch, keeps its set when
	// every core is still in the pool and no other live round holds one.
	if keptSet(pool, held, b.RoundCPUs) {
		if deps.AllowedCPUs == "" {
			b.RoundCPU = nil
		}
		return b
	}
	// A legacy single pin keeps as a size-1 set on the same terms.
	if len(b.RoundCPUs) == 0 && b.RoundCPU != nil &&
		slices.Contains(pool, *b.RoundCPU) && !slices.Contains(held, *b.RoundCPU) {
		b.RoundCPUs = []int{*b.RoundCPU}
		if deps.AllowedCPUs == "" {
			b.RoundCPU = nil
		}
		return b
	}

	set, ok := pickCPUSet(pool, held, size)
	if !ok {
		slog.Warn("cpu pool exhausted; round runs on the whole pool", "binding", b.Name, "round", b.Round, "pool", deps.AllowedCPUs, "held", len(held))
		b.RoundCPU = nil
		b.RoundCPUs = nil
		return b
	}
	b.RoundCPUs = set
	if deps.AllowedCPUs != "" {
		first := set[0]
		b.RoundCPU = &first
	} else {
		b.RoundCPU = nil
	}
	return b
}
