package stats

import (
	"sort"
	"strings"

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

// RepoRow is one repo bucket and its children: the repo's own GroupRow, its
// labelled features, and its feature-less bucket. Each feature carries its own
// tickets, so a label two repos use appears under each with only that repo's
// rows counted.
type RepoRow struct {
	GroupRow
	Features  []FeatureRow // only this repo's rows with a Feature
	NoFeature FeatureRow   // this repo's "(none)" feature bucket; zero when every row has a feature
	Scratch   []RepoRow    // the fold row's own repos; nil on every other row
}

// ScratchKey is the reserved key the scratch repos fold into: one RepoRow
// carrying the aggregate and its repos in Scratch.
const ScratchKey = "(scratch)"

// isScratchRepoKey reports whether a repo key is a scratch repo's common dir.
// A scratch repo has no origin_url, so db.RoundRow.Repo carries git.RepoFacts'
// absolute, symlink-resolved common_dir, which starts with a separator; a
// remote origin normalises to an https URL (or stays http) instead, so it
// never does. An origin that is itself a local path counts as scratch: it has
// no remote host.
func isScratchRepoKey(key string) bool {
	return strings.HasPrefix(key, "/")
}

// foldRepoKey is the key a row's repo aggregates under: a scratch repo folds
// into ScratchKey, every other repo into its own key.
func foldRepoKey(r db.RoundRow) (string, bool) {
	k, ok := repoKey(r)
	if !ok {
		return "", false
	}
	if isScratchRepoKey(k) {
		return ScratchKey, true
	}
	return k, true
}

// FeatureRow is one feature's bucket and the labelled tickets nested under it.
// Its own numbers include the feature's ticketless rounds; Tickets holds only
// the rounds that also carry a ticket.
type FeatureRow struct {
	GroupRow
	Tickets []GroupRow // only this feature's rows with a Ticket
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

// buildRepos groups the rows by repo and, inside each repo, nests each
// feature's tickets under it. A repo's children count only its own rows, so a
// label two repos use appears under each with that repo's numbers. A round with
// neither label counts only in its repo's (no feature) bucket. Every scratch
// repo folds into the one ScratchKey row, which carries the aggregate and the
// individual repos in Scratch. The repos come back in groupRows order: rounds
// desc, key asc.
func buildRepos(in Inputs, rows []db.RoundRow) []RepoRow {
	byRepo := map[string][]db.RoundRow{}
	for _, r := range rows {
		k, ok := repoKey(r)
		if !ok {
			continue
		}
		byRepo[k] = append(byRepo[k], r)
	}
	groups := groupRows(in, rows, foldRepoKey)
	out := make([]RepoRow, 0, len(groups))
	for _, g := range groups {
		if g.Key != ScratchKey {
			out = append(out, repoRow(in, g, byRepo[g.Key]))
			continue
		}
		out = append(out, scratchRepoRow(in, g, byRepo))
	}
	return out
}

// scratchRepoRow is the fold row over every scratch repo: g is the aggregate,
// byRepo supplies each path's own rows for its Scratch child, and the fold's
// own feature buckets stay zero because a feature across repos is nothing
// renderable.
func scratchRepoRow(in Inputs, g GroupRow, byRepo map[string][]db.RoundRow) RepoRow {
	scratch := make([]RepoRow, 0, len(byRepo))
	for _, sg := range groupRows(in, foldRows(byRepo), repoKey) {
		scratch = append(scratch, repoRow(in, sg, byRepo[sg.Key]))
	}
	return RepoRow{GroupRow: g, Scratch: scratch}
}

// foldRows flattens the rows of every scratch repo, which is what the fold's
// own children are grouped over.
func foldRows(byRepo map[string][]db.RoundRow) []db.RoundRow {
	var out []db.RoundRow
	for k, rows := range byRepo {
		if !isScratchRepoKey(k) {
			continue
		}
		out = append(out, rows...)
	}
	return out
}

// repoRow is one repo's row over its own rows: g is its aggregate and own the
// rows that belong to it, which the feature and ticket buckets each scope to.
func repoRow(in Inputs, g GroupRow, own []db.RoundRow) RepoRow {
	byFeature := map[string][]db.RoundRow{}
	var featureless []db.RoundRow
	for _, r := range own {
		if r.Feature == nil {
			featureless = append(featureless, r)
			continue
		}
		byFeature[*r.Feature] = append(byFeature[*r.Feature], r)
	}
	features := make([]FeatureRow, 0, len(byFeature))
	for _, f := range groupRows(in, own, featureKey) {
		features = append(features, FeatureRow{
			GroupRow: f,
			Tickets:  groupRows(in, byFeature[f.Key], ticketKey),
		})
	}
	return RepoRow{
		GroupRow: g,
		Features: features,
		NoFeature: FeatureRow{
			GroupRow: noneRow(in, own, noFeatureKey),
			Tickets:  groupRows(in, featureless, ticketKey),
		},
	}
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
