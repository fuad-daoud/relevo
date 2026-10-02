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

// chainFactsOf is one chain row as the view types carry it. A halted or
// stopped chain whose builder member has a round open -- a manual round a
// human sent while the chain was down -- carries that round as ManualRound, so
// the row reads the chain's own status word and names the round in flight
// instead of claiming NEEDS YOU.
func chainFactsOf(s *store.Store, c db.ChainRow) view.ChainFacts {
	lf := chainStoredFactsOf(c)
	f := view.ChainFacts{
		Status:      c.Status,
		Phase:       lf.Phase,
		Step:        lf.Step,
		Plan:        lf.Plan,
		Plans:       lf.Plans,
		Corrections: lf.Corrections,
		Awaiting:    lf.Awaiting,
		Reason:      c.Reason,
	}
	if len(c.StateJSON) > 0 {
		chainFlowFacts(&f, c)
	}
	if (c.Status == string(chain.StatusHalted) || c.Status == string(chain.StatusStopped)) &&
		chainBuilderRoundOpen(s, c) {
		if b, err := s.Load(c.Builder); err == nil {
			f.ManualRound = b.Round
		}
	}
	return f
}

// chainFlowFacts fills a workflow chain's own facts: the step its engine is
// on, the round (or check run) it awaits, and its position in the plan input.
func chainFlowFacts(f *view.ChainFacts, c db.ChainRow) {
	st, err := chainWorkflowState(c)
	if err != nil {
		return
	}
	f.StepAt = st.Awaiting.Step
	if f.StepAt == "" {
		f.StepAt = st.At
	}
	f.Round = st.Awaiting.Round
	f.Check = st.Awaiting.Step != "" && st.Awaiting.Member == ""
	if f.Check {
		f.Round = st.Awaiting.Run
	}
	it := st.Iter["plans"]
	f.PlanTotal = len(it.Items)
	f.PlanPos = it.Index + 1
	if f.PlanPos < 1 {
		f.PlanPos = 1
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

// viewChainRow is the synthetic row that stands in for a chain: the
// chain's name, the tree it works in, the display word every surface shares,
// and the chain's own facts. It names the mastermind the chain belongs to, so
// the mastermind filter and the attention sort read it like any other row.
func viewChainRow(s *store.Store, c db.ChainRow) view.BindingStatus {
	f := chainFactsOf(s, c)
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

// applyChains replaces the member rows of every chain with the chain's own
// row, so the mastermind reads one row per chain instead of its members'.
//
// The rows are re-sorted, so a chain that waits on a human rises to the top of
// the listing like any other NEEDS YOU row.
func applyChains(s *store.Store, rep view.Report, chains []db.ChainRow) view.Report {
	member := map[string]db.ChainRow{}
	for _, c := range chains {
		for _, name := range chainReadMembers(s, c) {
			member[name] = c
		}
	}
	if len(member) == 0 {
		return rep
	}

	placed := map[string]bool{}
	rows := make([]view.BindingStatus, 0, len(rep.Bindings))
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
			rows = append(rows, viewChainRow(s, c))
		}
	}
	// A chain whose member rows are all gone from the report still exists, so
	// it still gets its row. Chains() orders by name, so the tail is stable.
	for _, c := range chains {
		if c.Status == string(chain.StatusDone) {
			continue
		}
		if !placed[c.Name] {
			rows = append(rows, viewChainRow(s, c))
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
	// A chain that runs on a server answers from the server's view, so the
	// status a human reads is the chain's, not this machine's stale mirror.
	if chainOnServer(c) {
		return chainServerStatus(ctx, rt, c)
	}
	bindings, err := rt.Store.List()
	if err != nil {
		return view.Report{}, err
	}
	rep, err := buildReport(ctx, rt, bindings)
	if err != nil {
		return view.Report{}, err
	}

	rows := []view.BindingStatus{viewChainRow(rt.Store, c)}
	for _, member := range chainReadMembers(rt.Store, c) {
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
