package relevo

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// chainMembersOf is a chain's member binding names in the order a chain view
// lists them: the builder first, then the reviewer, the planner and the
// security member. A part the row leaves empty is skipped.
func chainMembersOf(c db.ChainRow) []string {
	var out []string
	for _, name := range []string{c.Builder, c.Reviewer, c.Planner, c.Security} {
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// chainFactsOf is one chain row as the view types carry it.
func chainFactsOf(c db.ChainRow) view.ChainFacts {
	return view.ChainFacts{
		Status:      c.Status,
		Phase:       c.Phase,
		Step:        c.Step,
		Plan:        c.Plan,
		Plans:       c.Plans,
		Corrections: c.Corrections,
		Awaiting:    c.AwaitingMember,
		Reason:      c.Reason,
	}
}

// chainStoreState maps a chain's status onto the stored binding state the
// existing row rules read: a halted or stopped chain waits on a human, so its
// row takes the state a human-facing row takes -- and a live chain never reads
// as DONE, or the done-hiding rule would drop a chain that still has work.
func chainStoreState(status string) store.State {
	switch status {
	case string(chain.StatusHalted), string(chain.StatusStopped):
		return store.StateNeedsYou
	case string(chain.StatusDone):
		return store.StateDone
	default:
		return store.StateActive
	}
}

// viewChainRow is the synthetic row that stands in for a live chain: the
// chain's name, the tree it works in, the display word every surface shares,
// and the chain's own facts. It names the mastermind the chain belongs to, so
// the mastermind filter and the attention sort read it like any other row.
func viewChainRow(c db.ChainRow) view.BindingStatus {
	f := chainFactsOf(c)
	row := view.BindingStatus{
		Name:            c.Name,
		CWD:             c.Worktree,
		State:           string(chainStoreState(c.Status)),
		Display:         view.ChainDisplay(f),
		Role:            "chain",
		MasterMindID:    c.MasterMindID,
		MasterMindRoute: "pull",
		Chain:           &f,
	}
	// The halt reason is the row's detail, exactly as a broken binding's is:
	// the word says a human must act, and the detail says on what.
	if c.Status == string(chain.StatusHalted) {
		row.Detail = c.Reason
	}
	return row
}

// applyChains replaces the member rows of every chain that is still live with
// the chain's own row, so the mastermind reads one row per chain instead of its
// members'. A chain whose status is done leaves its members' rows alone: that
// work is over, and each member's own row is the honest one.
//
// The rows are re-sorted, so a chain that waits on a human rises to the top of
// the listing like any other NEEDS YOU row.
func applyChains(rep view.Report, chains []db.ChainRow) view.Report {
	member := map[string]db.ChainRow{}
	live := make([]db.ChainRow, 0, len(chains))
	for _, c := range chains {
		if c.Status == string(chain.StatusDone) {
			continue
		}
		live = append(live, c)
		for _, name := range chainMembersOf(c) {
			member[name] = c
		}
	}
	if len(member) == 0 {
		return rep
	}

	placed := map[string]bool{}
	rows := make([]view.BindingStatus, 0, len(rep.Bindings)+len(live))
	for _, b := range rep.Bindings {
		c, ok := member[b.Name]
		if !ok {
			rows = append(rows, b)
			continue
		}
		// The chain row takes the place of the first of its members, and the
		// other member rows go with it.
		if !placed[c.Name] {
			placed[c.Name] = true
			rows = append(rows, viewChainRow(c))
		}
	}
	// A chain whose member rows are all gone from the report still exists, so
	// it still gets its row. Chains() orders by name, so the tail is stable.
	for _, c := range live {
		if !placed[c.Name] {
			rows = append(rows, viewChainRow(c))
		}
	}
	rep.Bindings = view.SortRows(rows, true)
	return rep
}

// ChainStatus is `relevo status <chain>`: the chain's own row with its member
// bindings listed underneath, builder first. The members keep their ordinary
// rows here whatever the chain's status -- naming a chain asks for the whole
// chain, not only the roll-up.
func ChainStatus(ctx context.Context, rt Runtime, name string) (view.Report, error) {
	c, err := rt.Store.Chain(name)
	if err != nil {
		return view.Report{}, err
	}
	bindings, err := rt.Store.List()
	if err != nil {
		return view.Report{}, err
	}
	rep, err := buildReport(ctx, rt, bindings)
	if err != nil {
		return view.Report{}, err
	}

	rows := []view.BindingStatus{viewChainRow(c)}
	for _, member := range chainMembersOf(c) {
		for _, b := range rep.Bindings {
			if b.Name == member {
				rows = append(rows, b)
				break
			}
		}
	}
	rep.Bindings = rows
	return rep, nil
}
