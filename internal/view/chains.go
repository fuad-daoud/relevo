package view

import (
	"sort"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// MembersOf is a chain's member binding names in the order a chain view
// lists them: the builder first, then the reviewer, the planner and the
// security member. A part the row leaves empty is skipped.
func MembersOf(c db.ChainRow) []string {
	var out []string
	for _, name := range []string{c.Builder, c.Reviewer, c.Planner, c.Security} {
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// ReadMembers names a chain's members for a read surface: the bindings its
// engine recorded, in creation order, when the row carries a state; the legacy
// part columns otherwise. A part the row leaves empty is skipped.
func ReadMembers(s *store.Store, c db.ChainRow) []string {
	if len(c.WorkflowJSON) > 0 {
		rows, err := s.ChainMembers(c.Name)
		if err != nil {
			return nil
		}
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			if row.Binding != "" {
				out = append(out, row.Binding)
			}
		}
		return out
	}
	return MembersOf(c)
}

// PendingMembers names, in member order, the members of a chain that still
// hold a payload the MasterMind has not collected: an unconfirmed to-mastermind
// entry nobody has read back.
//
// It reads each member's own log rather than the roll-up's: a chain row
// replaces its members, so this is the only place that can still say which one
// is holding something.
//
// A member whose record is gone answers nothing and is not named, and a log that
// cannot be read names nothing: a chain row must not claim a member is stranded
// on the strength of a read that failed.
func PendingMembers(s *store.Store, c db.ChainRow) []string {
	var out []string
	for _, member := range ReadMembers(s, c) {
		entries, err := s.ReadLog(member)
		if err != nil {
			continue
		}
		if chainHoldsUncollected(entries) {
			out = append(out, member)
		}
	}
	return out
}

// chainHoldsUncollected reports whether the log holds an entry still waiting on
// the MasterMind. An admitted entry is excluded along with a confirmed one: a
// push route has taken it, so nobody is waiting on it here.
func chainHoldsUncollected(entries []store.LogEntry) bool {
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && !e.Confirmed && e.AdmittedAt == nil {
			return true
		}
	}
	return false
}

// EndMembers names the members a chain's end verbs walk: the bindings its
// engine recorded in chain_member, in creation order, when the row carries a
// workflow; the legacy part columns otherwise. A binding the row leaves empty
// is skipped. A read failure keeps the legacy path, so an end verb on a legacy
// row reads the same as ever.
func EndMembers(m chain.MemberLister, c db.ChainRow) []string {
	if len(c.WorkflowJSON) == 0 {
		return MembersOf(c)
	}
	rows, err := m.ChainMembers(c.Name)
	if err != nil {
		return MembersOf(c)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Binding != "" {
			out = append(out, row.Binding)
		}
	}
	return out
}

// FindingsOfState reads a chain's security finding count from its engine
// state: the `findings` outcome the workflow's scan step recorded in Results. A
// row with no engine state, or a scan that never closed, carries 0.
func FindingsOfState(c db.ChainRow) int {
	st, err := chain.WorkflowState(c)
	if err != nil {
		return 0
	}
	return FlowFindings(st)
}

// FlowFindings is the finding count a state recorded: the newest step result
// whose outcomes carry a `findings` count. Only a security run step declares
// one, so this is that step's count; the round breaks a tie when a workflow
// runs more than one.
func FlowFindings(st workflow.State) int {
	steps := make([]string, 0, len(st.Results))
	for step := range st.Results {
		steps = append(steps, step)
	}
	sort.Strings(steps)
	findings, round := 0, -1
	for _, step := range steps {
		val, ok := st.Results[step].Outcomes["findings"]
		if !ok {
			continue
		}
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 {
			continue
		}
		if r := st.Results[step].Round; r >= round {
			findings, round = n, r
		}
	}
	return findings
}

// StoredFacts is a chain's fixed-column facts as a read surface shows
// them: the engine state's legacy projection when the row carries a state, the
// stored columns otherwise.
type StoredFacts struct {
	Phase       string
	Step        string
	Plan        int
	Plans       int
	Corrections int
	Awaiting    string
}

// StoredFactsOf projects a chain's facts onto the legacy columns a read
// surface shows. A row with a state reads them from the state through
// workflow.LegacyView, so the columns themselves stay unread; the plan count is
// kept at least one, as the row's own validity asks.
func StoredFactsOf(c db.ChainRow) StoredFacts {
	if len(c.StateJSON) > 0 {
		def, derr := chain.WorkflowDef(c)
		st, serr := chain.WorkflowState(c)
		if derr == nil && serr == nil {
			lf := workflow.LegacyView(def, st)
			plan, plans := lf.Plan, lf.Plans
			if plans < 1 {
				plan, plans = 1, 1
			}
			return StoredFacts{
				Phase: lf.Phase, Step: lf.Step, Plan: plan, Plans: plans,
				Corrections: lf.Corrections, Awaiting: lf.AwaitingMember,
			}
		}
	}
	return StoredFacts{
		Phase: c.Phase, Step: c.Step, Plan: c.Plan, Plans: c.Plans,
		Corrections: c.Corrections, Awaiting: c.AwaitingMember,
	}
}

// FactsOf is one chain row as the types of this package carry it. A halted or
// stopped chain whose builder member has a round open -- a manual round a
// human sent while the chain was down -- carries that round as ManualRound, so
// the row reads the chain's own status word and names the round in flight
// instead of claiming NEEDS YOU.
func FactsOf(s *store.Store, c db.ChainRow) ChainFacts {
	lf := StoredFactsOf(c)
	f := ChainFacts{
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
		ChildFacts(s, &f, c)
	}
	// A DONE chain names no pending member: Done() deliberately leaves the log
	// history in place, so the stranded entries on every member would read as
	// pending forever. Halted and stopped chains keep the names, where pending
	// is meaningful and interacts with ManualRound.
	if c.Status != string(chain.StatusDone) {
		f.PendingMembers = PendingMembers(s, c)
	}
	if (c.Status == string(chain.StatusHalted) || c.Status == string(chain.StatusStopped)) &&
		BuilderRoundOpen(s, c) {
		if b, err := s.Load(c.Builder); err == nil {
			f.ManualRound = b.Round
		}
	}
	return f
}

// chainFlowFacts fills a workflow chain's own facts: the step its engine is
// on, the round (or check run) it awaits, and its position in the plan input.
func chainFlowFacts(f *ChainFacts, c db.ChainRow) {
	st, err := chain.WorkflowState(c)
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
	f.PlanPos, f.PlanTotal = FlowPlanPos(st)
}

// FlowPlanPos is a plans walk's position for a status surface: the
// exhausted walk reads as its last plan, exactly as workflow.LegacyView
// projects it, so a chain past its plans (scan, security fixes, done) reads 6/6
// and not the reset 1/6.
func FlowPlanPos(st workflow.State) (pos, total int) {
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

// ChainStoreState maps a chain's status onto the stored binding state the
// existing row rules read: a halted or stopped chain waits on a human, so its
// row takes the state a human-facing row takes -- and a live chain never reads
// as DONE, or the done-hiding rule would drop a chain that still has work.
func ChainStoreState(status string) store.State {
	switch status {
	case string(chain.StatusHalted), string(chain.StatusStopped):
		return store.StateNeedsYou
	case string(chain.StatusDone):
		return store.StateDone
	default:
		return store.StateActive
	}
}

// ChainRow is the synthetic row that stands in for a chain: the
// chain's name, the tree it works in, the display word every surface shares,
// and the chain's own facts. It names the mastermind the chain belongs to, so
// the mastermind filter and the attention sort read it like any other row.
func ChainRow(s *store.Store, c db.ChainRow) BindingStatus {
	f := FactsOf(s, c)
	row := BindingStatus{
		Name:            c.Name,
		CWD:             c.Worktree,
		State:           string(ChainStoreState(c.Status)),
		Display:         ChainDisplay(f),
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

// ApplyChains replaces the member rows of every chain with the chain's own
// row, so the mastermind reads one row per chain instead of its members'. A
// fork's child is the exception: it takes no top-level row of its own, and is
// threaded in under the parent that forked it.
//
// The rows are re-sorted, so a chain that waits on a human rises to the top of
// the listing like any other NEEDS YOU row.
func ApplyChains(s *store.Store, rep Report, chains []db.ChainRow) Report {
	member := map[string]db.ChainRow{}
	for _, c := range chains {
		for _, name := range ReadMembers(s, c) {
			member[name] = c
		}
	}
	if len(member) == 0 {
		return rep
	}

	placed := map[string]bool{}
	rows := make([]BindingStatus, 0, len(rep.Bindings))
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
	rows = SortRows(rows, true)
	rows = appendChainChildRows(s, rows, chains)
	rep.Bindings = rows
	return rep
}

// chainTopRow is the row a chain prints when it is not a fork's child.
func chainTopRow(s *store.Store, c db.ChainRow) BindingStatus {
	return ChainRow(s, c)
}

// appendChainChildRows inserts every fork child under the parent row it belongs
// to, in key order. A child whose parent is not in the row set -- a parent that
// is done, or a chain on another surface -- is skipped rather than printed at
// the top level, because the child has no top level of its own.
func appendChainChildRows(s *store.Store, rows []BindingStatus, chains []db.ChainRow) []BindingStatus {
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
		nested := make([]BindingStatus, 0, len(kids))
		for _, k := range kids {
			nested = append(nested, childRowOf(s, k))
		}
		rows = append(rows[:at+1], append(nested, rows[at+1:]...)...)
	}
	return rows
}

// ChildRows is a chain's child chains in key order, with how many of them
// have ended. The rows whose Parent names this chain are its children; the key
// order is the order the engine named them in ("1".."N"), so the listing reads
// in fork order rather than in whatever order the query returned.
func ChildRows(s *store.Store, c db.ChainRow) (children []db.ChainRow, done int) {
	if s == nil {
		return nil, 0
	}
	rows, err := s.Chains()
	if err != nil {
		return nil, 0
	}
	for _, r := range rows {
		if r.Parent == c.Name && r.Parent != "" {
			children = append(children, r)
		}
	}
	sortChainRowsByKey(children)
	for _, r := range children {
		if chain.Status(r.Status) == chain.StatusDone {
			done++
		}
	}
	return children, done
}

// sortChainRowsByKey orders child rows by their fork key, the suffix after the
// parent name. Keys are the short digit runs the engine assigned in order, so
// the order is the declaration order -- and that order is numeric, not textual:
// with ten or more children a plain string comparison reads "10" before "2".
// A key that does not parse as a number -- which the engine never writes --
// falls back to the name comparison, so the sort still orders the rows.
func sortChainRowsByKey(rows []db.ChainRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, aok := chainRowForkKey(rows[i])
		b, bok := chainRowForkKey(rows[j])
		if aok && bok && a != b {
			return a < b
		}
		return rows[i].Name < rows[j].Name
	})
}

// chainRowForkKey is a row's own fork key -- its name with its own
// "<parent>." prefix trimmed -- as the integer the engine declared it with. ok
// is false for a row whose key is not a number.
func chainRowForkKey(c db.ChainRow) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(c.Name, c.Parent+"."))
	if err != nil {
		return 0, false
	}
	return n, true
}

// ChildFacts fills a chain's fork facts: the child chain names in key order
// and how many of them are done.
func ChildFacts(s *store.Store, f *ChainFacts, c db.ChainRow) {
	children, done := ChildRows(s, c)
	for _, ch := range children {
		f.Children = append(f.Children, ch.Name)
	}
	f.ChildrenDone = done
}

// childRowOf is the row a child chain prints under its parent: the child's
// own chain row, unchanged in every other way. The nesting itself is read from
// the facts the parent the row names, so the status and the statusline cannot
// disagree about which rows are nested. Its halt reason is already the row's
// detail, exactly as any halted chain's is.
func childRowOf(s *store.Store, c db.ChainRow) BindingStatus {
	return ChainRow(s, c)
}

// BuilderRoundOpen reports whether a chain's builder member has a round
// open: a prompt entry for its current round with no report entry for it yet,
// or a queued round that has not started. A builder record that is gone, or
// unreadable, has no open round.
func BuilderRoundOpen(s *store.Store, c db.ChainRow) bool {
	b, err := s.Load(c.Builder)
	if err != nil {
		return false
	}
	if !b.QueuedAt.IsZero() {
		return true
	}
	entries, err := s.ReadLog(c.Builder)
	if err != nil {
		return false
	}
	return store.RoundOpen(entries, b.Round)
}
