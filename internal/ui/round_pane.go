package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// roundPane is one binding's round detail: its state, its fetch
// orchestration and its rendering, lifted out of Model (§4.4, §5.1). Model
// holds exactly one and lends it the fields it cannot own -- src, ctx, now,
// report, width and rows -- through syncPane on every call.
type roundPane struct {
	src     Source
	ctx     context.Context
	now     func() time.Time
	report  view.Report
	detail  detailModel
	actions bool // Actions != nil: action keys are shown

	// tabInFlight is the pane's own fetch guard, moved from
	// Model.tabInFlight: true while a fetch is in flight for the tab on
	// screen. It is separate from statusInFlight because a terminal read
	// can block.
	tabInFlight bool

	// reader is the row's shape: true for a reader round, whose tabs are
	// prompt, artifacts, log and transcript (round 5b). artifactSel is the
	// artifacts tab's cursor, an index into the fetched list, and
	// baselineHead is the binding's RoundBaselineHead for line 1's scratch
	// cell.
	reader       bool
	artifactSel  int
	baselineHead string

	// width and rows are the pane's geometry: today's Model.paneWidth()
	// and Model.bodyRows().
	width int
	rows  int
}

// tabs is the tab bar's tabs in the order it draws them: a reader round shows
// prompt, artifacts, log and transcript; a writer round keeps today's prompt,
// report, transcript, diff and log.
func (p roundPane) tabs() []tab {
	if p.reader {
		return readerTabs
	}
	return writerTabs
}

// tabLabel is one tab's label: the artifacts tab names the file count once its
// list has been fetched ("artifacts 2"), every other tab is its own title.
func (p roundPane) tabLabel(t tab) string {
	if t == tabArtifacts {
		if n := artifactCount(p.detail.cache[tabArtifacts]); n > 0 {
			return fmt.Sprintf("%s %d", tabTitles[t], n)
		}
	}
	return tabTitles[t]
}

// headRows returns the number of furniture rows before the viewport: the
// tokens line, the tab bar and the source line. Both shapes of round draw
// the same head (round 5b).
func (p roundPane) headRows() int {
	return 6
}

// contentWidth is the width allocated for viewport content, accounting for the
// 5-space left indent (§2.5).
func (p roundPane) contentWidth() int {
	cw := p.width - 6
	if cw < 20 {
		return 20
	}
	return cw
}

// viewportHeight is the rows left for the viewport after the pane's own
// furniture, floored at 0 (§2.5).
func (p roundPane) viewportHeight() int {
	h := p.rows - p.headRows()
	if h < 0 {
		return 0
	}
	return h
}

func (p roundPane) visibleTabFetch() tea.Cmd {
	lines := p.detail.vp.Height
	if lines < 1 {
		lines = 1
	}
	t := p.detail.active
	if t == tabTerminal && p.detail.live {
		// A live terminal shows the builder's screen right now, so it
		// refetches on every visible tick regardless of cache; a hist
		// row's terminal is transcript rows already in the database --
		// static, fetched once like every other tab (not tail-following).
		return fetchFor(p.ctx, p.src, tabTerminal, p.detail.name, p.detail.round, lines, p.artifactSel, p.detail.live)
	}
	if !p.detail.cache[t].loaded {
		return fetchFor(p.ctx, p.src, t, p.detail.name, p.detail.round, lines, p.artifactSel, p.detail.live)
	}
	return nil
}

// artifactsFetch is the reader round's artifact-list fetch, issued alongside
// the visible tab's so the tab's label and the card know the count and the
// total size before the human opens the tab. A writer round has none, and the
// visible fetch already covers the artifacts tab when it is the one on
// screen.
func (p roundPane) artifactsFetch() tea.Cmd {
	if !p.reader || p.detail.active == tabArtifacts || p.detail.cache[tabArtifacts].loaded {
		return nil
	}
	return fetchFor(p.ctx, p.src, tabArtifacts, p.detail.name, p.detail.round, 1, p.artifactSel, p.detail.live)
}

// reportFetch reads the round's report tab, whatever tab is on screen. Opening
// a binding whose report is ready lands on that tab, so it needs its read even
// when another tab's fetch is in flight; the two replies are told apart by
// their tab, so issuing it beside startFetch's own fetch is safe.
func (p roundPane) reportFetch() tea.Cmd {
	return fetchFor(p.ctx, p.src, tabReport, p.detail.name, p.detail.round, 1, p.artifactSel, p.detail.live)
}

// startFetch issues whatever the pane must read now: the visible tab's fetch,
// and a reader round's artifact list, in one command. It is a no-op while a
// fetch is in flight, and marks the pane in flight when it returns one.
func (p *roundPane) startFetch() tea.Cmd {
	if p.tabInFlight {
		return nil
	}
	cmd := p.visibleTabFetch()
	extra := p.artifactsFetch()
	if cmd == nil && extra == nil {
		return nil
	}
	p.tabInFlight = true
	switch {
	case cmd == nil:
		return extra
	case extra == nil:
		return cmd
	default:
		return tea.Batch(cmd, extra)
	}
}

// pointDetailAt re-targets the pane at the row keyed: key, round
// (paneRound), lastLogTS from row.Last, every cache cleared, every
// parked scroll zeroed. The active tab is kept -- a human reading diffs
// across bindings stays on diff. It issues the visible-tab fetch only if
// tabInFlight is clear; a fetch already in flight for the previous
// binding is discarded on arrival by tabMsg's name check, which exists
// for exactly this. A no-op when the pane already shows key.
func (p roundPane) pointDetailAt(key string) (roundPane, tea.Cmd) {
	if p.detail.name == key {
		return p, nil
	}
	r := row(p.report, key)
	if r == nil {
		return p, nil
	}
	// #143: opening a binding's detail pane is what "viewed" means; the
	// stamp is best-effort (each Source swallows its own errors) and must
	// never block re-targeting the pane.
	p.src.MarkViewed(key)
	vp := viewport.New(p.contentWidth(), p.viewportHeight())
	// The row's shape, a chain row's member round and the round's baseline
	// head live on the binding, not the status document: a reader round's tabs,
	// card and context row are keyed on them (round 5b). A key the runtime
	// cannot resolve, or a read that fails, leaves the pane a writer's.
	//
	// A live chain's row stands in for its members, and its builder member
	// carries the chain's own name, so the row opens that member's current
	// round rather than the row's absent plan. A chain row whose member cannot
	// be loaded leaves round 0 -- never the -1 its zero plan would otherwise
	// give.
	round, rounds := paneRound(*r), roundsOf(*r)
	if r.Chain != nil {
		round, rounds = 0, 0
	}
	p.reader, p.baselineHead = false, ""
	if rt, name, ok := p.src.Runtime(key); ok && rt.Store != nil {
		if b, err := rt.Store.Load(name); err == nil {
			if r.Chain != nil {
				round, rounds = b.Round, b.Round
			}
			p.reader = b.Shape == store.ShapeReader
			p.baselineHead = b.RoundBaselineHead
		}
	}
	p.detail = detailModel{
		name:     key,
		round:    round,
		rounds:   rounds,
		live:     true,
		active:   p.detail.active,
		vp:       vp,
		headless: r.Headless != nil,
		follow:   true,
	}
	p.artifactSel = 0
	if r.Last != nil {
		p.detail.lastLogTS = r.Last.TS
	}
	p.fillViewport()
	if cmd := p.startFetch(); cmd != nil {
		return p, cmd
	}
	return p, nil
}

// pointDetailAtHist re-targets the pane at h, a database row not in the
// live report (#172, §5.8): live false, round the newest -- every one of
// h's rounds is closed, unlike a live row's round-1 rule -- rounds h.Rounds,
// archivedAt from h, follow false (a hist row's terminal is transcript
// rows, never tailed). Every cache cleared, the active tab kept, exactly
// like pointDetailAt. A no-op when the pane already shows h.Name.
func (p roundPane) pointDetailAtHist(h relevo.HistoryBinding) (roundPane, tea.Cmd) {
	if p.detail.name == h.Name {
		return p, nil
	}
	vp := viewport.New(p.contentWidth(), p.viewportHeight())
	p.detail = detailModel{
		name:       h.Name,
		bindingID:  h.ID,
		live:       false,
		round:      h.Rounds,
		rounds:     h.Rounds,
		archivedAt: h.ArchivedAt,
		active:     p.detail.active,
		vp:         vp,
		follow:     false,
	}
	p.reader = false
	p.baselineHead = ""
	p.artifactSel = 0
	p.fillViewport()
	if cmd := p.startFetch(); cmd != nil {
		return p, cmd
	}
	return p, nil
}

// fillViewport sets the viewport to the active tab's body, wrapped to the
// viewport's width, keeping the current offset (the viewport clamps it).
// Every SetContent goes through here so a resize re-wraps.
func (p *roundPane) fillViewport() {
	y := p.detail.vp.YOffset
	if p.detail.name == "" {
		// Nothing is pointed at, so nothing is loading: an empty fleet's
		// pane stays blank rather than promising content.
		p.detail.vp.SetContent("")
		return
	}
	c := p.detail.cache[p.detail.active]
	w := p.detail.vp.Width
	if w <= 0 {
		w = p.contentWidth()
	}
	p.detail.vp.SetContent(wrapBody(bodyOf(p.detail.active, c, p.detail.headless), w))
	p.detail.vp.SetYOffset(y)
}

// invalidate re-reads the row: when the newest log timestamp moved it drops
// the caches, re-points the round and re-fetches the active tab. gone is
// true when the row is no longer in the report; the pane is then left
// untouched and Model handles the screen, the notice and the re-point. This
// is the part of Model.maybeInvalidate after its first guard; Model keeps
// that guard and the gone branch.
func (p roundPane) invalidate() (roundPane, tea.Cmd, bool) {
	r := row(p.report, p.detail.name)
	if r == nil {
		return p, nil, true
	}
	if r.Last == nil {
		return p, nil, false
	}
	if r.Last.TS.Equal(p.detail.lastLogTS) {
		return p, nil, false
	}

	p.detail.lastLogTS = r.Last.TS
	p.detail.round = paneRound(*r)
	p.detail.rounds = roundsOf(*r)
	for _, t := range p.tabs() {
		if t == tabTerminal {
			// A terminal refetches on every visible tick.
			continue
		}
		p.detail.cache[t] = tabContent{} // loaded=false
		p.detail.scroll[t] = 0           // reset parked offset on invalidation
	}
	if cmd := p.startFetch(); cmd != nil {
		return p, cmd, false
	}
	return p, nil, false
}

// stepRound moves detail.round by delta, clamped to [1, detail.rounds] --
// "[" and "]" step a binding's rounds, live and archived alike (#183). At
// either edge it is a no-op with no notice. Every tab's cache is
// invalidated (a round-keyed fetch is meaningless against the old round's
// reply) and the active tab is re-fetched.
func (p roundPane) stepRound(delta int) (roundPane, tea.Cmd) {
	next := p.detail.round + delta
	if next < 1 || next > p.detail.rounds {
		return p, nil
	}
	p.detail.round = next
	p.artifactSel = 0
	for t := tab(0); t < tabCount; t++ {
		p.detail.cache[t] = tabContent{} // loaded=false
		p.detail.scroll[t] = 0           // reset parked offset on invalidation
	}
	p.fillViewport()
	if cmd := p.startFetch(); cmd != nil {
		return p, cmd
	}
	return p, nil
}

// onTab is the tabMsg arm's work: a reply for a name, round or tab the pane
// is no longer showing is stale and is dropped. Model keeps the
// paneVisible() early return.
func (p roundPane) onTab(msg tabMsg) roundPane {
	if msg.name != p.detail.name {
		return p
	}
	// Every tab is round-keyed now (#183): prompt, report, terminal and
	// log all read the specific round fetchFor was called with, the
	// same way diff always has. A reply for a round that is no longer
	// the one on screen -- a slow fetch outlived by two presses of "]"
	// -- is stale and must never land in the cache.
	if msg.round != p.detail.round {
		return p
	}
	// A reader round's artifact reply carries the list in order; keep the
	// cursor on the file it names, so a refetch never moves it.
	if msg.t == tabArtifacts {
		for i, f := range msg.content.artifacts {
			if f.Rel == msg.content.artifactRel {
				p.artifactSel = i
				break
			}
		}
	}
	if msg.t != p.detail.active {
		p.detail.cache[msg.t] = msg.content
		return p
	}
	p.detail.cache[msg.t] = msg.content
	p.fillViewport()
	if msg.t == tabTerminal && p.detail.follow {
		p.detail.vp.GotoBottom()
	}
	return p
}

// cycleTab is tab / shift+tab, over the shape's own tabs.
func (p roundPane) cycleTab(msg tea.KeyMsg) (roundPane, tea.Cmd) {
	tabs := p.tabs()
	i := 0
	for j, t := range tabs {
		if t == p.detail.active {
			i = j
			break
		}
	}
	if msg.Type == tea.KeyShiftTab || msg.String() == "shift+tab" || msg.String() == "back_tab" {
		return p.switchTab(tabs[(i-1+len(tabs))%len(tabs)])
	}
	return p.switchTab(tabs[(i+1)%len(tabs)])
}

func (p roundPane) switchTab(next tab) (roundPane, tea.Cmd) {
	p.detail.scroll[p.detail.active] = p.detail.vp.YOffset // park
	p.detail.active = next
	c := p.detail.cache[next]
	p.fillViewport()
	p.detail.vp.SetYOffset(p.detail.scroll[next]) // restore
	if next == tabTerminal && p.detail.follow {
		p.detail.vp.GotoBottom()
	}
	if !c.loaded && !p.tabInFlight {
		p.tabInFlight = true
		lines := p.detail.vp.Height
		if lines < 1 {
			lines = 1
		}
		return p, fetchFor(p.ctx, p.src, next, p.detail.name,
			p.detail.round, lines, p.artifactSel, p.detail.live)
	}
	return p, nil
}

// detailHeader is the detail pane's identity line (#183): name and round N
// of M, plus "archived <date>" for a hist row the database recorded as
// archived, plus "live" when the round on screen is a live binding's own
// open (not yet closed) round.
func (p roundPane) detailHeader() string {
	s := fmt.Sprintf("%s · round %d of %d", p.detail.name, p.detail.round, p.detail.rounds)
	if !p.detail.archivedAt.IsZero() {
		s += " · archived " + p.detail.archivedAt.Format("2006-01-02")
	} else {
		b := row(p.report, p.detail.name)
		if b != nil && p.detail.live && p.detail.round == p.detail.rounds && b.RoundEnd.IsZero() {
			s += " · live"
		}
	}
	return s
}

// sourceLine says, in one faint line, what the viewport is showing.
func (p roundPane) sourceLine() string {
	if p.detail.name == "" {
		return ""
	}
	c := p.detail.cache[p.detail.active]
	if !c.loaded {
		return faintStyle.Render("loading…")
	}
	if p.detail.active == tabArtifacts {
		// The header carries the count and the total size (round 5b); a
		// source line here would only repeat them.
		return ""
	}
	var s string
	switch p.detail.active {
	case tabPrompt:
		if c.at.IsZero() {
			s = fmt.Sprintf("prompt r%d", c.round)
		} else {
			s = fmt.Sprintf("prompt r%d · %s", c.round, c.at.Local().Format("15:04"))
		}
	case tabReport:
		if c.at.IsZero() {
			s = fmt.Sprintf("report r%d", c.round)
		} else {
			s = fmt.Sprintf("report r%d · %s", c.round, c.at.Local().Format("15:04"))
		}
	case tabTerminal:
		n := strings.Count(strings.TrimRight(c.body, "\n"), "\n") + 1
		r := row(p.report, p.detail.name)
		if c.transcript {
			src := "pane"
			if p.detail.headless {
				src = "headless"
				if r != nil && r.Server != "" {
					src = "remote"
				}
			}
			if c.logName != "" {
				src += " · " + c.logName
			}
			mode := "following"
			if !p.detail.follow {
				mode = "scrolled"
			}
			s = fmt.Sprintf("%s · %d lines · %s", src, n, mode)
		} else {
			pane := ""
			if r != nil {
				pane = "remote"
				if r.Headless != nil {
					pane = r.ProcessWord()
				}
			}
			s = fmt.Sprintf("%s · captured %s ago · %d lines", pane, ago(c.at, p.now()), n)
		}
	case tabDiff:
		files, add, del := diffStat(c.body)
		unit := "files"
		if files == 1 {
			unit = "file"
		}
		s = fmt.Sprintf("round %d · %d %s · +%d −%d", p.detail.round, files, unit, add, del)
	case tabLog:
		n := strings.Count(strings.TrimRight(c.body, "\n"), "\n") + 1
		s = fmt.Sprintf("%d entries", n)
	}
	if c.err != nil || c.empty != "" {
		// The viewport carries the prose; the source line says only where
		// it looked.
		switch p.detail.active {
		case tabPrompt:
			s = fmt.Sprintf("round %d", p.detail.round)
		case tabReport:
			s = "report"
		case tabDiff:
			s = fmt.Sprintf("round %d", p.detail.round)
		case tabLog:
			s = "log"
		}
	}
	return faintStyle.Render(s)
}

// hintLine is the one rendered line under a blocked builder's dialog on
// the terminal tab: the verb that resolves it (spec §3.4). The ui runs
// nothing; it names the command.
func (p roundPane) hintLine(b *view.BindingStatus) (string, bool) {
	if b == nil || b.Waiting == nil || b.Waiting.Cause != "blocked" || p.detail.active != tabTerminal {
		return "", false
	}
	return accentStyle.Render("relevo: ") + fgStyle.Render(b.Waiting.Hint), true
}

// view draws exactly rows rows at width (§2.5, §5).
func (p roundPane) view(width int) string {
	b := row(p.report, p.detail.name)
	out := []string{p.tokensLine(b), "", p.tabsRow(), ""}
	// A reader round's artifacts tab draws no source line: the header
	// already carries the count and the size (round 5b).
	if s := p.sourceLine(); s != "" {
		out = append(out, "     "+s, "")
	}

	budget := p.rows - len(out)
	if budget < 0 {
		budget = 0
	}
	hint, hasHint := p.hintLine(b)
	vpRows := budget
	if hasHint && vpRows > 0 {
		vpRows--
	}
	vp := p.detail.vp
	vp.Width = p.contentWidth()
	vp.Height = vpRows
	if vpRows > 0 {
		vpLines := strings.Split(vp.View(), "\n")
		bar := barCells(vp.TotalLineCount(), vp.Height, vp.YOffset)
		for i, l := range vpLines {
			cell := ""
			if i < len(bar) {
				cell = bar[i]
			}
			out = append(out, "     "+l+cell)
		}
	}
	if hasHint && budget > 0 {
		out = append(out, hint)
	}
	for len(out) < p.rows {
		out = append(out, "")
	}
	out = out[:p.rows]
	for i := range out {
		out[i] = fit(out[i], width)
	}
	return strings.Join(out, "\n")
}
