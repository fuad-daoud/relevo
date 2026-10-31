package relevo

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// MasterMindStatus is the statusline's report: this mastermind's bindings
// and chains, built through the same row builder the plain listing uses.
// Which bindings belong is Scope's rule, asked one step early through
// scopeBinding, so the DONE decision is made in one place for both
// surfaces.
//
// An empty id names no rows: the statusline is scoped by construction.
func MasterMindStatus(ctx context.Context, rt Runtime, mastermindID string) (view.Report, error) {
	if mastermindID == "" {
		return view.Report{}, nil
	}
	rep, _, _, err := scopedStatus(ctx, rt, Scope{MasterMindID: mastermindID})
	if err != nil {
		return view.Report{}, err
	}
	return rep, nil
}

// ScopedStatus is the one status builder every scoped surface reads. It
// takes the whole Scope: Scope{All} narrows nothing and Scope{Named} takes
// no narrowing at all, so both reach the row builder un-narrowed.
//
// Narrowing here rather than after the rows exist is the point: a row is
// expensive, and a scope that cannot show a row does not pay to build one.
// The DONE rows the scope hides were never built, so DoneHidden is counted
// from the store rather than from the rows.
func ScopedStatus(ctx context.Context, rt Runtime, sc Scope) (view.Report, error) {
	rep, bindings, chains, err := scopedStatus(ctx, rt, sc)
	if err != nil {
		return view.Report{}, err
	}
	if !sc.Named && !sc.All {
		rep.DoneHidden = doneHiddenBy(rt.Store, bindings, chains, sc)
	}
	rep.HandedOver = handedOverRows(rt)
	return rep, nil
}

// scopedStatus narrows the stored bindings and chains by the same scope
// before the rows exist, so a chain's member rows and the chain that
// stands in for them are always decided together. It reports the stored
// rows it narrowed from, which is what the hidden count is counted from.
func scopedStatus(ctx context.Context, rt Runtime, sc Scope) (view.Report, []store.Binding, []db.ChainRow, error) {
	bindings, err := rt.Store.List()
	if err != nil {
		return view.Report{}, nil, nil, err
	}
	rep, err := buildReport(ctx, rt, narrowBindings(bindings, sc))
	if err != nil {
		return view.Report{}, nil, nil, err
	}
	chains, err := rt.Store.Chains()
	if err != nil {
		return view.Report{}, nil, nil, err
	}
	return view.ApplyChains(rt.Store, rep, scopedChains(chains, sc)), bindings, chains, nil
}

// narrowBindings is scopeBinding applied to every stored binding: the set
// the rows are built from. It asks the same predicate ScopeReport asks of
// a built report, one step earlier, where the answer costs no row.
func narrowBindings(bindings []store.Binding, sc Scope) []store.Binding {
	kept := make([]store.Binding, 0, len(bindings))
	for _, b := range bindings {
		if scopeBinding(b, sc) {
			kept = append(kept, b)
		}
	}
	return kept
}

// chainScopeBindings narrows the bindings a chain listing builds rows for:
// the scope's own bindings, plus every member of a chain the scope kept. A
// chain in scope is shown with the members it has, so a member the scope
// would hide keeps its row here; a binding that is neither is never built.
func chainScopeBindings(rt Runtime, bindings []store.Binding, chains []db.ChainRow, sc Scope) []store.Binding {
	member := map[string]bool{}
	for _, c := range chains {
		for _, name := range view.ReadMembers(rt.Store, c) {
			member[name] = true
		}
	}
	kept := make([]store.Binding, 0, len(bindings))
	for _, b := range bindings {
		if scopeBinding(b, sc) || member[b.Name] {
			kept = append(kept, b)
		}
	}
	return kept
}

// doneHiddenBy is the count view.HideDone would have removed from a full
// report under sc. A row the scope never built is still a row a human was
// told was hidden, so the count comes from the stored bindings and chains
// rather than the rows built: the DONE bindings this scope hides that no
// chain row stands in for, plus the DONE chain rows a chain still earns.
//
// A chain's member row is replaced by the chain's own row, so a DONE
// member is never counted here, and neither is a dropped chain.
func doneHiddenBy(s *store.Store, bindings []store.Binding, chains []db.ChainRow, sc Scope) int {
	if sc.Named || sc.All {
		return 0
	}
	member, present := map[string]bool{}, map[string]bool{}
	for _, b := range bindings {
		present[b.Name] = true
	}
	for _, c := range chains {
		for _, name := range view.ReadMembers(s, c) {
			member[name] = true
		}
	}
	hidden := 0
	for _, b := range bindings {
		if b.State == store.StateDone && !member[b.Name] && inMastermindScope(b.MasterMindID, sc) {
			hidden++
		}
	}
	for _, c := range chains {
		if c.Parent != "" || !inMastermindScope(c.MasterMindID, sc) {
			continue
		}
		if view.ChainStoreState(c.Status) == store.StateDone && anyPresent(view.ReadMembers(s, c), present) {
			hidden++
		}
	}
	return hidden
}

// inMastermindScope is ScopeReport's mastermind filter asked of an id
// instead of a built row: a named view takes no filter, and an empty id
// is every mastermind's.
func inMastermindScope(id string, sc Scope) bool {
	return sc.Named || sc.MasterMindID == "" || id == sc.MasterMindID
}

func anyPresent(names []string, present map[string]bool) bool {
	for _, n := range names {
		if present[n] {
			return true
		}
	}
	return false
}

// scopedChains keeps the scope's chains and the children hanging under
// them, so narrowing to one mastermind never orphans a fork child into
// a root.
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
	// repeats until no kept parent gains a child.
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
