package dash

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/histq"
)

type roundColLayout struct {
	hasRepo    bool
	hasTreeCom bool
	hasTokens  bool
	bindWidth  int
}

func (m Model) roundLayout(indented bool) roundColLayout {
	cw := m.width - 6
	if cw < 0 {
		cw = 0
	}
	avail := cw
	if indented {
		avail -= 2
	}
	layout := roundColLayout{
		hasRepo:    true,
		hasTreeCom: true,
		hasTokens:  true,
	}
	if avail-106 >= 12 {
		layout.bindWidth = avail - 106
		return layout
	}
	layout.hasRepo = false
	if avail-82 >= 12 {
		layout.bindWidth = avail - 82
		return layout
	}
	layout.hasTreeCom = false
	if avail-66 >= 12 {
		layout.bindWidth = avail - 66
		return layout
	}
	layout.hasTokens = false
	w := avail - 57
	if w < 12 {
		w = 12
	}
	layout.bindWidth = w
	return layout
}

// roundLine is one round row.
func (m Model) roundLine(r db.RoundRow, cursor, indented bool) string {
	cw := m.width - 6
	if cw < 0 {
		cw = 0
	}
	layout := m.roundLayout(indented)
	var parts []string
	parts = m.appendRoundStarted(parts, r, cursor)
	parts = m.appendRoundBinding(parts, r, layout, cursor)
	parts = m.appendRoundNumber(parts, r, cursor)
	parts = m.appendRoundCandidate(parts, r, cursor)
	if layout.hasRepo {
		parts = m.appendRoundRepo(parts, r, cursor)
	}
	var outWord string
	parts, outWord = m.appendRoundOutcome(parts, r, cursor)
	if layout.hasTreeCom {
		parts = m.appendRoundTree(parts, r, cursor)
	}
	if layout.hasTokens {
		parts = m.appendRoundTokens(parts, r, cursor)
	}
	parts = m.appendRoundTook(parts, r, outWord, cursor)

	sep := "  "
	if cursor {
		sep = m.styles.Selected.Render("  ")
	}
	line := strings.Join(parts, sep)
	if indented {
		indent := "  "
		if cursor {
			indent = m.styles.Selected.Render("  ")
		}
		line = indent + line
	}
	if cursor {
		if lipgloss.Width(line) > cw {
			line = lipgloss.NewStyle().MaxWidth(cw).Render(line)
		}
		if w := lipgloss.Width(line); w < cw {
			line += m.styles.Selected.Render(strings.Repeat(" ", cw-w))
		}
		return "   " + line + "   "
	}
	content := fit(line, cw)
	return "   " + content + "   "
}

// STARTED: StartedAt.In(loc).Format("15:04"), Dim, width 7
func (m Model) appendRoundStarted(parts []string, r db.RoundRow, cursor bool) []string {
	return append(parts, m.cell(r.StartedAt.In(m.loc).Format("15:04"), 7, false, m.styles.Dim, cursor))
}

// BINDING: binding name, Fg (Strong on cursor row), width layout.bindWidth
func (m Model) appendRoundBinding(parts []string, r db.RoundRow, layout roundColLayout, cursor bool) []string {
	bStyle := m.styles.Fg
	if cursor {
		bStyle = m.styles.Strong
	}
	return append(parts, m.cell(r.BindingName, layout.bindWidth, false, bStyle, cursor))
}

// RND: r<n>, Dim, width 4
func (m Model) appendRoundNumber(parts []string, r db.RoundRow, cursor bool) []string {
	return append(parts, m.cell(fmt.Sprintf("r%d", r.Number), 4, false, m.styles.Dim, cursor))
}

// CANDIDATE: nameOf(Candidate), · when nil, Dim, width 21
func (m Model) appendRoundCandidate(parts []string, r db.RoundRow, cursor bool) []string {
	candText := "·"
	if r.Candidate != nil {
		candText = m.nameOf(*r.Candidate)
	}
	return append(parts, m.cell(candText, 21, false, m.styles.Dim, cursor))
}

// REPO: ShortRepo(*Repo), (no repo) when nil, Dim, width 22
func (m Model) appendRoundRepo(parts []string, r db.RoundRow, cursor bool) []string {
	repoText := "(no repo)"
	if r.Repo != nil {
		repoText = ShortRepo(*r.Repo)
	}
	return append(parts, m.cell(repoText, 22, false, m.styles.Dim, cursor))
}

// OUTCOME: outcomeWord, outcomeStyle, width 10
func (m Model) appendRoundOutcome(parts []string, r db.RoundRow, cursor bool) ([]string, string) {
	outWord, outStyle := m.outcomeWord(r)
	return append(parts, m.cell(outWord, 10, false, outStyle, cursor)), outWord
}

// COMMITS & TREE
func (m Model) appendRoundTree(parts []string, r db.RoundRow, cursor bool) []string {
	if r.Commits != nil {
		cStyle := m.styles.Fg
		if *r.Commits == 0 {
			cStyle = m.styles.Faint
		}
		parts = append(parts, m.cell(strconv.Itoa(*r.Commits), 7, true, cStyle, cursor))
	} else {
		parts = append(parts, m.cell("·", 7, true, m.styles.Faint, cursor))
	}

	if r.Tree != nil {
		tStyle := m.styles.Faint
		if *r.Tree == "dirty" {
			tStyle = m.styles.Warn
		}
		parts = append(parts, m.cell(*r.Tree, 5, false, tStyle, cursor))
	} else {
		parts = append(parts, m.cell("·", 5, false, m.styles.Faint, cursor))
	}
	return parts
}

// TOKENS: shortTokens(sum); · when all four token columns are nil, Fg, width 7
func (m Model) appendRoundTokens(parts []string, r db.RoundRow, cursor bool) []string {
	if r.InTokens == nil && r.CacheTokens == nil && r.WriteTokens == nil && r.OutTokens == nil {
		return append(parts, m.cell("·", 7, true, m.styles.Fg, cursor))
	}
	return append(parts, m.cell(shortTokens(histq.RowTokens(r)), 7, true, m.styles.Fg, cursor))
}

// TOOK: shortDuration; for running round now-StartedAt; for nil DurationMS it is ·, Dim, width 5
func (m Model) appendRoundTook(parts []string, r db.RoundRow, outWord string, cursor bool) []string {
	tookText := "·"
	if outWord == "running" {
		durMS := m.now().Sub(r.StartedAt).Milliseconds()
		tookText = shortDuration(durMS)
	} else if r.DurationMS != nil {
		tookText = shortDuration(*r.DurationMS)
	}
	return append(parts, m.cell(tookText, 5, true, m.styles.Dim, cursor))
}

// roundHeader labels the round grid's columns.
func (m Model) roundHeader() string {
	cw := m.width - 6
	if cw < 0 {
		cw = 0
	}
	layout := m.roundLayout(false)
	var parts []string
	add := func(label string, width int, alignRight bool) {
		if alignRight {
			parts = append(parts, fmt.Sprintf("%*s", width, label))
		} else {
			parts = append(parts, pad(label, width))
		}
	}
	add("STARTED", 7, false)
	add("BINDING", layout.bindWidth, false)
	add("RND", 4, false)
	add("CANDIDATE", 21, false)
	if layout.hasRepo {
		add("REPO", 22, false)
	}
	add("OUTCOME", 10, false)
	if layout.hasTreeCom {
		add("COMMITS", 7, true)
		add("TREE", 5, false)
	}
	if layout.hasTokens {
		add("TOKENS", 7, true)
	}
	add("TOOK", 5, true)

	line := strings.Join(parts, "  ")
	styled := m.styles.Faint.Bold(true).Render(line)
	return "   " + fit(styled, cw) + "   "
}
