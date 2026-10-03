package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/view"
)

// envNow is the clock a view hands the roundPane: the shell reads time once
// per call (Env.Now).
func envNow(env Env) func() time.Time { return func() time.Time { return env.Now } }

// newRoundPane builds a pane lent everything Env carries (§4.4, R2.4).
func newRoundPane(env Env) roundPane {
	return roundPane{
		src:     env.Src,
		ctx:     env.Ctx,
		now:     envNow(env),
		report:  env.Report,
		width:   env.Width,
		rows:    bodyHeight(env),
		actions: env.Actions != nil,
	}
}

// newRoundView targets a live binding's round. round == 0 means the
// default: pointDetailAt's rule (paneRound). A non-zero round is set
// after the point, with the caches cleared like stepRound does, and its
// fetch is issued (§4.5, R2.4).
func newRoundView(env Env, key string, round int) (View, tea.Cmd) {
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

// newChainRoundView targets a member's round from a chain's step list, where
// the member's row is not in the shell's report: it opens the round against a
// report carrying the chain's own member rows, and carries them as the view's
// extra so every later status refresh keeps the pane's target resolvable.
func newChainRoundView(env Env, key string, round int, members []view.BindingStatus, crumbs []string) (View, tea.Cmd) {
	env.Report = mergeRows(env.Report, members)
	v, cmd := newRoundView(env, key, round)
	if rv, ok := v.(roundView); ok {
		rv.extra = members
		rv.crumbs = crumbs
		v = rv
	}
	return v, cmd
}

// newHistRoundView targets an archived binding's round through
// pointDetailAtHist. round == 0 means the default (Rounds) (§4.5, R2.4).
func newHistRoundView(env Env, h relevo.HistoryBinding, round int) (View, tea.Cmd) {
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

// row finds a report row by key (BindingStatus.Key()): a mastermind row keys
// by Name, a server row by owner/name, so two clients' same-named bindings
// never collide. Moved from model.go (R2.8).
func row(rep view.Report, key string) *view.BindingStatus {
	for i := range rep.Bindings {
		if rep.Bindings[i].Key() == key {
			return &rep.Bindings[i]
		}
	}
	return nil
}

// paneRound returns the round the pane opens on (#428). While a round is in
// flight, r.Round is the running round, so Round - 1 alone wrongly targets the
// previous (finished) round instead of the live tail. It returns r.PlanRound
// when > 0, else r.Round - 1 (a binding with no plan yet, or a row from an
// older relevo serve whose JSON lacks plan_round).
func paneRound(r view.BindingStatus) int {
	if r.PlanRound > 0 {
		return r.PlanRound
	}
	return r.Round - 1
}

// roundsOf returns the count of sent rounds (§2.2): r.PlanRound when it is > 0,
// else r.Round.
func roundsOf(r view.BindingStatus) int {
	if r.PlanRound > 0 {
		return r.PlanRound
	}
	return r.Round
}

// roundView is the full-screen round detail: today's pane, hosted as a
// view (§4.5).
type roundView struct {
	pane    roundPane
	actions bool // Actions != nil at construction: the action keys are shown (r1)
	// extra carries report rows the shell's report no longer holds: a chain's
	// own members are folded into its synthetic row by the cockpit's read
	// model, so a round drilled in from the chains view lends them back for
	// as long as the view is on screen.
	extra []view.BindingStatus
	// crumbs replaces the default binding-and-round breadcrumb when the round
	// was reached through a chain, so the trail reads chains › chain › step
	// rather than naming the member twice.
	crumbs []string
}

// mergeRows is rep with extra appended, one loop, dropping any extra row the
// report already carries under the same key: the shell's own row wins, and a
// member spliced in twice is one row.
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

// Context is the context row. It reads the same report the pane does, with
// the view's own rows merged in, so a chain member's round names its binding
// after a status refresh as it did when it was opened.
func (r roundView) Context(env Env) (string, string) {
	b := row(mergeRows(env.Report, r.extra), r.pane.detail.name)

	rightText := fmt.Sprintf("round %d of %d", r.pane.detail.round, r.pane.detail.rounds)
	if !r.pane.detail.archivedAt.IsZero() {
		rightText += " · archived " + r.pane.detail.archivedAt.Format("2006-01-02")
	} else if b != nil && r.pane.detail.live && r.pane.detail.round == r.pane.detail.rounds && b.RoundEnd.IsZero() {
		rightText += " · live"
	}
	right := faintStyle.Render(rightText) + " "

	if b == nil || !r.pane.detail.live {
		left := "   " + mutedStyle.Render(r.pane.detailHeader())
		if lipgloss.Width(left)+1+lipgloss.Width(right) > env.Width {
			right = ""
		}
		return left, right
	}

	left, right := contextLine(roundStateCell(*b, env.Now), r.pane.contextCells(*b), right, env.Width)
	return left, right
}

// contextLine joins line 1 from the state-and-time cell, the round's own
// cells and the round-of-n cell on the right. The state-and-time cell never
// drops; the cells after it drop whole, from the right, while the line alone
// is still too wide -- the writers' own rule, kept for readers (round 5b).
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

// roundStateCell is line 1's first cell: the state chip and the age, drawn
// the same way for a writer and a reader with the same state.
// TestReaderHeaderSharesTheWriterLayout pins that sharing.
func roundStateCell(b view.BindingStatus, now time.Time) string {
	pill, age := roundState(b, now)
	return pill + age
}

// roundState is the state chip and the age behind roundStateCell: the words
// and styles the fleet's group draws, then the age the row's own rule gives.
func roundState(b view.BindingStatus, now time.Time) (pill, age string) {
	g := groupOf(b)
	var pStyle lipgloss.Style
	var pWord string
	switch g {
	case groupNeedsYou:
		pStyle, pWord = chipWarnStyle.Bold(true), "needs you"
	case groupWorking:
		pStyle, pWord = chipGreenStyle.Bold(true), "working"
	case groupIdle:
		pStyle, pWord = kbdStyle, "idle"
	case groupHeld:
		pStyle, pWord = kbdStyle, "on hold"
	case groupDone:
		pStyle, pWord = kbdStyle, "done"
	default:
		pStyle, pWord = kbdStyle, strings.ToLower(b.Display)
	}

	var ageStr string
	if g == groupWorking {
		if a := ago(b.RoundStart, now); a != "" {
			ageStr = textStyle.Bold(true).Render(a)
			if b.QuietFor != "" {
				ageStr += mutedStyle.Render(" · quiet " + b.QuietFor)
			}
		} else if b.QuietFor != "" {
			ageStr = mutedStyle.Render("quiet " + b.QuietFor)
		}
	} else {
		rn := rowNow(b, now)
		if b.Round > 0 {
			rn = strings.TrimPrefix(rn, fmt.Sprintf("r%d · ", b.Round))
		}
		ageStr = textStyle.Render(rn)
	}
	return "   " + chip(pStyle, pWord), "   " + ageStr
}

// contextCells is what line 1 draws after its state-and-time cell. A reader
// round shows its artifacts count and total size, "repository unchanged" once
// the round has closed, and the actor, its shape, its mastermind and its scratch
// worktree; a writer shows its candidate, its mastermind, its branch and a dirty
// tree. The cells drop from the right, so a reader loses its scratch worktree
// first, then its mastermind, its shape and its actor (round 5b).
func (p roundPane) contextCells(b view.BindingStatus) []string {
	if p.reader {
		return p.readerContextCells(b)
	}
	cells := []string{"      " + textStyle.Render(candidateText(b))}
	cells = append(cells, faintStyle.Render("  ·  ")+mutedStyle.Render(mastermindWordOf(b)))
	branch := b.Branch
	if branch == "" {
		branch = repoCell(b)
	}
	if branch != "" {
		cells = append(cells, faintStyle.Render("  ·  ")+mutedStyle.Render(branch))
	}
	if b.Dirty {
		cells = append(cells, faintStyle.Render("  ·  ")+redStyle.Render("dirty"))
	}
	return cells
}

// readerContextCells is a reader round's cells after the state-and-time: the
// artifact facts, the closed-round note, then the actor, its shape, its
// mastermind and its scratch worktree.
func (p roundPane) readerContextCells(b view.BindingStatus) []string {
	var cells []string
	c := p.detail.cache[tabArtifacts]
	if artifactCount(c) > 0 {
		cells = append(cells,
			faintStyle.Render("  ·  ")+mutedStyle.Render(artifactsWord(artifactCount(c))),
			faintStyle.Render("  ·  ")+mutedStyle.Render(relevo.ArtifactSizeText(artifactTotalSize(c))))
	}
	if !p.roundOpen(b) {
		cells = append(cells, faintStyle.Render("  ·  ")+mutedStyle.Render("repository unchanged"))
	}
	cells = append(cells,
		"      "+textStyle.Render(actorCell(b))+faintStyle.Render(" on ")+textStyle.Render(candidateText(b)),
		faintStyle.Render("  ·  ")+mutedStyle.Render("reader"),
		faintStyle.Render("  ·  ")+mutedStyle.Render(mastermindWordOf(b)),
		faintStyle.Render("  ·  ")+mutedStyle.Render(scratchText(p.baselineHead)))
	return cells
}

// mastermindWordOf names the mastermind as the context row does: the client label
// when the binding has one, the mastermind cell otherwise.
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

// scratchText names a reader round's throwaway worktree: the head its
// baseline was cut from (the binding's RoundBaselineHead, shortened) and the
// uncommitted diff it carried. A round with no recorded head names the
// worktree alone.
func scratchText(head string) string {
	if head == "" {
		return "scratch + dirty diff"
	}
	if len(head) > 7 {
		head = head[:7]
	}
	return "scratch @ " + head + " + dirty diff"
}

func (r roundView) Keys() []KeyHelp {
	// A reader round's footer is its own (round 5b): no x stop, no g gate,
	// no o shell; enter opens the artifacts tab's selected file, and send
	// next and retry on are the reader's actions.
	if r.pane.reader {
		var keys []KeyHelp
		if r.pane.detail.active == tabArtifacts {
			keys = append(keys, KeyHelp{"enter", "open file"})
		}
		keys = append(keys, KeyHelp{"tab", "next tab"}, KeyHelp{"[ ]", "round"})
		if r.pane.detail.active == tabArtifacts {
			keys = append(keys, KeyHelp{"↑↓", "move"})
		}
		if r.actions {
			keys = append(keys, KeyHelp{"s", "send next"}, KeyHelp{"r", "retry on…"})
		}
		return keys
	}
	keys := []KeyHelp{
		{"tab", "next tab"},
		{"[ ]", "round"},
	}
	if r.actions {
		keys = append(keys,
			KeyHelp{"x", "stop"},
			KeyHelp{"g", "gate"},
			KeyHelp{"o", "shell"},
		)
	}
	return keys
}

func (r roundView) HelpKeys() []KeyHelp {
	keys := []KeyHelp{
		{"tab", "next tab"},
		{"1-5", "tab"},
		{"[ ]", "round"},
		{"↑↓", "scroll"},
	}
	if r.actions {
		keys = append(keys,
			KeyHelp{"s", "send"},
			KeyHelp{"E", "edit+send"},
			KeyHelp{"x", "stop"},
			KeyHelp{"D", "done"},
			KeyHelp{"u", "unbind"},
			KeyHelp{"g", "gate"},
			KeyHelp{"o", "shell"},
			KeyHelp{"r", "retry on…"},
		)
	}
	return keys
}

func (r roundView) Capturing() bool { return false }

func (r roundView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	// Every call lends the pane the shell's current report and box (§4.4):
	// invalidate, the ages and the fetch all read these. The pane's own rows
	// ride along, or a member the shell's report no longer carries would
	// vanish on the first status refresh.
	r.pane.report = mergeRows(env.Report, r.extra)
	r.pane.now = envNow(env)
	r.pane.width = env.Width
	r.pane.rows = bodyHeight(env)
	r.pane.actions = r.actions
	switch msg := msg.(type) {
	case tickMsg:
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
		// A reply always ends the fetch it answers, stale or not (today's
		// tabMsg arm); onTab then drops a mismatched one.
		r.pane.tabInFlight = false
		r.pane = r.pane.onTab(msg)
		return r, nil

	case tea.WindowSizeMsg:
		r.pane.width = env.Width
		r.pane.rows = bodyHeight(env)
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

func (r roundView) updateKey(msg tea.KeyMsg, env Env) (View, tea.Cmd) {
	if b := row(r.pane.report, r.pane.detail.name); b != nil {
		if cmd, ok := actionKey(env, *b, msg.String()); ok {
			return r, cmd
		}
	}
	// The artifacts tab's own keys (round 5b): enter opens the selected
	// file, the arrows move the cursor between files.
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

// Body is the pane, given the shell's full body box (§5.4).
func (r roundView) Body(env Env, width, height int) string {
	r.pane.width = width
	r.pane.rows = height
	r.pane.report = mergeRows(env.Report, r.extra)
	r.pane.now = envNow(env)
	r.pane.actions = r.actions
	return r.pane.view(width)
}
