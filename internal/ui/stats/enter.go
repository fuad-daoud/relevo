package stats

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	datastats "github.com/fuad-daoud/relevo/internal/stats"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

// cutFor turns a stats window into the instant before which rounds are
// ignored: zero ("all") for the literal all, else ParseSince's cut.
func cutFor(window string, now time.Time) time.Time {
	if window == "all" {
		return time.Time{}
	}
	cut, err := relevo.ParseSince(window, now)
	if err != nil {
		return time.Time{}
	}
	return cut
}

// sinceTerm is the query's ` since:<window>` term, omitted for all.
func (v statsView) sinceTerm() string {
	if v.window == "all" {
		return ""
	}
	return " since:" + v.window
}

// enter opens `:rounds` filtered to the focused panel's selected row. On
// the repos tab the cursor indexes the visible rows: every repo row, plus the
// children of the expanded repos.
func (v statsView) enter(env widget.Env) (widget.View, tea.Cmd) {
	if v.focus == 0 {
		names := env.Src.Base().Candidates.NameOf
		rows := v.overviewCandRows(names)
		if len(rows) == 0 {
			return v, nil
		}
		i := statsClamp(v.cursor[0], len(rows))
		if rows[i].Idle {
			return v, notice(names(rows[i].Token) + " has no rounds in this window")
		}
		return v.roundsForCandidate(env, rows[i].Token)
	}
	rows := v.repoTabRows()
	if len(rows) == 0 {
		return v, nil
	}
	r := rows[statsClamp(v.cursor[1], len(rows))]
	switch r.kind {
	case kindFeature:
		return v.enterRepoChild(env, r, "feature")
	case kindTicket:
		return v.enterRepoChild(env, r, "ticket")
	}
	key := r.group.Key
	if key == "(none)" {
		return v, notice("rounds with no repo cannot be filtered")
	}
	if key == datastats.ScratchKey {
		return v, notice("scratch repos cannot be filtered as one")
	}
	return v.roundsForRepo(env, key)
}

// enterRepoChild opens `:rounds` for a feature or ticket child, scoped to its
// repo. A ticket under a labelled feature also carries that feature's
// term; a ticket under the (no feature) bucket has none. A "(none)" label keeps
// its notice, and a child of the "(none)" repo cannot be expressed -- no query
// term means "no repo" -- so it notices.
func (v statsView) enterRepoChild(env widget.Env, r repoTabRow, term string) (widget.View, tea.Cmd) {
	if r.group.Key == "(none)" {
		return v, notice("rounds with no " + term + " cannot be filtered")
	}
	if r.parent == "(none)" {
		return v, notice("rounds with no repo cannot be filtered")
	}
	query := `repo:"` + r.parent + `"`
	if r.feature != "" && r.feature != "(none)" {
		query += ` feature:"` + r.feature + `"`
	}
	return v.openRounds(env, query+` `+term+`:"`+r.group.Key+`"`+v.sinceTerm())
}

// enterOverview opens filtered rounds for the overview table's selected row.
func (v statsView) enterOverview(env widget.Env) (widget.View, tea.Cmd) {
	if v.ovFocus == 0 {
		names := env.Src.Base().Candidates.NameOf
		rows := v.overviewCandRows(names)
		if len(rows) == 0 {
			return v, nil
		}
		i := statsClamp(v.ovCursor[0], len(rows))
		if rows[i].Idle {
			return v, notice(names(rows[i].Token) + " has no rounds in this window")
		}
		return v.roundsForCandidate(env, rows[i].Token)
	}
	rows := v.overviewRepoRows()
	if len(rows) == 0 {
		return v, nil
	}
	i := statsClamp(v.ovCursor[1], len(rows))
	key := rows[i].Key
	if key == "(none)" {
		return v, notice("rounds with no repo cannot be filtered")
	}
	if key == datastats.ScratchKey {
		return v, notice("scratch repos cannot be filtered as one")
	}
	return v.roundsForRepo(env, key)
}

// roundsForCandidate is the `candidate:<token><since>` query both the tabs'
// enter and the overview's share.
func (v statsView) roundsForCandidate(env widget.Env, token string) (widget.View, tea.Cmd) {
	return v.openRounds(env, "candidate:"+token+v.sinceTerm())
}

// roundsForRepo is the `repo:"<key>"<since>` query both the tabs' enter and
// the overview's share.
func (v statsView) roundsForRepo(env widget.Env, key string) (widget.View, tea.Cmd) {
	return v.openRounds(env, `repo:"`+key+`"`+v.sinceTerm())
}

// openRounds pushes a rounds view on q, or notices the error.
func (v statsView) openRounds(env widget.Env, q string) (widget.View, tea.Cmd) {
	rv, init, err := v.open(env, q)
	if err != nil {
		return v, notice("no database: " + err.Error())
	}
	return v, push(rv, init)
}
