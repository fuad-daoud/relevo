package relevo

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// MasterMindStatus is the statusline's report: this mastermind's bindings and
// this mastermind's chains, built through the same row builder the plain
// listing uses. Which bindings belong is Scope's rule, asked one step early
// through scopeBinding, so the DONE decision is made in the same place for both
// surfaces instead of once per surface.
//
// An empty id keeps the old meaning of "no mastermind names no rows": the
// statusline is scoped by construction, and the scope a caller could not
// resolve never reaches here.
func MasterMindStatus(ctx context.Context, rt Runtime, mastermindID string) (view.Report, error) {
	if mastermindID == "" {
		return view.Report{}, nil
	}
	rep, err := scopedStatus(ctx, rt, Scope{MasterMindID: mastermindID})
	if err != nil {
		return view.Report{}, err
	}
	return rep, nil
}

// scopedStatus is the one status builder every scoped surface reads: it narrows
// the stored bindings and the stored chains by the same scope before the rows
// exist, so a chain's member rows and the chain that stands in for them are
// always decided together.
func scopedStatus(ctx context.Context, rt Runtime, sc Scope) (view.Report, error) {
	bindings, err := rt.Store.List()
	if err != nil {
		return view.Report{}, err
	}
	var kept []store.Binding
	for _, b := range bindings {
		if scopeBinding(b, sc) {
			kept = append(kept, b)
		}
	}
	rep, err := buildReport(ctx, rt, kept)
	if err != nil {
		return view.Report{}, err
	}
	chains, err := rt.Store.Chains()
	if err != nil {
		return view.Report{}, err
	}
	return applyChains(rt.Store, rep, scopedChains(chains, sc)), nil
}

// scopedChains keeps the scope's chains and the children hanging under them, so
// narrowing to one mastermind never orphans a fork child into a root.
func scopedChains(chains []db.ChainRow, sc Scope) []db.ChainRow {
	if sc.Named || sc.MasterMindID == "" {
		return chains
	}
	keep := make(map[string]bool, len(chains))
	for _, c := range chains {
		if c.MasterMindID == sc.MasterMindID {
			keep[c.Name] = true
		}
	}
	// A parent may sit above the child in the store's order, so the walk
	// repeats until it stops finding a child under a kept parent.
	for grew := true; grew; {
		grew = false
		for _, c := range chains {
			if !keep[c.Name] && c.Parent != "" && keep[c.Parent] {
				keep[c.Name] = true
				grew = true
			}
		}
	}
	mine := make([]db.ChainRow, 0, len(chains))
	for _, c := range chains {
		if keep[c.Name] {
			mine = append(mine, c)
		}
	}
	return mine
}
