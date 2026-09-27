package ui

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/view"
)

const maxErrorLines = 8

// renderError wraps err across the terminal width, preserving the newlines
// the message already has, capped at maxErrorLines with a trailing "…" when
// it overflows. Moved here from list.go (R2.7).
func renderError(err error, width int) string {
	if err == nil {
		return ""
	}
	if width <= 0 {
		width = 80
	}
	rawLines := strings.Split(err.Error(), "\n")
	var wrapped []string
	for _, l := range rawLines {
		wrapped = append(wrapped, wrapLine(l, width)...)
	}
	if len(wrapped) == 0 {
		return ""
	}
	if len(wrapped) > maxErrorLines {
		wrapped = wrapped[:maxErrorLines]
		last := wrapped[maxErrorLines-1]
		ellipsis := "…"
		ew := lipgloss.Width(ellipsis)
		for lipgloss.Width(last)+ew > width && len(last) > 0 {
			runes := []rune(last)
			last = string(runes[:len(runes)-1])
		}
		wrapped[maxErrorLines-1] = last + ellipsis
	}
	return strings.Join(wrapped, "\n")
}

// wrapLine word-wraps one line to width. Moved here from list.go (R2.7).
func wrapLine(line string, width int) []string {
	if width <= 0 {
		width = 80
	}
	if lipgloss.Width(line) <= width {
		return []string{line}
	}
	words := strings.Split(line, " ")
	var lines []string
	var cur strings.Builder
	curW := 0

	for _, w := range words {
		ww := lipgloss.Width(w)
		if cur.Len() == 0 {
			if ww > width {
				for _, r := range w {
					rw := lipgloss.Width(string(r))
					if curW+rw > width && cur.Len() > 0 {
						lines = append(lines, cur.String())
						cur.Reset()
						curW = 0
					}
					cur.WriteRune(r)
					curW += rw
				}
			} else {
				cur.WriteString(w)
				curW = ww
			}
		} else {
			if curW+1+ww <= width {
				cur.WriteByte(' ')
				cur.WriteString(w)
				curW += 1 + ww
			} else {
				lines = append(lines, cur.String())
				cur.Reset()
				curW = 0
				if ww > width {
					for _, r := range w {
						rw := lipgloss.Width(string(r))
						if curW+rw > width && cur.Len() > 0 {
							lines = append(lines, cur.String())
							cur.Reset()
							curW = 0
						}
						cur.WriteRune(r)
						curW += rw
					}
				} else {
					cur.WriteString(w)
					curW = ww
				}
			}
		}
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

// errorRows is the line count of renderError(err, width) when err != nil,
// else 0.
func errorRows(err error, width int) int {
	if err == nil {
		return 0
	}
	return strings.Count(renderError(err, width), "\n") + 1
}

// bodyHeight is the shell's body height: height minus the header, blank row and the
// context row, the error block, and the rule and keys rows (§5.3).
func bodyHeight(env Env) int {
	h := env.Height - 3 - env.ErrRows - 2
	if h < 0 {
		return 0
	}
	return h
}

// needsYouCount is the needs-you phrase the header and the fleet's context
// line share, so the two can never drift: "1 needs you" for one, "N need
// you" otherwise (A3).
func needsYouCount(n int) string {
	if n == 1 {
		return "1 needs you"
	}
	return fmt.Sprintf("%d need you", n)
}

// headerView is row 1: the breadcrumb on the left, attention and the clock
// headerView is row 1: the relevo mark and crumbs on the left, attention, version
// and the clock on the right (§2.2). No gates, no background.
func (m Model) headerView(env Env) string {
	var allCrumbs []string
	for _, v := range m.stack {
		allCrumbs = append(allCrumbs, v.Crumbs()...)
	}
	left := "  " + accentStyle.Bold(true).Render("◆ relevo")
	if len(allCrumbs) == 1 {
		left += "   " + chip(kbdStyle, allCrumbs[0])
	} else if len(allCrumbs) > 1 {
		var crumbParts []string
		for i := 0; i < len(allCrumbs)-1; i++ {
			crumbParts = append(crumbParts, mutedStyle.Render(allCrumbs[i]))
		}
		left += "   " + strings.Join(crumbParts, faintStyle.Render(" › ")) + faintStyle.Render(" › ") + chip(kbdStyle, allCrumbs[len(allCrumbs)-1])
	}

	n := 0
	for _, b := range env.Report.Bindings {
		// A report ready for the human mastermind needs them exactly as a
		// NEEDS YOU question does, so it counts here too (§4.5).
		if b.Display == "NEEDS YOU" || reportReady(b) {
			n++
		}
	}

	var rightParts []string
	if n > 0 {
		rightParts = append(rightParts, chip(chipWarnStyle.Bold(true), "● "+needsYouCount(n)))
	}
	if sv := shortVersion(m.opts.Version); sv != "" {
		rightParts = append(rightParts, faintStyle.Render(sv))
	}
	rightParts = append(rightParts, mutedStyle.Render(env.Now.Local().Format("15:04")))
	right := strings.Join(rightParts, "    ") + "  "

	return fit(spread(left, right, env.Width), env.Width)
}

// contextView is row 2: the top view's context, spread to width (§5.3).
func (m Model) contextView(env Env) string {
	left, right := m.top().Context(env)
	return fit(spread(left, right, env.Width), env.Width)
}

// errorBlock is the optional rows under the context: the refresh error,
// wrapped and capped (§5.3).
func (m Model) errorBlock(env Env) string {
	return renderError(m.err, env.Width)
}

// keysView is the last row: the global tail and as many of the top view's
// keys as fit, the notices and the refresh failure on the right (§2.2).
// Each key is a kbd chip ` k ` followed by ` label` in muted. Keys are separated by
// five spaces. The view's `Keys()` come first, then the global tail `: command`,
// `? all keys`, `q quit` (root) / `esc back` (deeper). Drop and notice rules are unchanged.
// While the command line, the help overlay or any overlay implementing
// overlayKeyer is up, that overlay's own keys stand in for the view's keys and
// the global tail (§2.1).
func (m Model) keysView(env Env) string {
	key := func(k, v string) string { return chip(kbdStyle, k) + " " + mutedStyle.Render(v) }
	tail := []string{key(":", "command"), key("?", "all keys")}
	if len(m.stack) > 1 {
		tail = append(tail, key("esc", "back"))
	} else {
		tail = append(tail, key("q", "quit"))
	}
	tailText := strings.Join(tail, "     ")

	var notes []string
	if w := m.workingText(); w != "" {
		notes = append(notes, mutedStyle.Render(w))
	}
	if m.notice != "" {
		notes = append(notes, m.noticeStyle().Render(m.notice))
	}
	if m.err != nil {
		notes = append(notes, errorStyle.Render("! refresh failed (retrying)"))
	}
	right := strings.Join(notes, "   ")

	// The right side is laid out first: what is left belongs to the tail,
	// and to the view's keys in their priority order.
	room := env.Width - lipgloss.Width(right) - 1
	if room < 0 {
		room = 0
	}

	left := tailText
	if modalKeys, ok := m.modalKeys(); ok {
		// A modal shows its own keys instead of the view's and the tail
		// (§2.1).
		left = layKeys(modalKeys, room, key)
	} else {
		avail := room - lipgloss.Width(tailText) - 5 // 5 for the gap before the tail
		viewKey := key
		if off, ok := m.top().(offKeyer); ok {
			offKeys := off.OffKeys(env)
			viewKey = func(k, v string) string {
				if slices.Contains(offKeys, k) {
					return chip(offKbdStyle, k) + " " + offStyle.Render(v)
				}
				return key(k, v)
			}
		}
		if viewKeys := layKeys(m.top().Keys(), avail, viewKey); viewKeys != "" {
			left = viewKeys + "     " + tailText
		}
	}

	if lipgloss.Width(left)+1+lipgloss.Width(right) > env.Width {
		room := env.Width - lipgloss.Width(right) - 1
		if room < 0 {
			room = 0
		}
		left = lipgloss.NewStyle().MaxWidth(room).Render(left)
	}
	return fit(spread(left, right, env.Width), env.Width)
}

// modalKeys is the keys of whichever overlay is up, and whether one is: the
// command line, help, or an overlay that implements overlayKeyer (§2.1).
func (m Model) modalKeys() ([]KeyHelp, bool) {
	switch {
	case m.cmd.open:
		return []KeyHelp{{"↑↓", "move"}, {"tab", "complete"}, {"enter", "go"}, {"esc", "close"}}, true
	case m.help:
		return []KeyHelp{{"esc", "close"}}, true
	case m.overlay != nil:
		if ok, isKeyer := m.overlay.(overlayKeyer); isKeyer {
			return ok.keys(), true
		}
	}
	return nil, false
}

// layKeys lays out keys as far as they fit in avail cells, separated by five
// spaces. Each key is ` k ` in a kbd chip followed by its label in muted.
func layKeys(keys []KeyHelp, avail int, key func(k, v string) string) string {
	var parts []string
	used := 0
	for _, kh := range keys {
		p := key(kh.Key, kh.Help)
		w := lipgloss.Width(p)
		if len(parts) > 0 {
			w += 5
		}
		if used+w > avail {
			break
		}
		parts = append(parts, p)
		used += w
	}
	return strings.Join(parts, "     ")
}

// body is row 4: the top view's body, or the command box, help overlay or a
// modal overlay when one is up (§5.3).
func (m Model) body(env Env) string {
	bh := bodyHeight(env)
	if m.cmd.open {
		title, rows, want, danger := m.cmdModal(env)
		return m.overlayBody(env, bh, title, rows, want, danger)
	}
	if m.help {
		return m.helpBody(env, bh)
	}
	if m.overlay != nil {
		if mo, ok := m.overlay.(modalOverlay); ok {
			title, rows, want, danger := mo.modal(env.Width)
			return m.overlayBody(env, bh, title, rows, want, danger)
		}
		box := m.overlay.view(env.Width)
		lines := strings.Split(m.top().Body(env, env.Width, bh), "\n")
		start := len(lines) - len(box)
		if start < 0 {
			start = 0
		}
		for i := 0; i < len(box) && start+i < len(lines); i++ {
			lines[start+i] = box[i]
		}
		return strings.Join(fitLines(lines, env.Width, bh), "\n")
	}
	return m.top().Body(env, env.Width, bh)
}

// overlayBody dims the top view's body and composes the modal box over it:
// centred, in the upper third (§2.3).
func (m Model) overlayBody(env Env, bh int, title string, rows []string, want int, danger bool) string {
	lines := strings.Split(m.top().Body(env, env.Width, bh), "\n")
	dimmed := dimLines(lines)
	box := renderModal(title, rows, want, env.Width, danger)
	boxW := 0
	if len(box) > 0 {
		boxW = lipgloss.Width(box[0])
	}
	x := (env.Width - boxW) / 2
	y := (bh - len(box)) / 3
	if y < 0 {
		y = 0
	}
	composed := composeBox(dimmed, box, x, y, env.Width)
	return strings.Join(fitLines(composed, env.Width, bh), "\n")
}

// noticeStyle is the sticky notice's colour: red for an action's error, faint
// for a captured stderr line, amber otherwise (§4.3, §4.4).
func (m Model) noticeStyle() lipgloss.Style {
	switch {
	case m.noticeErr:
		return errorStyle
	case m.noticeFaint:
		return faintStyle
	}
	return stateNeedsYouStyle
}

// workingText is the footer's action indicator: "working: <verb> <key>…" for
// every action in flight, in key order so the line is stable (§4.3).
func (m Model) workingText() string {
	if len(m.running) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m.running))
	for k := range m.running {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, m.running[k]+" "+k+"…")
	}
	return "working: " + strings.Join(parts, ", ")
}

// cmdModal is the command line's box (§3.1): the input, then the matches
// grouped into the VIEWS, BINDINGS and GATES sections, then the overflow and
// key rows. Title "command", want 80.
func (m Model) cmdModal(env Env) (string, []string, int, bool) {
	const want = 80
	innerW := modalInnerW(want, env.Width)
	ms := m.cmd.matches(env)

	type entry struct {
		idx int
		c   command
	}
	grouped := map[string][]entry{}
	for i, c := range ms {
		section := cmdSection(c)
		grouped[section] = append(grouped[section], entry{i, c})
	}
	// Done bindings sort after every other binding match (§3.1).
	if es := grouped["BINDINGS"]; len(es) > 0 {
		var live, done []entry
		for _, e := range es {
			if b, ok := m.bindingFor(env, e.c); ok && groupOf(b) == groupDone {
				done = append(done, e)
			} else {
				live = append(live, e)
			}
		}
		grouped["BINDINGS"] = append(live, done...)
	}

	rows := []string{m.cmd.input.View(), ""}
	for _, section := range []string{"VIEWS", "BINDINGS", "GATES"} {
		entries := grouped[section]
		if len(entries) == 0 {
			continue
		}
		rows = append(rows, modalSection(section))
		for _, e := range entries {
			name, desc := e.c.name, e.c.help
			if section == "BINDINGS" {
				if b, ok := m.bindingFor(env, e.c); ok {
					desc = strings.Join([]string{
						groupMetas[groupOf(b)].label,
						fmt.Sprintf("r%d", b.Round),
						candidateText(b),
					}, " · ")
				}
			}
			rows = append(rows, modalRow(e.idx == m.cmd.sel, name, desc, "", innerW, accentStyle, dimStyle))
		}
	}
	if n := m.cmd.matchCount(env); n > len(ms) {
		rows = append(rows, faintStyle.Render(fmt.Sprintf("+ %d more match; keep typing", n-len(ms))))
	}
	rows = append(rows, rightAligned(faintStyle.Render("tab complete · enter go · esc close"), innerW))
	return "command", rows, want, false
}

// bindingFor finds the report row a `round <key>` completion opens.
func (m Model) bindingFor(env Env, c command) (view.BindingStatus, bool) {
	key := strings.TrimPrefix(c.name, "round ")
	for _, b := range env.Report.Bindings {
		if b.Key() == key {
			return b, true
		}
	}
	return view.BindingStatus{}, false
}

// rightAligned pads text so it ends at innerW.
func rightAligned(text string, innerW int) string {
	pad := innerW - lipgloss.Width(text)
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat(" ", pad) + text
}

// globalKeys are the shell's own keys, shown first in the help overlay.
var globalKeys = []KeyHelp{
	{":", "command"},
	{"/", "filter"},
	{"?", "help"},
	{"esc", "back"},
	{"q", "quit / back"},
	{"wheel", "scroll"},
	{"shift+drag", "select text"},
}

// actKeys are the keys the help modal's ACT ON THE ROW column collects (§3.2).
var actKeys = map[string]bool{
	"s": true, "E": true, "b": true, "x": true, "D": true,
	"u": true, "g": true, "r": true, "o": true,
}

// helpColumns splits the top view's help keys into the help modal's two key
// columns (§3.2): ACT ON THE ROW holds the action keys, MOVE & VIEW the rest.
// ANYWHERE is globalKeys minus any key the view already shows, so a key shared
// with the view (e.g. "/ filter") appears once (§2.6).
func (m Model) helpColumns(env Env) (move, act, anywhere []KeyHelp) {
	keys := m.top().Keys()
	if hk, ok := m.top().(helpKeyer); ok {
		keys = hk.HelpKeys()
	}
	viewKeys := map[string]bool{}
	for _, kh := range keys {
		viewKeys[kh.Key] = true
		if actKeys[kh.Key] {
			act = append(act, kh)
		} else {
			move = append(move, kh)
		}
	}
	for _, kh := range globalKeys {
		if !viewKeys[kh.Key] {
			anywhere = append(anywhere, kh)
		}
	}
	return move, act, anywhere
}

// helpBody is the help overlay: the MOVE & VIEW, ACT ON THE ROW and ANYWHERE
// columns in a boxed modal (§3.2). Title "keys · <last crumb>", want 108.
func (m Model) helpBody(env Env, height int) string {
	title, rows, want, danger := m.helpModal(env)
	return m.overlayBody(env, height, title, rows, want, danger)
}

// helpModal builds the help modal's title and rows (§3.2).
func (m Model) helpModal(env Env) (string, []string, int, bool) {
	const want = 108
	innerW := modalInnerW(want, env.Width)
	move, act, anywhere := m.helpColumns(env)
	cols := [][]KeyHelp{move, act, anywhere}
	headers := []string{"MOVE & VIEW", "ACT ON THE ROW", "ANYWHERE"}
	colW := innerW / 3

	var rows []string
	head := ""
	for _, h := range headers {
		head += fit(modalSection(h), colW)
	}
	rows = append(rows, head)

	n := 0
	for _, col := range cols {
		if len(col) > n {
			n = len(col)
		}
	}
	for i := 0; i < n; i++ {
		line := ""
		for _, col := range cols {
			cell := ""
			if i < len(col) {
				cell = helpEntry(col[i])
			}
			line += fit(cell, colW)
		}
		rows = append(rows, line)
	}

	title := "keys"
	if crumbs := m.top().Crumbs(); len(crumbs) > 0 {
		title = "keys · " + crumbs[len(crumbs)-1]
	}
	return title, rows, want, false
}

// helpEntry is one help line: the key as a kbd chip padded to 12 cells, then
// its help in muted (§3.2).
func helpEntry(kh KeyHelp) string {
	return fit(chip(kbdStyle, kh.Key), 12) + " " + mutedStyle.Render(kh.Help)
}
