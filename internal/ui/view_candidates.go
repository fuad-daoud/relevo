package ui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// candDocMsg is one ConfigDoc load's reply (§4).
type candDocMsg struct {
	doc relevo.ConfigDoc
	err error
}

// candidatesView is ':candidates' (§4): the configured candidates in pick
// order, the gates on them, the actors that serve them, the read-only detail
// block for the cursor row, and the add/edit candidate form.
type candidatesView struct {
	doc     relevo.ConfigDoc
	err     error // the last ConfigDoc error; shown centred like stats' error state
	loaded  bool
	cur     int // cursor over rows()
	top     int // first body line shown (page follows the cursor)
	actions bool
}

// candRow is one table row, computed per render from doc and env.Report.Gated,
// never stored (§3).
type candRow struct {
	c     candidate.Candidate
	slots []relevo.ActorSlot // CandidateSlots(doc, c.Name), on entries only, for SERVES
	on    bool               // some actor has it on
	gate  *availability.Gate // the gate on its token from env.Report.Gated, nil when none
}

// newCandidatesView builds ':candidates' and the command that loads its doc
// (§4). env.Actions must be non-nil: execLine refuses the command otherwise.
func newCandidatesView(env Env) (View, tea.Cmd) {
	v := candidatesView{actions: env.Actions != nil}
	return v, candDocCmd(env)
}

// candDocCmd reads the stored config off the update loop.
func candDocCmd(env Env) tea.Cmd {
	return func() tea.Msg {
		doc, err := env.Actions.ConfigDoc()
		return candDocMsg{doc: doc, err: err}
	}
}

// actorOrder walks the actors the pick order uses (§4): builder, reviewer,
// researcher, then any other actor by name.
func actorOrder(acts map[string]roles.Actor) []string {
	var out []string
	for _, name := range []string{"builder", "reviewer", "researcher"} {
		if _, ok := acts[name]; ok {
			out = append(out, name)
		}
	}
	rest := make([]string, 0, len(acts))
	for name := range acts {
		switch name {
		case "builder", "reviewer", "researcher":
			continue
		}
		rest = append(rest, name)
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// candName resolves one actor entry's reference to the candidate's short name,
// by name or by canonical token. "" when no candidate matches.
func candName(doc relevo.ConfigDoc, ref string) string {
	for _, c := range doc.Candidates {
		if c.Name == ref || c.Ref().String() == ref {
			return c.Name
		}
	}
	return ""
}

// candRows is the table in pick order (§4): walk the actors in actorOrder,
// each actor's entries in order, emitting each candidate the first time it is
// seen (including off entries); then every remaining candidate in stored
// order.
func candRows(doc relevo.ConfigDoc, gated []availability.Gate) []candRow {
	byName := make(map[string]candidate.Candidate, len(doc.Candidates))
	for _, c := range doc.Candidates {
		byName[c.Name] = c
	}
	seen := make(map[string]bool, len(doc.Candidates))
	var out []candRow
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		c, ok := byName[name]
		if !ok {
			return
		}
		seen[name] = true
		out = append(out, candRowWith(doc, c, gated))
	}
	for _, actor := range actorOrder(doc.Actors) {
		for _, e := range doc.Actors[actor].Candidates {
			add(candName(doc, e.Candidate))
		}
	}
	for _, c := range doc.Candidates {
		add(c.Name)
	}
	return out
}

// candRowWith computes one row's slots, on flag and gate.
func candRowWith(doc relevo.ConfigDoc, c candidate.Candidate, gated []availability.Gate) candRow {
	r := candRow{c: c}
	for _, s := range relevo.CandidateSlots(doc, c.Name) {
		if s.Off {
			continue
		}
		r.slots = append(r.slots, s)
		r.on = true
	}
	token := c.Ref().String()
	for i := range gated {
		if gated[i].Token == token {
			g := gated[i]
			r.gate = &g
			break
		}
	}
	return r
}

// servesText is the SERVES cell (§4): "<actor> <position>" for each slot where
// the candidate is on, joined " · ", in actor-name order.
func servesText(r candRow) string {
	parts := make([]string, 0, len(r.slots))
	for _, s := range r.slots {
		parts = append(parts, fmt.Sprintf("%s %d", s.Actor, s.Position))
	}
	return strings.Join(parts, " · ")
}

// candStatus is the STATUS cell and its colour (§4): off in faint when no
// actor has it on, else the gate's time left in red, else ready in green.
func candStatus(r candRow, now time.Time) (string, lipgloss.Style) {
	switch {
	case !r.on:
		return "off", faintStyle
	case r.gate != nil:
		return statsGateLeft(*r.gate, now), redStyle
	}
	return "ready", greenStyle
}

// candCol is one fixed-width table column after CANDIDATE.
type candCol struct {
	key  string
	head string
	w    int
}

// candLayout is the columns to show at width and the CANDIDATE column's room
// (§4): the table spans width-6 cells from column 3, each column followed by
// two spaces, CANDIDATE taking the rest. While that would fall under 16 cells,
// columns leave in the order MODEL, SERVES, HARNESS.
func candLayout(width int) (int, []candCol) {
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	visible := []candCol{
		{"harness", "HARNESS", 8},
		{"provider", "PROVIDER", 10},
		{"model", "MODEL", 35},
		{"serves", "SERVES", 22},
		{"status", "STATUS", 9},
	}
	nameW := func(cols []candCol) int {
		fixed := 2 // the two spaces after CANDIDATE
		for i, c := range cols {
			fixed += c.w
			if i < len(cols)-1 {
				fixed += 2
			}
		}
		return cw - fixed
	}
	for _, drop := range []string{"model", "serves", "harness"} {
		if nameW(visible) >= 16 {
			break
		}
		for i, c := range visible {
			if c.key == drop {
				visible = append(append([]candCol(nil), visible[:i]...), visible[i+1:]...)
				break
			}
		}
	}
	return nameW(visible), visible
}

// candCell is one table cell: its already-padded text and the style it draws
// in. A cursor row repaints every cell on the selection band.
type candCell struct {
	text  string
	style lipgloss.Style
}

// candLine renders one table row (§4): cells separated by two spaces, a
// non-cursor row fitted to cw, a cursor row carried on a full-width selection
// band, as view_log builds its selected row.
func candLine(cells []candCell, sel bool, cw int) string {
	var b strings.Builder
	for i, c := range cells {
		if i > 0 {
			if sel {
				b.WriteString(selBandStyle.Render("  "))
			} else {
				b.WriteString("  ")
			}
		}
		st := c.style
		if sel {
			st = st.Background(selBandStyle.GetBackground())
		}
		b.WriteString(st.Render(c.text))
	}
	row := b.String()
	if !sel {
		return "   " + fit(row, cw) + "   "
	}
	if lipgloss.Width(row) > cw {
		row = lipgloss.NewStyle().MaxWidth(cw).Render(row)
	}
	if w := lipgloss.Width(row); w < cw {
		row += selBandStyle.Render(strings.Repeat(" ", cw-w))
	}
	return "   " + row + "   "
}

// candHeaderLine is the table's header row, in faint bold.
func candHeaderLine(nameW int, cols []candCol, cw int) string {
	style := faintStyle.Bold(true)
	cells := []candCell{{text: pad("CANDIDATE", nameW), style: style}}
	for _, c := range cols {
		cells = append(cells, candCell{text: pad(c.head, c.w), style: style})
	}
	return candLine(cells, false, cw)
}

// candDataLine is one candidate's table row (§4). An off row is faint
// throughout; the cursor row's name is bold.
func candDataLine(r candRow, sel bool, nameW int, cols []candCol, cw int, now time.Time) string {
	base, meta := textStyle, mutedStyle
	if !r.on {
		base, meta = faintStyle, faintStyle
	}
	nameStyle := base
	if sel {
		nameStyle = nameStyle.Bold(true)
	}
	cells := []candCell{{text: pad(r.c.Name, nameW), style: nameStyle}}
	for _, col := range cols {
		var text string
		style := meta
		switch col.key {
		case "harness":
			text = r.c.Harness
		case "provider":
			text = r.c.Provider
		case "model":
			text = r.c.Model
		case "serves":
			text = servesText(r)
		case "status":
			text, style = candStatus(r, now)
		}
		cells = append(cells, candCell{text: pad(text, col.w), style: style})
	}
	return candLine(cells, sel, cw)
}

// candSince is the gate's Since in local time (§4): the time of day when it is
// today, the date and time otherwise.
func candSince(t, now time.Time) string {
	l, n := t.Local(), now.Local()
	if l.Year() == n.Year() && l.Month() == n.Month() && l.Day() == n.Day() {
		return l.Format("15:04")
	}
	return l.Format("Jan 2 15:04")
}

// candUntilText is a gate's expiry text (§4): "until cleared" for a gate with
// no expiry, else "until " and statsGateUntil's time.
func candUntilText(g availability.Gate, now time.Time) string {
	if g.Until.IsZero() {
		return "until cleared"
	}
	return "until " + statsGateUntil(g, now)
}

// candOrdinal is a slot's ordinal (§4): first … tenth, then #11.
func candOrdinal(n int) string {
	switch n {
	case 1:
		return "first"
	case 2:
		return "second"
	case 3:
		return "third"
	case 4:
		return "fourth"
	case 5:
		return "fifth"
	case 6:
		return "sixth"
	case 7:
		return "seventh"
	case 8:
		return "eighth"
	case 9:
		return "ninth"
	case 10:
		return "tenth"
	}
	return fmt.Sprintf("#%d", n)
}

// candPickSentence is the detail block's pick line (§4): the slots that pick
// it, and, when it is gated, who the first slot's actor takes instead. Muted,
// with actor and candidate names in text.
func candPickSentence(doc relevo.ConfigDoc, env Env, r candRow) string {
	if !r.on {
		return mutedStyle.Render("off: no actor has it on")
	}
	parts := make([]string, 0, len(r.slots))
	for _, s := range r.slots {
		parts = append(parts, mutedStyle.Render("the ")+textStyle.Render(s.Actor)+
			mutedStyle.Render("'s "+candOrdinal(s.Position)+" pick"))
	}
	out := strings.Join(parts, mutedStyle.Render(" and "))
	if r.gate == nil || len(r.slots) == 0 {
		return out
	}
	actor := r.slots[0].Actor
	if next, ok := nextReady(doc, env, actor, r.c.Name); ok {
		return out + mutedStyle.Render("; while it is gated the ") + textStyle.Render(actor) +
			mutedStyle.Render(" takes ") + textStyle.Render(next)
	}
	return out + mutedStyle.Render("; the ") + textStyle.Render(actor) +
		mutedStyle.Render(" has no other ready candidate")
}

// nextReady is the pick the actor takes while name is gated (§4): the first
// entry in that actor's list after, then before, this one that is on and not
// gated.
func nextReady(doc relevo.ConfigDoc, env Env, actor, name string) (string, bool) {
	type slot struct {
		name string
		on   bool
	}
	var list []slot
	at := -1
	for _, e := range doc.Actors[actor].Candidates {
		n := candName(doc, e.Candidate)
		if n == "" {
			continue
		}
		if n == name && at < 0 {
			at = len(list)
		}
		list = append(list, slot{name: n, on: !e.Off})
	}
	if at < 0 {
		return "", false
	}
	ready := func(i int) (string, bool) {
		s := list[i]
		if !s.on || s.name == name {
			return "", false
		}
		for _, c := range doc.Candidates {
			if c.Name != s.name {
				continue
			}
			token := c.Ref().String()
			for j := range env.Report.Gated {
				if env.Report.Gated[j].Token == token {
					return "", false
				}
			}
			return s.name, true
		}
		return "", false
	}
	for i := at + 1; i < len(list); i++ {
		if n, ok := ready(i); ok {
			return n, true
		}
	}
	for i := 0; i < at; i++ {
		if n, ok := ready(i); ok {
			return n, true
		}
	}
	return "", false
}

// candDetailLines is the cursor row's detail block (§4): the name and its
// triple, the gate when there is one, and the pick sentence.
func candDetailLines(doc relevo.ConfigDoc, env Env, r candRow, width int) []string {
	c := r.c
	meta := c.Harness + " · " + c.Provider + " · " + c.Model
	first := "   " + faintStyle.Bold(true).Render(c.Name) + "   " + mutedStyle.Render(meta)
	out := []string{fit(first, width)}
	if r.gate != nil {
		g := *r.gate
		line := "   " + redStyle.Render("gated") +
			mutedStyle.Render(" since "+candSince(g.Since, env.Now)) +
			mutedStyle.Render(", "+candUntilText(g, env.Now))
		if reason := statsGateReasonText(g.Note); reason != "" {
			line += "   " + textStyle.Render(reason)
		}
		out = append(out, fit(line, width))
	}
	return append(out, fit("   "+candPickSentence(doc, env, r), width))
}

// bodyLines is the whole body before it is windowed: a blank line under the
// context row, the table, the unused-provider table when there is one, two
// blank lines and the cursor row's detail block (§4).
func (v candidatesView) bodyLines(env Env, width int) []string {
	rows := v.rows(env)
	unused := env.Report.Unused
	nameW, cols := candLayout(width)
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	lines := []string{""}
	lines = append(lines, candHeaderLine(nameW, cols, cw))
	cur := candClamp(v.cur, len(rows)+len(unused))
	for i, r := range rows {
		lines = append(lines, candDataLine(r, i == cur, nameW, cols, cw, env.Now))
	}
	if len(unused) > 0 {
		reasonW, setByW := unusedCols(width, nameW)
		lines = append(lines, "", unusedHeaderLine(nameW, reasonW, setByW, cw))
		for i, g := range unused {
			lines = append(lines, unusedDataLine(g, len(rows)+i == cur, nameW, reasonW, setByW, cw, env.Now))
		}
	}
	lines = append(lines, "", "")
	if cur < len(rows) {
		return append(lines, candDetailLines(v.doc, env, rows[cur], width)...)
	}
	return append(lines, unusedDetailLines(unused[cur-len(rows)], env.Now, width)...)
}

// rows is the table for the newest report, in pick order (§4).
func (v candidatesView) rows(env Env) []candRow { return candRows(v.doc, env.Report.Gated) }

// candClamp keeps an index inside [0, n).
func candClamp(i, n int) int {
	if n <= 0 || i < 0 {
		return 0
	}
	if i > n-1 {
		return n - 1
	}
	return i
}

// follow scrolls the page so the cursor's table row stays visible (§4).
func (v *candidatesView) follow(env Env) {
	rows := v.rows(env)
	n := len(rows)
	if n == 0 && len(env.Report.Unused) == 0 {
		v.top = 0
		return
	}
	avail := bodyHeight(env)
	if avail <= 0 {
		v.top = 0
		return
	}
	lines := v.bodyLines(env, env.Width)
	sel := 2 + v.cur // the blank line, the header row, then the rows
	if v.cur >= n {
		// Past the candidates come the unused table's blank line and header
		// before its first row.
		sel = 2 + n + 2 + (v.cur - n)
	}
	if sel < v.top {
		v.top = sel
	}
	if sel >= v.top+avail {
		v.top = sel - avail + 1
	}
	v.top = clamp(v.top, 0, max(0, len(lines)-avail))
}

func (v candidatesView) Crumbs() []string { return []string{"candidates"} }

// Capturing is always false: the view owns no text input of its own (§4).
func (v candidatesView) Capturing() bool { return false }

// Keys are the view's keys, shown only when the shell has an Actions seam
// (§4). `↑↓ move` is left out: the footer has no room for it at 132 columns
// once `p probe` is included (§10), so it lives in HelpKeys alone.
func (v candidatesView) Keys() []KeyHelp {
	if !v.actions {
		return nil
	}
	return []KeyHelp{
		{"enter", "edit"},
		{"a", "add"},
		{"d", "delete"},
		{"g", "gate"},
		{"u", "ungate"},
		{"p", "probe"},
	}
}

// HelpKeys is the help overlay's key list (§2.2, §10): every key the view has,
// `↑↓ move` included, so the `?` modal shows what the footer cannot.
func (v candidatesView) HelpKeys() []KeyHelp {
	if !v.actions {
		return nil
	}
	return []KeyHelp{
		{"↑↓", "move"},
		{"enter", "edit"},
		{"a", "add"},
		{"d", "delete"},
		{"g", "gate"},
		{"u", "ungate"},
		{"p", "probe"},
	}
}

// OffKeys greys the candidate-only keys on an unused-provider row (§4): the
// cursor row is not a candidate, so editing, deleting, gating and probing it
// do nothing. Nil everywhere else, including a view with no Actions seam.
func (v candidatesView) OffKeys(env Env) []string {
	if !v.actions {
		return nil
	}
	if len(env.Report.Unused) == 0 || v.cur < len(v.rows(env)) {
		return nil
	}
	return []string{"enter", "d", "g", "p"}
}

// Context is the counts line on the left and the row order on the right (§4).
func (v candidatesView) Context(env Env) (string, string) {
	rows := v.rows(env)
	total, ready, gated, off := 0, 0, 0, 0
	for _, r := range rows {
		total++
		switch {
		case !r.on:
			off++
		case r.gate != nil:
			gated++
		default:
			ready++
		}
	}
	item := func(count int, label string) string {
		return textStyle.Bold(true).Render(fmt.Sprintf("%d", count)) + " " + mutedStyle.Render(label)
	}
	left := "   " + item(total, "candidates")
	left += "   " + item(ready, "ready")
	left += "   " + item(gated, "gated")
	if off > 0 {
		left += "   " + item(off, "off")
	}
	if m := len(env.Report.Unused); m > 0 {
		label := "gate on an unused provider"
		if m > 1 {
			label = "gates on unused providers"
		}
		left += "   " + item(m, label)
	}
	right := faintStyle.Render("sort ") + mutedStyle.Render("pick order")
	return left, right
}

func (v candidatesView) Body(env Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if !v.loaded && v.err != nil {
		return strings.Join(statsCentered(v.err.Error(), width, height), "\n")
	}
	if !v.loaded {
		return strings.Join(blockLines([]string{"loading…"}, width, height), "\n")
	}
	rows := v.rows(env)
	if len(rows) == 0 {
		return strings.Join(statsCentered("no candidates; a adds one", width, height), "\n")
	}
	lines := v.bodyLines(env, width)
	top := clamp(v.top, 0, max(0, len(lines)-height))
	if top > len(lines) {
		top = len(lines)
	}
	end := top + height
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(fitLines(lines[top:end], width, height), "\n")
}

func (v candidatesView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case candDocMsg:
		if msg.err != nil {
			v.err = msg.err
		} else {
			v.doc = msg.doc
			v.err = nil
			v.loaded = true
		}
		v.cur = candClamp(v.cur, len(v.rows(env))+len(env.Report.Unused))
		return v, nil

	case statusMsg:
		// A refresh from anywhere re-reads the config, which is what
		// reloads the table after ApplyConfig's Refresh (§4).
		return v, candDocCmd(env)

	case tea.KeyMsg:
		if !v.actions {
			return v, nil
		}
		return v.updateKey(msg, env)
	}
	return v, nil
}

// updateKey is the view's own keys (§4). The cursor ranges over the candidate
// rows then the unused-provider rows; the candidate-only keys do nothing on an
// unused row, where `u` clears the provider's gate and `a` still adds one.
func (v candidatesView) updateKey(k tea.KeyMsg, env Env) (View, tea.Cmd) {
	rows := v.rows(env)
	n := len(rows)
	total := n + len(env.Report.Unused)
	onUnused := v.cur >= n
	switch k.String() {
	case "up":
		v.cur = candClamp(v.cur-1, total)
		v.follow(env)
	case "down":
		v.cur = candClamp(v.cur+1, total)
		v.follow(env)
	case "home":
		v.cur = 0
		v.follow(env)
	case "end":
		v.cur = candClamp(total-1, total)
		v.follow(env)
	case "pgup":
		v.cur = candClamp(v.cur-bodyHeight(env), total)
		v.follow(env)
	case "pgdown":
		v.cur = candClamp(v.cur+bodyHeight(env), total)
		v.follow(env)
	case "enter":
		if n == 0 || onUnused {
			return v, nil
		}
		return v, openOverlay(newCandidateForm(env, v.doc, rows[v.cur].c.Name, servesText(rows[v.cur]), ""))
	case "a":
		if onUnused {
			return v, openOverlay(newCandidateForm(env, v.doc, "", "", ""))
		}
		if n == 0 {
			return v, nil
		}
		return v, openOverlay(newCandidateForm(env, v.doc, "", "", rows[v.cur].c.Harness))
	case "d":
		if n == 0 || onUnused {
			return v, nil
		}
		return v, v.deleteCmd(env, rows[v.cur])
	case "g":
		if n == 0 || onUnused {
			return v, nil
		}
		return v, v.gateForm(env, rows[v.cur])
	case "u":
		if n == 0 {
			return v, nil
		}
		if onUnused {
			provider := env.Report.Unused[v.cur-n].Provider
			return v, runAction(env.Ctx, "ungate", provider, func(ctx context.Context) Result {
				return env.Actions.Ungate(ctx, provider)
			})
		}
		name := rows[v.cur].c.Name
		return v, runAction(env.Ctx, "ungate", name, func(ctx context.Context) Result {
			return env.Actions.Ungate(ctx, name)
		})
	case "p":
		if n == 0 || onUnused {
			return v, nil
		}
		name := rows[v.cur].c.Name
		return v, runAction(env.Ctx, "probe", name, func(ctx context.Context) Result {
			return env.Actions.Probe(ctx, name)
		})
	}
	return v, nil
}

// deleteCmd is the d key (§4): validate the delete, then confirm it as a
// danger box. A refused delete is a notice, not an overlay.
func (v candidatesView) deleteCmd(env Env, r candRow) tea.Cmd {
	edit, err := relevo.DeleteCandidate(v.doc, r.c.Name)
	if err != nil {
		return notice(err.Error())
	}
	name := r.c.Name
	return openOverlay(confirmBox{
		kind:   "delete",
		yes:    "delete",
		danger: true,
		title:  "Delete " + accentStyle.Bold(true).Render(name) + "?",
		lines:  candDeleteLines(v.doc, env, r),
		onYes: runAction(env.Ctx, "delete", name, func(ctx context.Context) Result {
			return env.Actions.ApplyConfig(ctx, edit)
		}),
	})
}

// candLeft is one actor left with a single on-candidate after a delete.
type candLeft struct{ actor, name string }

// candActorsLeft is every actor whose on-list shrinks to exactly one
// remaining entry when name is deleted (§4).
func candActorsLeft(doc relevo.ConfigDoc, name string) []candLeft {
	var out []candLeft
	for _, actor := range actorOrder(doc.Actors) {
		var remaining []string
		had := false
		for _, e := range doc.Actors[actor].Candidates {
			n := candName(doc, e.Candidate)
			if n == "" || e.Off {
				continue
			}
			if n == name {
				had = true
				continue
			}
			remaining = append(remaining, n)
		}
		if had && len(remaining) == 1 {
			out = append(out, candLeft{actor: actor, name: remaining[0]})
		}
	}
	return out
}

// candDeleteLines is the delete confirm's text (§4): who picked it and whether
// they drop it, who is left with one candidate, the gate kept, and where its
// past rounds are.
func candDeleteLines(doc relevo.ConfigDoc, env Env, r candRow) []string {
	label := func(l string) string { return pad(l, 11) }
	var out []string
	if len(r.slots) > 0 {
		drop := "it drops it"
		if len(r.slots) > 1 {
			drop = "both drop it"
		}
		out = append(out, label("picked by")+servesText(r)+"   "+drop)
	}
	for _, left := range candActorsLeft(doc, r.c.Name) {
		out = append(out, label("")+"the "+left.actor+" is left with "+left.name)
	}
	if r.gate != nil {
		out = append(out, label("kept")+r.c.Provider+"'s gate, "+candUntilText(*r.gate, env.Now))
	}
	return append(out, label("")+"its past rounds in :rounds and :stats")
}

// gateForm is the g key (§4): the same form gateCmd builds, for a candidate
// instead of a binding row.
func (v candidatesView) gateForm(env Env, r candRow) tea.Cmd {
	name, provider := r.c.Name, r.c.Provider

	forField := formField{
		label: "for",
		input: newFormInput(true),
		hint:  "e.g. 30m, 2h, 1d · empty = until cleared",
	}
	forField.validate = func(value string) error {
		s := strings.TrimSpace(value)
		if s == "" {
			return nil
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		if d <= 0 {
			return errors.New("duration must be positive")
		}
		return nil
	}
	reasonField := formField{label: "reason", input: newFormInput(false), hint: "optional"}

	header := accentStyle.Bold(true).Render("Gate "+name) +
		faintStyle.Render("  (provider "+provider+")")

	return openOverlay(formBox{
		kind:   "gate",
		submit: "gate",
		header: []string{header},
		note:   []string{"The pick skips it until then. Builders already running are not stopped."},
		fields: []formField{forField, reasonField},
		onSubmit: func(values []string) tea.Cmd {
			forDur, _ := time.ParseDuration(strings.TrimSpace(values[0]))
			reason := values[1]
			return runAction(env.Ctx, "gate", name, func(ctx context.Context) Result {
				return env.Actions.Gate(ctx, name, forDur, reason)
			})
		},
	})
}
