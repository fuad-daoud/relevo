package stats

import (
	"sort"

	"github.com/fuad-daoud/relevo/internal/db"
)

type GroupRow struct {
	Key                    string
	Rounds, Halted, Landed int
	Bindings               int // distinct BindingID among the group's rows
	Done                   int // rows whose ReportOutcome is "done"
	ReportHalted           int // rows whose ReportOutcome is "halted"
	Commits                int // sum of the non-nil RoundRow.Commits
	CostUSD                float64
	Tokens                 int64 // in+cache+write+out over the group's rows
	RoundsPerLand          float64
	ByCandidate            map[string]int // round count per Candidate, nil skipped
}

// RepoRow is one repo bucket and its children: the repo's own GroupRow,
// its labelled features and tickets, and the per-repo feature-less and
// ticket-less buckets. A label two repos use appears under each with only that
// repo's rows counted.
type RepoRow struct {
	GroupRow
	Features  []GroupRow // only this repo's rows with a Feature
	NoFeature GroupRow   // this repo's "(none)" feature bucket; zero when every row has a feature
	Tickets   []GroupRow // only this repo's rows with a Ticket
	NoTicket  GroupRow   // this repo's "(none)" ticket bucket; zero when every row has a ticket
}

type Outcomes struct {
	ByRound  map[string]int // db outcome values: reported, halted, exited, switched, done_no_report, open
	ByReport map[string]int // done, halted, blocked, deferred, "no outcome" (= unstructured)
}

type groupAcc struct {
	rounds, halted int
	done           int
	reportHalted   int
	commits        int
	cost           float64
	landedRounds   int
	bindings       map[string]bool
	byCandidate    map[string]int
	landed         map[string]bool
	tokens         TokenCounts
}

func newGroupAcc() *groupAcc {
	return &groupAcc{landed: map[string]bool{}, bindings: map[string]bool{}, byCandidate: map[string]int{}}
}

func (a *groupAcc) add(in Inputs, r db.RoundRow) {
	a.rounds++
	a.bindings[r.BindingID] = true
	if r.ReportOutcome != nil {
		switch *r.ReportOutcome {
		case "done":
			a.done++
		case "halted":
			a.reportHalted++
		}
	}
	if r.Commits != nil {
		a.commits += *r.Commits
	}
	if r.Candidate != nil {
		a.byCandidate[*r.Candidate]++
	}
	addTokens(&a.tokens, r)
	if r.Outcome == db.OutcomeHalted {
		a.halted++
	}
	if costKnown(in, r) {
		a.cost += *r.CostUSD
	}
	if in.Landed[r.BindingID] {
		a.landed[r.BindingID] = true
		a.landedRounds++
	}
}

func (a *groupAcc) row(k string) GroupRow {
	g := GroupRow{
		Key:          k,
		Rounds:       a.rounds,
		Halted:       a.halted,
		Landed:       len(a.landed),
		Bindings:     len(a.bindings),
		Done:         a.done,
		ReportHalted: a.reportHalted,
		Commits:      a.commits,
		ByCandidate:  a.byCandidate,
		CostUSD:      a.cost,
		Tokens:       a.tokens.Total(),
	}
	if g.Landed > 0 {
		g.RoundsPerLand = float64(a.landedRounds) / float64(g.Landed)
	}
	return g
}

// groupRows skips a row whose key returns false: that section does not cover it.
func groupRows(in Inputs, rows []db.RoundRow, key func(db.RoundRow) (string, bool)) []GroupRow {
	accs := map[string]*groupAcc{}
	var order []string
	for _, r := range rows {
		k, ok := key(r)
		if !ok {
			continue
		}
		a := accs[k]
		if a == nil {
			a = newGroupAcc()
			accs[k] = a
			order = append(order, k)
		}
		a.add(in, r)
	}

	out := make([]GroupRow, 0, len(order))
	for _, k := range order {
		out = append(out, accs[k].row(k))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rounds != out[j].Rounds {
			return out[i].Rounds > out[j].Rounds
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// buildRepos groups the rows by repo and, inside each repo, by feature and by
// ticket. A repo's children count only its own rows, so a label two repos use
// appears under each with that repo's numbers . The repos come back in
// groupRows order: rounds desc, key asc.
func buildRepos(in Inputs, rows []db.RoundRow) []RepoRow {
	byRepo := map[string][]db.RoundRow{}
	for _, r := range rows {
		k, ok := repoKey(r)
		if !ok {
			continue
		}
		byRepo[k] = append(byRepo[k], r)
	}
	groups := groupRows(in, rows, repoKey)
	out := make([]RepoRow, 0, len(groups))
	for _, g := range groups {
		own := byRepo[g.Key]
		out = append(out, RepoRow{
			GroupRow:  g,
			Features:  groupRows(in, own, featureKey),
			NoFeature: noneRow(in, own, noFeatureKey),
			Tickets:   groupRows(in, own, ticketKey),
			NoTicket:  noneRow(in, own, noTicketKey),
		})
	}
	return out
}

// noneRow is the single "(none)" bucket over rows, or the zero GroupRow when
// no row lacks the label.
func noneRow(in Inputs, rows []db.RoundRow, key func(db.RoundRow) (string, bool)) GroupRow {
	if g := groupRows(in, rows, key); len(g) > 0 {
		return g[0]
	}
	return GroupRow{}
}

func repoKey(r db.RoundRow) (string, bool) {
	if r.Repo == nil {
		return "(none)", true
	}
	return *r.Repo, true
}

func featureKey(r db.RoundRow) (string, bool) {
	if r.Feature == nil {
		return "", false
	}
	return *r.Feature, true
}

func noFeatureKey(r db.RoundRow) (string, bool) {
	if r.Feature == nil {
		return "(none)", true
	}
	return "", false
}

func ticketKey(r db.RoundRow) (string, bool) {
	if r.Ticket == nil {
		return "", false
	}
	return *r.Ticket, true
}

func noTicketKey(r db.RoundRow) (string, bool) {
	if r.Ticket == nil {
		return "(none)", true
	}
	return "", false
}

func buildOutcomes(rows []db.RoundRow) Outcomes {
	out := Outcomes{
		ByRound:  map[string]int{},
		ByReport: map[string]int{},
	}
	for _, r := range rows {
		out.ByRound[r.Outcome]++
		if r.ReportOutcome == nil {
			continue
		}
		v := *r.ReportOutcome
		if v == "unstructured" {
			v = "no outcome"
		}
		out.ByReport[v]++
	}
	return out
}
