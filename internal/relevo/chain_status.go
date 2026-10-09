package relevo

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
	"github.com/fuad-daoud/relevo/internal/workflow"
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
		Parent:      c.Parent,
	}
	if len(c.StateJSON) > 0 {
		chainFlowFacts(&f, c)
	}
	// A parent's own children are the fork's progress; a child's row carries
	// the parent instead, and its own children are none of its own rows' business.
	if c.Parent == "" {
		chainChildFacts(s, &f, c)
	}
	// A DONE chain names no pending member: Done() deliberately leaves the log
	// history in place, so the stranded entries on every member would read as
	// pending forever. Halted and stopped chains keep the names, where pending
	// is meaningful and interacts with ManualRound.
	if c.Status != string(chain.StatusDone) {
		f.PendingMembers = chainPendingMembers(s, c)
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
	f.PlanTotal, f.PlanPos = chainFlowPlanPos(st)
}

// chainFlowPlanPos is a plans walk's position for a status surface: the
// exhausted walk reads as its last plan, exactly as workflow.LegacyView
// projects it, so a chain past its plans (scan, security fixes, done) reads 6/6
// and not the reset 1/6.
func chainFlowPlanPos(st workflow.State) (pos, total int) {
	it := st.Iter["plans"]
	pos, total = it.Index+1, len(it.Items)
	if it.Done && total > 0 {
		pos = total
	}
	if pos < 1 {
		pos = 1
	}
	return pos, total
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
// row, so the mastermind reads one row per chain instead of its members'. A
// fork's child is the exception: it takes no top-level row of its own, and is
// threaded in under the parent that forked it.
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
		// A fork's child is never a row of the top level: it prints under the
		// parent that forked it, and only there (appendChainChildRows).
		if c.Parent != "" {
			continue
		}
		// The chain row takes the place of the first of its members, and the
		// other member rows go with it.
		if !placed[c.Name] {
			placed[c.Name] = true
			rows = append(rows, chainTopRow(s, c))
		}
	}
	// A chain whose member rows are all gone from the report still exists, so
	// it still gets its row. Chains() orders by name, so the tail is stable.
	for _, c := range chains {
		if c.Status == string(chain.StatusDone) || c.Parent != "" {
			continue
		}
		if !placed[c.Name] {
			placed[c.Name] = true
			rows = append(rows, chainTopRow(s, c))
		}
	}
	// The top-level rows sort first, so a chain that waits on a human rises to
	// the top of the listing like any other NEEDS YOU row. Only then are a
	// fork's children threaded in under the parent that forked them, in key
	// order, each with its own step and status: a child is a chain, and a halted
	// child says why. Inserting them last is what keeps the sort from pulling a
	// halted child ahead of the still-running parent it belongs under.
	rows = view.SortRows(rows, true)
	rows = appendChainChildRows(s, rows, chains)
	rep.Bindings = rows
	return rep
}

// chainTopRow is the row a chain prints when it is not a fork's child.
func chainTopRow(s *store.Store, c db.ChainRow) view.BindingStatus {
	return viewChainRow(s, c)
}

// appendChainChildRows inserts every fork child under the parent row it belongs
// to, in key order. A child whose parent is not in the row set -- a parent that
// is done, or a chain on another surface -- is skipped rather than printed at
// the top level, because the child has no top level of its own.
func appendChainChildRows(s *store.Store, rows []view.BindingStatus, chains []db.ChainRow) []view.BindingStatus {
	children := map[string][]db.ChainRow{}
	for _, c := range chains {
		if c.Parent != "" {
			children[c.Parent] = append(children[c.Parent], c)
		}
	}
	for parent, kids := range children {
		sortChainRowsByKey(kids)
		at := -1
		for i, r := range rows {
			if r.Name == parent {
				at = i
				break
			}
		}
		if at < 0 {
			continue
		}
		nested := make([]view.BindingStatus, 0, len(kids))
		for _, k := range kids {
			nested = append(nested, chainChildRowOf(s, k))
		}
		rows = append(rows[:at+1], append(nested, rows[at+1:]...)...)
	}
	return rows
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
