package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
	"github.com/fuad-daoud/relevo/internal/view"
)

func envNow(env widget.Env) func() time.Time { return func() time.Time { return env.Now } }

func newRoundPane(env widget.Env) roundPane {
	return roundPane{
		src:     env.Src,
		ctx:     env.Ctx,
		now:     envNow(env),
		report:  env.Report,
		width:   env.Width,
		rows:    widget.BodyHeight(env),
		actions: env.Actions != nil,
	}
}

func newRoundView(env widget.Env, key string, round int) (widget.View, tea.Cmd) {
	p := newRoundPane(env)
	p.actions = env.Actions != nil
	var cmd tea.Cmd
	p, cmd = p.pointDetailAt(key)
	if round > 0 {
		p.detail.round = round
		p.artifactSel = 0
		for t := tab(0); t < tabCount; t++ {
			p.detail.cache[t] = tabContent{}
			p.detail.scroll[t] = 0
		}
		p.fillViewport()
		if c := p.startFetch(); c != nil {
			cmd = tea.Batch(cmd, c)
		}
	}
	// A binding whose report is ready is opened on its report tab, which the
	// pane reads like any other tab. That read is non-claiming: the cockpit is
	// the human's own view, so opening a round leaves the payload pending for
	// the route that pushes it.
	if b := row(env.Report, key); b != nil && reportReady(*b) {
		p.detail.active = tabReport
		p.fillViewport()
		cmd = tea.Batch(cmd, p.reportFetch())
	}
	return roundView{pane: p, actions: env.Actions != nil}, cmd
}

// The member's row is not in the shell's report: the round opens against a
// report carrying the chain's own member rows, and carries them as the view's
// extra so every later status refresh keeps the pane's target resolvable.
func newChainRoundView(env widget.Env, key string, round int, members []view.BindingStatus, crumbs []string) (widget.View, tea.Cmd) {
	env.Report = mergeRows(env.Report, members)
	v, cmd := newRoundView(env, key, round)
	if rv, ok := v.(roundView); ok {
		rv.extra = members
		rv.crumbs = crumbs
		v = rv
	}
	return v, cmd
}

func newHistRoundView(env widget.Env, h relevo.HistoryBinding, round int) (widget.View, tea.Cmd) {
	p := newRoundPane(env)
	p.actions = false
	var cmd tea.Cmd
	p, cmd = p.pointDetailAtHist(h)
	if round > 0 {
		p.detail.round = round
		p.artifactSel = 0
		for t := tab(0); t < tabCount; t++ {
			p.detail.cache[t] = tabContent{}
			p.detail.scroll[t] = 0
		}
		p.fillViewport()
		if c := p.startFetch(); c != nil {
			cmd = tea.Batch(cmd, c)
		}
	}
	return roundView{pane: p}, cmd
}

// A mastermind row keys by Name, a server row by owner/name, so two clients'
// same-named bindings never collide.
func row(rep view.Report, key string) *view.BindingStatus {
	for i := range rep.Bindings {
		if rep.Bindings[i].Key() == key {
			return &rep.Bindings[i]
		}
	}
	return nil
}

// While a round is in flight, r.Round is the running round, so Round - 1 alone
// wrongly targets the previous (finished) round instead of the live tail:
// prefer r.PlanRound when > 0, else r.Round - 1 (a binding with no plan yet,
// or a row from an older relevo serve whose JSON lacks plan_round).
func paneRound(r view.BindingStatus) int {
	if r.PlanRound > 0 {
		return r.PlanRound
	}
	return r.Round - 1
}

func roundsOf(r view.BindingStatus) int {
	if r.PlanRound > 0 {
		return r.PlanRound
	}
	return r.Round
}

type roundView struct {
	pane    roundPane
	actions bool
	// extra carries report rows the shell's report no longer holds: a chain's
	// own members are folded into its synthetic row by the cockpit's read
	// model, so a round drilled in from the chains view lends them back for
	// as long as the view is on screen.
	extra []view.BindingStatus
	// crumbs replaces the default binding-and-round breadcrumb when the round
	// was reached through a chain, so the trail reads chains › chain › step
	// rather than naming the member twice.
	crumbs []string
	// detailKey and detailRow are the row this view fetched for itself, with
	// the three figures the fleet report leaves nil (Live, LiveUsage,
	// Headless.Tail). They are nil until the fetch lands: a row with no detail
	// fetch in flight keeps the fleet's nil figures, and a failed one keeps
	// them too (onDetailRow).
	detailKey string
	detailRow *view.BindingStatus
}

// The shell's own row wins, and a member spliced in twice is one row.
func mergeRows(rep view.Report, extra []view.BindingStatus) view.Report {
	if len(extra) == 0 {
		return rep
	}
	have := make(map[string]bool, len(rep.Bindings))
	for _, b := range rep.Bindings {
		have[b.Key()] = true
	}
	out := rep
	out.Bindings = append([]view.BindingStatus(nil), rep.Bindings...)
	for _, b := range extra {
		if have[b.Key()] {
			continue
		}
		have[b.Key()] = true
		out.Bindings = append(out.Bindings, b)
	}
	return out
}

func (r roundView) Crumbs() []string {
	if len(r.crumbs) > 0 {
		return r.crumbs
	}
	return []string{r.pane.detail.name, fmt.Sprintf("r%d", r.pane.detail.round)}
}

// The view's own rows are merged in, so a chain member's round names its
// binding after a status refresh as it did when it was opened.
func (r roundView) Context(env widget.Env) (string, string) {
	b := row(r.rows(env), r.pane.detail.name)

	rightText := fmt.Sprintf("round %d of %d", r.pane.detail.round, r.pane.detail.rounds)
	if !r.pane.detail.archivedAt.IsZero() {
		rightText += " · archived " + r.pane.detail.archivedAt.Format("2006-01-02")
	} else if b != nil && r.pane.detail.live && r.pane.detail.round == r.pane.detail.rounds && b.RoundEnd.IsZero() {
		rightText += " · live"
	}
	right := widget.FaintStyle.Render(rightText) + " "

	if b == nil || !r.pane.detail.live {
		left := "   " + widget.MutedStyle.Render(r.pane.detailHeader())
		if lipgloss.Width(left)+1+lipgloss.Width(right) > env.Width {
			right = ""
		}
		return left, right
	}

	left, right := contextLine(roundStateCell(*b, env.Now), r.pane.contextCells(*b), right, env.Width)
	return left, right
}

// When the line is too wide, the cells after the state-and-time cell drop
// whole, from the right; the state-and-time cell never drops.
func contextLine(stateCell string, cells []string, right string, width int) (string, string) {
	build := func() string {
		s := stateCell
		for _, c := range cells {
			s += c
		}
		return s
	}
	left := build()
	if lipgloss.Width(left)+1+lipgloss.Width(right) > width {
		right = ""
	}
	for len(cells) > 0 && lipgloss.Width(left) > width {
		cells = cells[:len(cells)-1]
		left = build()
	}
	return left, right
}

// A writer and a reader with the same state draw the same cell.
func roundStateCell(b view.BindingStatus, now time.Time) string {
	pill, age := roundState(b, now)
	return pill + age
}

func roundState(b view.BindingStatus, now time.Time) (pill, age string) {
	g := groupOf(b)
	var pStyle lipgloss.Style
	var pWord string
	switch g {
	case groupNeedsYou:
		pStyle, pWord = widget.ChipWarnStyle.Bold(true), "needs you"
	case groupWorking:
		pStyle, pWord = widget.ChipGreenStyle.Bold(true), "working"
	case groupIdle:
		pStyle, pWord = widget.KbdStyle, "idle"
	case groupHeld:
		pStyle, pWord = widget.KbdStyle, "on hold"
	case groupDone:
		pStyle, pWord = widget.KbdStyle, "done"
	default:
		pStyle, pWord = widget.KbdStyle, strings.ToLower(b.Display)
	}

	var ageStr string
	if g == groupWorking {
		if a := ago(b.RoundStart, now); a != "" {
			ageStr = widget.TextStyle.Bold(true).Render(a)
			if b.QuietFor != "" {
				ageStr += widget.MutedStyle.Render(" · quiet " + b.QuietFor)
			}
		} else if b.QuietFor != "" {
			ageStr = widget.MutedStyle.Render("quiet " + b.QuietFor)
		}
	} else {
		rn := rowNow(b, now)
		if b.Round > 0 {
			rn = strings.TrimPrefix(rn, fmt.Sprintf("r%d · ", b.Round))
		}
		ageStr = widget.TextStyle.Render(rn)
	}
	return "   " + widget.Chip(pStyle, pWord), "   " + ageStr
}

func (p roundPane) contextCells(b view.BindingStatus) []string {
	if p.reader {
		return p.readerContextCells(b)
	}
	cells := []string{"      " + widget.TextStyle.Render(candidateText(b))}
	cells = append(cells, widget.FaintStyle.Render("  ·  ")+widget.MutedStyle.Render(mastermindWordOf(b)))
	branch := b.Branch
	if branch == "" {
		branch = repoCell(b)
	}
	if branch != "" {
		cells = append(cells, widget.FaintStyle.Render("  ·  ")+widget.MutedStyle.Render(branch))
	}
	if b.Dirty {
		cells = append(cells, widget.FaintStyle.Render("  ·  ")+widget.RedStyle.Render("dirty"))
	}
	return cells
}

func (p roundPane) readerContextCells(b view.BindingStatus) []string {
	var cells []string
	c := p.detail.cache[tabArtifacts]
	if artifactCount(c) > 0 {
		cells = append(cells,
			widget.FaintStyle.Render("  ·  ")+widget.MutedStyle.Render(artifactsWord(artifactCount(c))),
			widget.FaintStyle.Render("  ·  ")+widget.MutedStyle.Render(relevo.ArtifactSizeText(artifactTotalSize(c))))
	}
	if !p.roundOpen(b) {
		cells = append(cells, widget.FaintStyle.Render("  ·  ")+widget.MutedStyle.Render("repository unchanged"))
	}
	cells = append(cells,
		"      "+widget.TextStyle.Render(actorCell(b))+widget.FaintStyle.Render(" on ")+widget.TextStyle.Render(candidateText(b)),
		widget.FaintStyle.Render("  ·  ")+widget.MutedStyle.Render("reader"),
		widget.FaintStyle.Render("  ·  ")+widget.MutedStyle.Render(mastermindWordOf(b)),
		widget.FaintStyle.Render("  ·  ")+widget.MutedStyle.Render(scratchText(p.baselineHead)))
	return cells
}

func mastermindWordOf(b view.BindingStatus) string {
	if b.OwnerLabel != "" {
		return "client " + b.OwnerLabel
	}
	return mastermindCell(b)
}

// roundOpen reports whether the round on screen is the binding's own open
// round: the writer's "live" round, and the one a reader's scratch worktree
// has not yet been reported unchanged against.
func (p roundPane) roundOpen(b view.BindingStatus) bool {
	return p.detail.live && p.detail.round == b.Round && b.RoundEnd.IsZero()
}

func scratchText(head string) string {
	if head == "" {
		return "scratch + dirty diff"
	}
	if len(head) > 7 {
		head = head[:7]
	}
	return "scratch @ " + head + " + dirty diff"
}

func (r roundView) Keys() []widget.KeyHelp {
	if r.pane.reader {
		var keys []widget.KeyHelp
		if r.pane.detail.active == tabArtifacts {
			keys = append(keys, widget.KeyHelp{Key: "enter", Help: "open file"})
		}
		keys = append(keys, widget.KeyHelp{Key: "tab", Help: "next tab"}, widget.KeyHelp{Key: "[ ]", Help: "round"})
		if r.pane.detail.active == tabArtifacts {
			keys = append(keys, widget.KeyHelp{Key: "↑↓", Help: "move"})
		}
		if r.actions {
			keys = append(keys, widget.KeyHelp{Key: "s", Help: "send next"}, widget.KeyHelp{Key: "r", Help: "retry on…"})
		}
		return keys
	}
	keys := []widget.KeyHelp{
		{Key: "tab", Help: "next tab"},
		{Key: "[ ]", Help: "round"},
	}
	if r.actions {
		keys = append(keys,
			widget.KeyHelp{Key: "x", Help: "stop"},
			widget.KeyHelp{Key: "g", Help: "gate"},
			widget.KeyHelp{Key: "o", Help: "shell"},
		)
	}
	return keys
}

func (r roundView) HelpKeys() []widget.KeyHelp {
	keys := []widget.KeyHelp{
		{Key: "tab", Help: "next tab"},
		{Key: "1-5", Help: "tab"},
		{Key: "[ ]", Help: "round"},
		{Key: "↑↓", Help: "scroll"},
	}
	if r.actions {
		keys = append(keys,
			widget.KeyHelp{Key: "s", Help: "send"},
			widget.KeyHelp{Key: "E", Help: "edit+send"},
			widget.KeyHelp{Key: "x", Help: "stop"},
			widget.KeyHelp{Key: "D", Help: "done"},
			widget.KeyHelp{Key: "u", Help: "unbind"},
			widget.KeyHelp{Key: "g", Help: "gate"},
			widget.KeyHelp{Key: "o", Help: "shell"},
			widget.KeyHelp{Key: "r", Help: "retry on…"},
		)
	}
	return keys
}

func (r roundView) Capturing() bool { return false }

func (r roundView) Update(msg tea.Msg, env widget.Env) (widget.View, tea.Cmd) {
	// Every call lends the pane the shell's current report and box:
	// invalidate, the ages and the fetch all read these. The pane's own rows
	// and its fetched detail row ride along, or a member the shell's report
	// no longer carries -- or the three figures the fleet row leaves nil --
	// would vanish on the first status refresh.
	r.pane.report = r.rows(env)
	r.pane.now = envNow(env)
	r.pane.width = env.Width
	r.pane.rows = widget.BodyHeight(env)
	r.pane.actions = r.actions
	switch msg := msg.(type) {
	case widget.TickMsg:
		if !r.pane.tabInFlight {
			if cmd := r.pane.visibleTabFetch(); cmd != nil {
				r.pane.tabInFlight = true
				return r, cmd
			}
		}
		return r, nil

	case statusMsg:
		var cmd tea.Cmd
		var gone bool
		r.pane, cmd, gone = r.pane.invalidate()
		if gone {
			return r, tea.Batch(pop(), notice(r.pane.detail.name+" is gone"))
		}
		return r, cmd

	case tabMsg:
		// A reply always ends the fetch it answers, stale or not; onTab then drops
		// a mismatched one.
		r.pane.tabInFlight = false
		r.pane = r.pane.onTab(msg)
		return r, nil

	case detailRowMsg:
		// The pane's report was synced above with the figures this reply
		// carries, so it is synced again here: the figures land on the same
		// paint rather than the one after it.
		r = r.onDetailRow(msg)
		r.pane.report = r.rows(env)
		return r, nil

	case tea.WindowSizeMsg:
		r.pane.width = env.Width
		r.pane.rows = widget.BodyHeight(env)
		r.pane.actions = r.actions
		r.pane.detail.vp.Width = r.pane.contentWidth()
		r.pane.detail.vp.Height = r.pane.viewportHeight()
		r.pane.fillViewport()
		return r, nil

	case tea.KeyMsg:
		return r.updateKey(msg, env)
	}
	return r, nil
}

func (r roundView) updateKey(msg tea.KeyMsg, env widget.Env) (widget.View, tea.Cmd) {
	if b := row(r.pane.report, r.pane.detail.name); b != nil {
		if cmd, ok := actionKey(env, *b, msg.String()); ok {
			return r, cmd
		}
	}
	if r.pane.reader && r.pane.detail.active == tabArtifacts {
		switch msg.String() {
		case "enter":
			return r, r.pane.openCmd(env)
		case "up", "down":
			delta := 1
			if msg.String() == "up" {
				delta = -1
			}
			var cmd tea.Cmd
			r.pane, cmd = r.pane.moveArtifact(delta)
			return r, cmd
		}
	}
	switch msg.String() {
	case "tab", "shift+tab", "back_tab":
		var cmd tea.Cmd
		r.pane, cmd = r.pane.cycleTab(msg)
		return r, cmd
	case "1", "2", "3", "4", "5":
		if r.pane.reader {
			// A reader's tabs are its own list, in its own order.
			tabs := r.pane.tabs()
			i := int(msg.String()[0] - '1')
			if i >= len(tabs) {
				return r, nil
			}
			if tabs[i] != r.pane.detail.active {
				var cmd tea.Cmd
				r.pane, cmd = r.pane.switchTab(tabs[i])
				return r, cmd
			}
			return r, nil
		}
		t := tab(msg.String()[0] - '1')
		if t != r.pane.detail.active {
			var cmd tea.Cmd
			r.pane, cmd = r.pane.switchTab(t)
			return r, cmd
		}
		return r, nil
	case "[", "]":
		delta := 1
		if msg.String() == "[" {
			delta = -1
		}
		var cmd tea.Cmd
		r.pane, cmd = r.pane.stepRound(delta)
		return r, cmd
	}
	var cmd tea.Cmd
	r.pane.detail.vp, cmd = r.pane.detail.vp.Update(msg)
	if r.pane.detail.active == tabTerminal {
		r.pane.detail.follow = r.pane.detail.vp.AtBottom()
	}
	return r, cmd
}

func (r roundView) Body(env widget.Env, width, height int) string {
	r.pane.width = width
	r.pane.rows = height
	r.pane.report = r.rows(env)
	r.pane.now = envNow(env)
	r.pane.actions = r.actions
	return r.pane.view(width)
}
