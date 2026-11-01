package dash

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/histq"
)

type groupColLayout struct {
	hasBindings  bool
	hasCommits   bool
	hasTokensPct bool
	keyWidth     int
}

func (m Model) groupLayout() groupColLayout {
	cw := m.width - 6
	if cw < 0 {
		cw = 0
	}
	hasTokensPct := m.width >= 110
	hasCommits := m.width >= 90
	hasBindings := m.width >= 90 && m.query.By != histq.AxisBinding

	numCols := 6                    // KEY, RNDS, DONE, HALTED, TOKENS, LAST
	fixedWidth := 5 + 5 + 6 + 7 + 9 // 32
	if hasBindings {
		numCols++
		fixedWidth += 8
	}
	if hasCommits {
		numCols++
		fixedWidth += 7
	}
	if hasTokensPct {
		numCols++
		fixedWidth += 15
	}
	seps := 2 * (numCols - 1)
	kw := cw - fixedWidth - seps
	if kw < 12 {
		kw = 12
	}
	return groupColLayout{
		hasBindings:  hasBindings,
		hasCommits:   hasCommits,
		hasTokensPct: hasTokensPct,
		keyWidth:     kw,
	}
}

func (m Model) groupLine(g histq.GroupRow, cursor bool) string {
	cw := m.width - 6
	if cw < 0 {
		cw = 0
	}
	layout := m.groupLayout()
	var parts []string
	parts = m.appendGroupKey(parts, g, layout, cursor)
	parts = m.appendGroupRounds(parts, g, cursor)
	if layout.hasBindings {
		parts = m.appendGroupBindings(parts, g, cursor)
	}
	parts = m.appendGroupOutcomes(parts, g, cursor)
	if layout.hasCommits {
		parts = m.appendGroupCommits(parts, g, cursor)
	}
	parts = m.appendGroupTokens(parts, g, cursor)
	if layout.hasTokensPct {
		parts = m.appendGroupTokensPct(parts, g, cursor)
	}
	parts = m.appendGroupLast(parts, g, cursor)

	sep := "  "
	if cursor {
		sep = m.styles.Selected.Render("  ")
	}
	line := strings.Join(parts, sep)
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

// KEY
func (m Model) appendGroupKey(parts []string, g histq.GroupRow, layout groupColLayout, cursor bool) []string {
	kStyle := m.styles.Fg
	if cursor {
		kStyle = m.styles.Strong
	}
	return append(parts, m.cell(m.groupKeyLabel(g.Key), layout.keyWidth, false, kStyle, cursor))
}

// RNDS
func (m Model) appendGroupRounds(parts []string, g histq.GroupRow, cursor bool) []string {
	return append(parts, m.cell(strconv.Itoa(g.Rounds), 5, true, m.styles.Dim, cursor))
}

// BINDINGS
func (m Model) appendGroupBindings(parts []string, g histq.GroupRow, cursor bool) []string {
	distinct := make(map[string]bool)
	for _, r := range g.Rows {
		distinct[r.BindingName] = true
	}
	return append(parts, m.cell(strconv.Itoa(len(distinct)), 8, true, m.styles.Dim, cursor))
}

// DONE & HALTED
func (m Model) appendGroupOutcomes(parts []string, g histq.GroupRow, cursor bool) []string {
	doneCnt := 0
	haltCnt := 0
	for _, r := range g.Rows {
		w, _ := m.outcomeWord(r)
		switch w {
		case "done":
			doneCnt++
		case "halted":
			haltCnt++
		}
	}
	parts = append(parts, m.cell(strconv.Itoa(doneCnt), 5, true, m.styles.Dim, cursor))
	return append(parts, m.cell(strconv.Itoa(haltCnt), 6, true, m.styles.Dim, cursor))
}

// COMMITS
func (m Model) appendGroupCommits(parts []string, g histq.GroupRow, cursor bool) []string {
	return append(parts, m.cell(strconv.Itoa(g.Commits), 7, true, m.styles.Dim, cursor))
}

// TOKENS
func (m Model) appendGroupTokens(parts []string, g histq.GroupRow, cursor bool) []string {
	if g.Tokens == 0 {
		return append(parts, m.cell("·", 7, true, m.styles.Faint, cursor))
	}
	return append(parts, m.cell(shortTokens(g.Tokens), 7, true, m.styles.Dim, cursor))
}

// % TOKENS
func (m Model) appendGroupTokensPct(parts []string, g histq.GroupRow, cursor bool) []string {
	var total int64
	for _, grp := range m.groups {
		total += grp.Tokens
	}
	var pct int
	if total > 0 {
		pct = int(math.Round(float64(100*g.Tokens) / float64(total)))
	}
	return append(parts, m.tokensBar(pct, cursor))
}

// LAST
func (m Model) appendGroupLast(parts []string, g histq.GroupRow, cursor bool) []string {
	var lastStr string
	if isToday(g.Last, m.loc, m.now()) {
		lastStr = "today"
	} else {
		lastStr = strings.ToLower(g.Last.In(m.loc).Format("Jan 02"))
	}
	return append(parts, m.cell(lastStr, 9, true, m.styles.Dim, cursor))
}

func (m Model) groupHeader() string {
	cw := m.width - 6
	if cw < 0 {
		cw = 0
	}
	layout := m.groupLayout()
	var parts []string
	add := func(label string, width int, alignRight bool) {
		if alignRight {
			parts = append(parts, fmt.Sprintf("%*s", width, label))
		} else {
			parts = append(parts, pad(label, width))
		}
	}

	keyLabel := strings.ToUpper(string(m.query.By))
	if m.query.By == histq.AxisCandidate {
		keyLabel = "CANDIDATE"
	}
	add(keyLabel, layout.keyWidth, false)
	add("RNDS", 5, true)
	if layout.hasBindings {
		add("BINDINGS", 8, true)
	}
	add("DONE", 5, true)
	add("HALTED", 6, true)
	if layout.hasCommits {
		add("COMMITS", 7, true)
	}
	add("TOKENS", 7, true)
	if layout.hasTokensPct {
		add("% TOKENS", 15, true)
	}
	add("LAST", 9, true)

	line := strings.Join(parts, "  ")
	styled := m.styles.Faint.Bold(true).Render(line)
	return "   " + fit(styled, cw) + "   "
}

// groupKeyLabel formats a group key display string.
func (m Model) groupKeyLabel(key string) string {
	if key == "-" || key == "" {
		switch m.query.By {
		case histq.AxisRepo:
			return "(no repo)"
		case histq.AxisFeature:
			return "(no feature)"
		case histq.AxisTicket:
			return "(no ticket)"
		default:
			return "(none)"
		}
	}
	switch m.query.By {
	case histq.AxisRepo:
		return ShortRepo(key)
	case histq.AxisCandidate:
		return m.nameOf(key)
	default:
		return key
	}
}

// tokensBar formats a 15-cell tokens bar.
func (m Model) tokensBar(pct int, band bool) string {
	barCount := int(math.Round(float64(pct) / 10.0))
	if barCount < 0 {
		barCount = 0
	}
	if barCount > 10 {
		barCount = 10
	}
	barStr := strings.Repeat("▇", barCount)
	barPadded := pad(barStr, 10)
	accent := m.styles.Accent
	fg := m.styles.Fg
	space := " "
	if band {
		bg := m.styles.Selected.GetBackground()
		accent = accent.Background(bg)
		fg = fg.Background(bg)
		space = m.styles.Selected.Render(" ")
	}
	return accent.Render(barPadded) + space + fg.Render(fmt.Sprintf("%3d%%", pct))
}
