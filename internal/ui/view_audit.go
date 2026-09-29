package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/ui/dash"
)

// auditLogMsg is one ConfigLog load's reply.
type auditLogMsg struct {
	revs []db.RevisionRow
	err  error
}

// auditChangesMsg is one ConfigChanges load's reply: rev's changes, cached by
// both the list and, when it is the revision it shows, the pushed revision
// view.
type auditChangesMsg struct {
	rev   int64
	lines []relevo.ChangeLine
	err   error
}

// auditPreviewMsg is one RollbackPreview reply: the reply opens the confirm, or
// turns into a notice.
type auditPreviewMsg struct {
	rev   int64
	lines []relevo.ChangeLine
	err   error
}

// auditView is ':audit': the config revisions grouped by day, newest first,
// each row's change count, the cursor revision's changes in human words, and
// the R key.
type auditView struct {
	revs       []db.RevisionRow
	err        error // the last ConfigLog error; shown centred like candidates' doc error
	loaded     bool
	cur        int // cursor over revs, never a rule
	top        int // first body line shown (page follows the cursor)
	actions    bool
	changes    map[int64][]relevo.ChangeLine
	changesErr map[int64]error
}

// newAuditView builds ':audit' and the command that loads its log.
// env.Actions must be non-nil: execLine refuses the command otherwise.
func newAuditView(env Env) (View, tea.Cmd) {
	v := auditView{
		actions:    env.Actions != nil,
		changes:    map[int64][]relevo.ChangeLine{},
		changesErr: map[int64]error{},
	}
	return v, auditLogCmd(env)
}

// auditLogCmd reads the stored revisions off the update loop.
func auditLogCmd(env Env) tea.Cmd {
	return func() tea.Msg {
		revs, err := env.Actions.ConfigLog()
		return auditLogMsg{revs: revs, err: err}
	}
}

// auditChangesCmd reads one revision's changes off the update loop.
func auditChangesCmd(env Env, rev int64) tea.Cmd {
	return func() tea.Msg {
		lines, err := env.Actions.ConfigChanges(rev)
		return auditChangesMsg{rev: rev, lines: lines, err: err}
	}
}

// auditPreviewCmd asks what rolling back to rev would change.
func auditPreviewCmd(env Env, rev int64) tea.Cmd {
	return func() tea.Msg {
		lines, err := env.Actions.RollbackPreview(rev)
		return auditPreviewMsg{rev: rev, lines: lines, err: err}
	}
}

func (v auditView) Crumbs() []string { return []string{"audit"} }

// Capturing is always false: the view owns no text input of its own.
func (v auditView) Capturing() bool { return false }

// Keys are the list's keys. `R` needs the shell's write seam; `enter`
// does not, because opening a revision changes nothing.
func (v auditView) Keys() []KeyHelp {
	keys := []KeyHelp{{"enter", "all changes"}}
	if v.actions {
		keys = append(keys, KeyHelp{"R", "roll back to here"})
	}
	return keys
}

// HelpKeys is the help overlay's key list: `↑↓ move` is left out of the
// footer, so it lives here.
func (v auditView) HelpKeys() []KeyHelp {
	return append([]KeyHelp{{"↑↓", "move"}}, v.Keys()...)
}

// Context is the counts line on the left and the newest config version on the
// right: m is the sum of every row's CHANGES.
func (v auditView) Context(env Env) (string, string) {
	total := 0
	for _, r := range v.revs {
		total += revisionChangeCount(r)
	}
	item := func(count int, label string) string {
		if count != 1 {
			label += "s"
		}
		return textStyle.Bold(true).Render(fmt.Sprintf("%d", count)) + " " + mutedStyle.Render(label)
	}
	left := "   " + item(len(v.revs), "revision") + "   " + item(total, "change")
	right := ""
	if len(v.revs) > 0 {
		right = faintStyle.Render(fmt.Sprintf("config version %d", v.revs[0].Version))
	}
	return left, right
}

func (v auditView) Body(env Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if !v.loaded && v.err != nil {
		return strings.Join(statsCentered(v.err.Error(), width, height), "\n")
	}
	if !v.loaded {
		return strings.Join(blockLines([]string{"loading…"}, width, height), "\n")
	}
	if len(v.revs) == 0 {
		return strings.Join(statsCentered("no config revisions yet", width, height), "\n")
	}
	lines, _ := v.bodyLines(env, width)
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

func (v auditView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case auditLogMsg:
		if msg.err != nil {
			v.err = msg.err
		} else {
			v.revs = msg.revs
			v.err = nil
			v.loaded = true
		}
		v.cache()
		v.cur = candClamp(v.cur, len(v.revs))
		return v, v.loadChanges(env)

	case auditChangesMsg:
		v.cache()
		if msg.err != nil {
			v.changesErr[msg.rev] = msg.err
		} else {
			v.changes[msg.rev] = msg.lines
		}
		return v, nil

	case auditPreviewMsg:
		return v, auditPreviewOpen(env, v.newest(), msg)

	case statusMsg:
		// A refresh from anywhere re-reads the log, which is what reloads
		// the table after a roll back's Refresh.
		return v, auditLogCmd(env)

	case tea.KeyMsg:
		return v.updateKey(msg, env)
	}
	return v, nil
}

// updateKey is the list's own keys: movement over revisions, enter to
// open the cursor revision's changes, R to roll back to it.
func (v auditView) updateKey(k tea.KeyMsg, env Env) (View, tea.Cmd) {
	n := len(v.revs)
	switch k.String() {
	case "up":
		v.cur = candClamp(v.cur-1, n)
		v.follow(env)
		return v, v.loadChanges(env)
	case "down":
		v.cur = candClamp(v.cur+1, n)
		v.follow(env)
		return v, v.loadChanges(env)
	case "home":
		v.cur = 0
		v.follow(env)
		return v, v.loadChanges(env)
	case "end":
		v.cur = candClamp(n-1, n)
		v.follow(env)
		return v, v.loadChanges(env)
	case "pgup":
		v.cur = candClamp(v.cur-bodyHeight(env), n)
		v.follow(env)
		return v, v.loadChanges(env)
	case "pgdown":
		v.cur = candClamp(v.cur+bodyHeight(env), n)
		v.follow(env)
		return v, v.loadChanges(env)
	case "enter":
		if n == 0 {
			return v, nil
		}
		rev := v.revs[v.cur]
		lines, cached := v.changes[rev.Rev]
		return v, push(newAuditRevView(env, rev, v.newest(), lines, v.changesErr[rev.Rev], cached))
	case "R":
		if !v.actions || n == 0 {
			return v, nil
		}
		return v, auditPreviewCmd(env, v.revs[v.cur].Rev)
	}
	return v, nil
}

// cache makes the two caches writable: a value-built view can carry nil maps.
func (v *auditView) cache() {
	if v.changes == nil {
		v.changes = map[int64][]relevo.ChangeLine{}
	}
	if v.changesErr == nil {
		v.changesErr = map[int64]error{}
	}
}

// newest is the highest revision number, 0 when there is none.
func (v auditView) newest() int64 {
	if len(v.revs) == 0 {
		return 0
	}
	return v.revs[0].Rev
}

// loadChanges asks for the cursor revision's changes when they are not cached
// yet. A cached error counts as cached: the list does not retry it on
// every cursor move.
func (v *auditView) loadChanges(env Env) tea.Cmd {
	if !v.actions || len(v.revs) == 0 {
		return nil
	}
	rev := v.revs[candClamp(v.cur, len(v.revs))].Rev
	if _, ok := v.changes[rev]; ok {
		return nil
	}
	if _, ok := v.changesErr[rev]; ok {
		return nil
	}
	return auditChangesCmd(env, rev)
}

// follow scrolls the page so the cursor's revision row stays visible:
// the cursor is an index over revisions, so its body line is computed from the
// grouped layout, not from the index alone.
func (v *auditView) follow(env Env) {
	if len(v.revs) == 0 {
		v.top = 0
		return
	}
	avail := bodyHeight(env)
	if avail <= 0 {
		v.top = 0
		return
	}
	lines, sel := v.bodyLines(env, env.Width)
	if sel < v.top {
		v.top = sel
	}
	if sel >= v.top+avail {
		v.top = sel - avail + 1
	}
	v.top = clamp(v.top, 0, max(0, len(lines)-avail))
}

// auditTableWidth is the table's room: the full width less the 3-cell margin
// on each side.
func auditTableWidth(width int) int {
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	return cw
}

// auditMessageWidth is the MESSAGE column's room: the table width less the four
// fixed columns (WHEN, REV, SOURCE, CHANGES) and the two spaces between each of
// the five.
func auditMessageWidth(cw int) int {
	w := cw - 33
	if w < 0 {
		w = 0
	}
	return w
}

// padLeft pads s to width cells on the left, so the text is right-aligned.
func padLeft(s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		return strings.Repeat(" ", width-w) + s
	}
	return s
}

// auditHeaderLine is the table's header row, in faint bold, with the
// REV and CHANGES columns right-aligned as their numbers are.
func auditHeaderLine(cw int) string {
	style := faintStyle.Bold(true)
	cells := []candCell{
		{text: pad("WHEN", 5), style: style},
		{text: padLeft("REV", 4), style: style},
		{text: pad("SOURCE", 9), style: style},
		{text: pad("MESSAGE", auditMessageWidth(cw)), style: style},
		{text: padLeft("CHANGES", 7), style: style},
	}
	return candLine(cells, false, cw)
}

// auditDataLine is one revision's table row: the local time, the
// revision number (bold on the cursor), the source, the message, and the change
// count, faint at zero.
func auditDataLine(r db.RevisionRow, sel bool, cw int) string {
	revStyle := dimStyle
	if sel {
		revStyle = dimStyle.Bold(true)
	}
	changes := revisionChangeCount(r)
	changeStyle := dimStyle
	if changes == 0 {
		changeStyle = faintStyle
	}
	cells := []candCell{
		{text: pad(r.At.Local().Format("15:04"), 5), style: dimStyle},
		{text: padLeft(fmt.Sprintf("#%d", r.Rev), 4), style: revStyle},
		{text: pad(r.Source, 9), style: dimStyle},
		{text: pad(r.Message, auditMessageWidth(cw)), style: textStyle},
		{text: padLeft(fmt.Sprintf("%d", changes), 7), style: changeStyle},
	}
	return candLine(cells, sel, cw)
}

// auditDayRule is one day's rule line, mirroring dash's dayRuleLine
// (internal/ui/dash/render.go:309-325): the label dim, the fill in the grid
// style, and the revision count faint. dash's dayRuleLine is a method on an
// unexported dash type, so it cannot be called from here; only its shape and
// dash.DayLabel, which formats the label, are shared.
func auditDayRule(label string, n, cw int) string {
	right := " " + auditCount(n, "revision")
	leftWidth := lipgloss.Width(label) + 1
	fill := cw - leftWidth - lipgloss.Width(right)
	if fill < 0 {
		fill = 0
	}
	content := dimStyle.Render(label) + " " + gridStyle.Render(strings.Repeat("┈", fill)) + faintStyle.Render(right)
	return "   " + fit(content, cw) + "   "
}

// auditCount is "1 revision" or "3 revisions".
func auditCount(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// auditDayCount is the size of the day group that starts at revision i.
func auditDayCount(env Env, revs []db.RevisionRow, i int) int {
	day := dash.DayLabel(revs[i].At, env.Now, time.Local)
	n := 0
	for j := i; j < len(revs); j++ {
		if dash.DayLabel(revs[j].At, env.Now, time.Local) != day {
			break
		}
		n++
	}
	return n
}

// revisionChangeCount is the length of a revision row's stored Changes array, 0
// when it does not decode.
func revisionChangeCount(r db.RevisionRow) int {
	var arr []json.RawMessage
	if err := json.Unmarshal(r.Changes, &arr); err != nil {
		return 0
	}
	return len(arr)
}

// bodyLines is the whole body before it is windowed: a blank line under the
// context row, the table with a rule per day, two blank lines and the cursor
// revision's detail block. It returns the cursor row's body line too, so
// follow can scroll to it.
func (v auditView) bodyLines(env Env, width int) ([]string, int) {
	cw := auditTableWidth(width)
	lines := []string{"", auditHeaderLine(cw)}
	cur := candClamp(v.cur, len(v.revs))
	sel := 2
	for i, r := range v.revs {
		day := dash.DayLabel(r.At, env.Now, time.Local)
		if i == 0 || day != dash.DayLabel(v.revs[i-1].At, env.Now, time.Local) {
			lines = append(lines, auditDayRule(day, auditDayCount(env, v.revs, i), cw))
		}
		if i == cur {
			sel = len(lines)
		}
		lines = append(lines, auditDataLine(r, i == cur, cw))
	}
	lines = append(lines, "", "")
	room := v.top + bodyHeight(env) - len(lines)
	if room < 0 {
		room = 0
	}
	return append(lines, v.detailLines(env, width, room)...), sel
}

// detailLines is the cursor revision's detail block: the revision's
// header, then one line per change, up to the room left above the footer, with
// the ones that do not fit counted in a last line.
func (v auditView) detailLines(env Env, width, room int) []string {
	if len(v.revs) == 0 {
		return nil
	}
	r := v.revs[candClamp(v.cur, len(v.revs))]
	first := "   " + faintStyle.Bold(true).Render(fmt.Sprintf("#%d", r.Rev)) + "   " +
		mutedStyle.Render(fmt.Sprintf("%s · %s · %s", r.Source, r.At.Local().Format("Jan 2 15:04"), r.Message))
	out := []string{fit(first, width)}
	if room <= 1 {
		return out
	}

	if err, ok := v.changesErr[r.Rev]; ok {
		return append(out, fit("   "+errorStyle.Render(err.Error()), width))
	}
	lines, ok := v.changes[r.Rev]
	if !ok {
		return append(out, fit("   "+mutedStyle.Render("loading…"), width))
	}

	budget := room - 1
	shown, more := lines, 0
	if len(shown) > budget {
		more = len(shown) - budget + 1
		shown = shown[:budget-1]
	}
	for _, l := range shown {
		out = append(out, auditChangeLine(l, width))
	}
	if more > 0 {
		out = append(out, fit("   "+faintStyle.Render(fmt.Sprintf("+ %d more · enter shows all", more)), width))
	}
	return out
}

// auditPreviewOpen turns a RollbackPreview reply into the command the view
// returns: a notice for a refusal, an error or a no-change, else the
// danger confirm that lists exactly what the roll back would change.
func auditPreviewOpen(env Env, newest int64, msg auditPreviewMsg) tea.Cmd {
	var refused *relevo.RollbackRefused
	switch {
	case errors.Is(msg.err, config.ErrNoChange):
		return notice(fmt.Sprintf("config already equals #%d", msg.rev))
	case errors.As(msg.err, &refused):
		return notice(msg.err.Error())
	case msg.err != nil:
		return notice(msg.err.Error())
	}
	innerW := modalInnerW(72, env.Width)
	return openOverlay(confirmBox{
		kind:   "roll back",
		yes:    "roll back",
		danger: true,
		title:  "Roll back to " + accentStyle.Bold(true).Render(fmt.Sprintf("#%d", msg.rev)) + "?",
		lines:  auditConfirmLines(newest, msg.lines, innerW),
		onYes: runAction(env.Ctx, "rollback", fmt.Sprintf("#%d", msg.rev), func(ctx context.Context) Result {
			return env.Actions.Rollback(ctx, msg.rev)
		}),
	})
}

// auditConfirmLines is the danger confirm's body: the first change the
// roll back undoes, every further one indented under it, then the revision it
// saves as and the revisions it keeps.
func auditConfirmLines(newest int64, lines []relevo.ChangeLine, innerW int) []string {
	label := func(l string) string { return pad(l, 11) }
	out := make([]string, 0, len(lines)+3)
	for i, l := range lines {
		prefix := label("")
		if i == 0 {
			prefix = label("undoes")
		}
		out = append(out, prefix+auditChangeText(l, innerW-lipgloss.Width(prefix)))
	}
	return append(out,
		"",
		label("saves as")+fmt.Sprintf("#%d · rollback", newest+1),
		label("kept")+fmt.Sprintf("#%d and every revision before it", newest),
	)
}
