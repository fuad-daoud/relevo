package serve

import (
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// openCheckLister lists chains and their open check rows: *store.Store in
// the census, a stub in tests. Its shape mirrors the handle the lookup
// takes, so either of the store's own handles qualifies.
type openCheckLister interface {
	Chains() ([]db.ChainRow, error)
	ChainCheckOpen() ([]db.ChainCheckRow, error)
}

// countOpenChecks adds one running round per member with an open chain
// check. The check runs on its member's retained CPUs after the round
// closed, so its process holds a seat the binding walk cannot see. A member
// the walk already counted keeps its single seat, never two. A member whose
// binding is gone counts at the builder defaults: the check process is real
// even when its record is not.
func countOpenChecks(c *census, root string, reg *roles.Registry, owner openCheckLister, bindings []store.Binding, counted map[string]bool) {
	open, err := relevo.OpenCheckMembers(owner)
	if err != nil {
		slog.Warn("census: open chain checks unreadable; counting none", "err", err)
		return
	}
	byName := make(map[string]store.Binding, len(bindings))
	for _, b := range bindings {
		byName[b.Name] = b
	}
	for member, pid := range open {
		if counted[member] {
			continue
		}
		var base, burst int64 = seatBuilderBase, seatBuilderBurst
		if mb, ok := byName[member]; ok {
			base, burst = seatCostFor(root, reg, mb)
		}
		c.RunningRounds = append(c.RunningRounds, runningRound{PID: pid, Base: base, Burst: burst})
		c.Running++
	}
}
