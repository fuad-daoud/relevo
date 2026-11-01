package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/ui/widget"
	"github.com/fuad-daoud/relevo/internal/view"
)

func (f fleetView) cardLines(env widget.Env, b view.BindingStatus, width int) []string {
	g := groupOf(b)
	title := cardTitle(b, env.Now)
	metaContent := cardMeta(b, g)
	keysContent := cardKeysRow(cardKeysFor(g, env.Actions != nil))

	return renderCard(width, title, "", []string{metaContent, "", keysContent})
}

func cardTitle(b view.BindingStatus, now time.Time) string {
	sr := shownRound(b)
	rn := rowNow(b, now)
	if sr > 0 {
		rn = strings.TrimPrefix(rn, fmt.Sprintf("r%d · ", sr))
	}
	roundPart := fmt.Sprintf("round %d · %s", sr, rn)
	if sr == 0 {
		roundPart = rn
	}
	return widget.AccentStyle.Bold(true).Render(b.Key()) + "  " + widget.FaintStyle.Render(roundPart)
}

func cardMeta(b view.BindingStatus, g fleetGroup) string {
	var metaParts []string
	mastermindName := mastermindCell(b)
	if g == groupNeedsYou {
		metaParts = append(metaParts, widget.MutedStyle.Render("MasterMind ")+widget.WarnStyle.Render(mastermindName))
	} else {
		metaParts = append(metaParts, widget.MutedStyle.Render("MasterMind "+mastermindName))
	}
	metaParts = append(metaParts, widget.MutedStyle.Render(candidateText(b)))
	branch := b.Branch
	if branch == "" {
		branch = repoCell(b)
	}
	if branch != "" {
		metaParts = append(metaParts, widget.MutedStyle.Render(branch))
	}
	if b.LastClose != nil && b.LastClose.Commits > 0 {
		commitWord := "commits"
		if b.LastClose.Commits == 1 {
			commitWord = "commit"
		}
		metaParts = append(metaParts, widget.MutedStyle.Render(fmt.Sprintf("+%d %s", b.LastClose.Commits, commitWord)))
	}
	if s := spendCell(b); s != "" {
		metaParts = append(metaParts, widget.MutedStyle.Render(s))
	}
	return "  " + strings.Join(metaParts, widget.MutedStyle.Render("  ·  "))
}

func cardKeysFor(g fleetGroup, actions bool) []widget.KeyHelp {
	if !actions {
		return []widget.KeyHelp{{Key: "enter", Help: "open round"}}
	}
	switch g {
	case groupNeedsYou:
		return []widget.KeyHelp{
			{Key: "enter", Help: "open round"},
			{Key: "s", Help: "send the next plan"},
			{Key: "o", Help: "shell"},
			{Key: "x", Help: "stop"},
		}
	case groupWorking:
		return []widget.KeyHelp{
			{Key: "enter", Help: "open round"},
			{Key: "x", Help: "stop"},
			{Key: "g", Help: "gate"},
			{Key: "o", Help: "shell"},
		}
	case groupIdle:
		return []widget.KeyHelp{
			{Key: "s", Help: "send the next plan"},
			{Key: "enter", Help: "open round"},
			{Key: "D", Help: "done"},
			{Key: "o", Help: "shell"},
		}
	case groupHeld, groupOther:
		return []widget.KeyHelp{
			{Key: "enter", Help: "open round"},
			{Key: "s", Help: "send"},
			{Key: "D", Help: "done"},
		}
	case groupDone:
		return []widget.KeyHelp{
			{Key: "enter", Help: "open round"},
			{Key: "u", Help: "unbind"},
		}
	}
	return nil
}

func gatedLine(env widget.Env, width int) string {
	if len(env.Report.Gated) == 0 {
		return ""
	}
	var order []string
	latest := map[string]time.Time{}
	for _, g := range env.Report.Gated {
		p := g.Token
		parts := strings.Split(g.Token, "/")
		if len(parts) >= 2 {
			p = parts[1]
		}
		if prev, ok := latest[p]; !ok {
			order = append(order, p)
			latest[p] = g.Until
		} else if g.Until.After(prev) {
			latest[p] = g.Until
		}
	}

	var entries []string
	for _, p := range order {
		until := latest[p]
		left := "until cleared"
		if !until.IsZero() {
			left = ago(env.Now, until)
		}
		entries = append(entries, fmt.Sprintf("%s %s", p, left))
	}

	line := "   " + widget.RedStyle.Render("◌") + widget.FaintStyle.Render(" gated  ") +
		strings.Join(entries, widget.FaintStyle.Render("  ·  ")) +
		widget.FaintStyle.Render("  ·  the pick skips them")
	return widget.Fit(clipName(line, width), width)
}

type fleetLine struct {
	text string
	row  int
}

type fleetSlotKind int

const (
	slotBlank fleetSlotKind = iota
	slotSection
	slotRow
	slotQuote
	slotFold
)

// Laying the list out and painting it are separate steps, so the window can
// be chosen on the layout and only the lines inside it are ever rendered.
type fleetSlot struct {
	kind  fleetSlotKind
	row   int
	group fleetGroup
	count int
	quote string
}

func (f fleetView) fleetListSlots(env widget.Env, rows []view.BindingStatus) []fleetSlot {
	q := f.activeFilter()
	groups := []fleetGroup{groupNeedsYou, groupWorking, groupIdle, groupHeld, groupOther}
	if f.showDone || q != "" {
		groups = append(groups, groupDone)
	}

	grouped := map[fleetGroup][]int{}
	for i := range rows {
		g := groupOf(rows[i])
		grouped[g] = append(grouped[g], i)
	}

	var slots []fleetSlot
	for _, g := range groups {
		idx := grouped[g]
		if len(idx) == 0 {
			continue
		}
		slots = append(slots, fleetSlot{kind: slotSection, row: -1, group: g, count: len(idx)})

		for _, i := range idx {
			slots = append(slots, fleetSlot{kind: slotRow, row: i, group: g})
			if w := rows[i].Waiting; rows[i].Display == "NEEDS YOU" && w != nil && w.Line != "" {
				slots = append(slots, fleetSlot{kind: slotQuote, row: i, group: g, quote: w.Line})
			}
		}
		slots = append(slots, fleetSlot{kind: slotBlank, row: -1})
	}

	if !f.showDone && q == "" && anyDone(env.Report.Bindings) {
		slots = append(slots, fleetSlot{kind: slotFold, row: -1})
	}

	return slots
}

func (f fleetView) paintSlot(s fleetSlot, rows []view.BindingStatus, env widget.Env, width int) string {
	switch s.kind {
	case slotSection:
		return widget.Fit(sectionLine(s.group, s.count), width)
	case slotRow:
		return fleetRowLine(rows[s.row], s.group, s.row == f.cursor, env.Now, width)
	case slotQuote:
		return quoteLine(s.quote, s.row == f.cursor, width)
	case slotFold:
		return doneFoldLine(doneRows(env.Report.Bindings), env.Now, width)
	}
	return widget.Fit("", width)
}

func (f fleetView) fleetListLines(env widget.Env, width int) []fleetLine {
	rows := f.rows(env)
	slots := f.fleetListSlots(env, rows)
	list := make([]fleetLine, 0, len(slots))
	for _, s := range slots {
		list = append(list, fleetLine{text: f.paintSlot(s, rows, env, width), row: s.row})
	}
	return list
}

func (f fleetView) cardBlock(env widget.Env, width, height int) []string {
	rows := f.rows(env)
	if len(rows) == 0 || height < 16 {
		return nil
	}
	sel := rows[clampCursor(f.cursor, len(rows))]
	card := f.cardLines(env, sel, width)
	return append(card, widget.Fit("", width))
}

func gatedBlock(env widget.Env, width int) []string {
	if gl := gatedLine(env, width); gl != "" {
		return []string{gl}
	}
	return nil
}

func (f fleetView) tableHeight(env widget.Env) int {
	bh := widget.BodyHeight(env)
	fixedH := 1
	if f.filtering {
		fixedH++
	}
	h := bh - fixedH - len(f.cardBlock(env, env.Width, bh)) - len(gatedBlock(env, env.Width))
	if h < 0 {
		return 0
	}
	return h
}

func (f fleetView) windowTopLines(lines []fleetLine, height int) int {
	return windowTopIndex(len(lines), height, f.cursor, f.top, func(i int) int { return lines[i].row })
}

func (f fleetView) windowTopSlots(slots []fleetSlot, height int) int {
	return windowTopIndex(len(slots), height, f.cursor, f.top, func(i int) int { return slots[i].row })
}

// windowTopIndex is the first of n lines to draw so the cursor's row is
// wholly inside the next height lines, moved as little as possible.
func windowTopIndex(n, height, cursor, top int, rowOf func(int) int) int {
	if height <= 0 || n <= height {
		return 0
	}
	first, last := -1, -1
	for i := 0; i < n; i++ {
		if rowOf(i) == cursor {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return 0
	}
	if top > n-height {
		top = n - height
	}
	if top < 0 {
		top = 0
	}
	if last >= top+height {
		top = last - height + 1
	}
	if first < top {
		top = first
	}
	return top
}

func (f fleetView) windowTop(rows []view.BindingStatus, env widget.Env) int {
	slots := f.fleetListSlots(env, rows)
	return f.windowTopSlots(slots, f.tableHeight(env))
}

// The window is chosen on the layout, so a line that falls outside it is
// never rendered.
func (f fleetView) fleetWindowLines(env widget.Env, width, listH int) []string {
	rows := f.rows(env)
	slots := f.fleetListSlots(env, rows)
	start := f.windowTopSlots(slots, listH)
	end := len(slots)
	if listH > 0 && start+listH < end {
		end = start + listH
	}

	window := make([]string, 0, listH)
	if start < len(slots) {
		for _, s := range slots[start:end] {
			window = append(window, f.paintSlot(s, rows, env, width))
		}
	}
	for len(window) < listH {
		window = append(window, widget.Fit("", width))
	}
	return window
}

func (f fleetView) Body(env widget.Env, width, height int) string {
	if !env.Loaded {
		return strings.Join(widget.BlockLines([]string{"loading…"}, width, height), "\n")
	}
	if len(env.Report.Bindings) == 0 {
		return strings.Join(emptyPaneBlock(width, height), "\n")
	}

	var fixed []string
	fixed = append(fixed, widget.Fit("", width))
	if f.filtering {
		fixed = append(fixed, widget.Fit(f.filter.View(), width))
	}

	card := f.cardBlock(env, width, height)
	gated := gatedBlock(env, width)

	listH := height - len(fixed) - len(card) - len(gated)
	if listH < 0 {
		listH = 0
	}

	window := f.fleetWindowLines(env, width, listH)

	var out []string
	out = append(out, fixed...)
	out = append(out, card...)
	out = append(out, window...)
	out = append(out, gated...)

	return strings.Join(widget.FitLines(out, width, height), "\n")
}
