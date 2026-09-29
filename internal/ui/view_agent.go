package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// agentFilesMsg is one agentView load's reply (§4).
type agentFilesMsg struct {
	name  string
	files []harness.AgentFile
	err   error
}

// agentReloadMsg asks agentView to re-read its files, which is what the editor
// exits with (§4).
type agentReloadMsg struct{}

// agentView is `agents › <name>` (§4): one row per harness with the definition
// file's path, model pin and state, and the e and r keys.
type agentView struct {
	name   string
	row    agentRow // the list row this view was pushed from
	doc    relevo.ConfigDoc
	files  []harness.AgentFile
	err    error
	loaded bool
	cur    int // cursor over rows()
	top    int // first body line shown (page follows the cursor)
}

// newAgentView pushes the agent's detail view (§4). env.Actions must be
// non-nil: ':agents' refuses to open without it.
func newAgentView(env Env, doc relevo.ConfigDoc, r agentRow) (View, tea.Cmd) {
	v := agentView{name: r.name, row: r, doc: doc, files: r.files, loaded: true}
	if env.Actions == nil {
		return v, nil
	}
	return v, agentFilesCmd(env, r.name)
}

// agentFilesCmd reads one agent's definition states off the update loop.
func agentFilesCmd(env Env, name string) tea.Cmd {
	return func() tea.Msg {
		files, err := env.Actions.AgentFiles(name)
		return agentFilesMsg{name: name, files: files, err: err}
	}
}

// rows is the table: the loaded file states for a shipped agent and for a
// custom agent relevo renders from a source; for a custom native agent, one
// row per kind its native entry names, at the kind's convention path. A custom
// agent whose entry cannot be read has no rows.
func (v agentView) rows() []agentFileRow {
	if v.row.source == "shipped" || relevo.IsSourceAgent(v.doc.Agents, v.name) {
		out := make([]agentFileRow, 0, len(v.files))
		for _, f := range v.files {
			out = append(out, agentFileRow{kind: f.Kind, path: f.Path, model: f.Model, state: f.State})
		}
		return out
	}
	return customAgentRows(v.doc, v.name)
}

// agentFileLayout is the detail table's columns (§4): HARNESS 8, FILE taking
// the rest of width-6 cells, MODEL 30 and STATE 12, every column followed by
// two spaces.
func agentFileLayout(width int) (int, []candCol) {
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	cols := []candCol{
		{"model", "MODEL", 30},
		{"state", "STATE", 12},
	}
	fixed := 2 + 8 + 2 // the two spaces after HARNESS and the two after FILE
	for i, c := range cols {
		fixed += c.w
		if i < len(cols)-1 {
			fixed += 2
		}
	}
	return cw - fixed, cols
}

// agentFileHeaderLine is the detail table's header row (§4), in faint bold.
func agentFileHeaderLine(nameW int, cols []candCol, cw int) string {
	style := faintStyle.Bold(true)
	cells := []candCell{{text: pad("HARNESS", 8), style: style}, {text: pad("FILE", nameW), style: style}}
	for _, c := range cols {
		cells = append(cells, candCell{text: pad(c.head, c.w), style: style})
	}
	return candLine(cells, false, cw)
}

// agentStateStyle colours one row's STATE (§4): up to date muted, stale, your
// edit and edit + newer amber, missing faint.
func agentStateStyle(state harness.FileState) lipgloss.Style {
	switch state {
	case harness.FileUpToDate:
		return mutedStyle
	case harness.FileStale, harness.FileEdited, harness.FileEditedNewer:
		return warnStyle
	case harness.FileMissing:
		return faintStyle
	}
	return mutedStyle
}

// agentFileLine is one definition file's row (§4). The cursor row's harness
// name is bold; its path, model pin and state carry their own styles.
func agentFileLineOf(f agentFileRow, sel bool, nameW int, cols []candCol, cw int) string {
	nameStyle := textStyle
	if sel {
		nameStyle = nameStyle.Bold(true)
	}
	cells := []candCell{
		{text: pad(f.kind, 8), style: nameStyle},
		{text: pad(tildePath(f.path), nameW), style: mutedStyle},
	}
	for _, col := range cols {
		text := f.model
		style := mutedStyle
		if col.key == "state" {
			text, style = string(f.state), agentStateStyle(f.state)
		}
		cells = append(cells, candCell{text: pad(text, col.w), style: style})
	}
	return candLine(cells, sel, cw)
}

// bodyLines is the whole detail body before it is windowed: a blank line under
// the context row, then the table (§4).
func (v agentView) bodyLines(env Env, width int) []string {
	rows := v.rows()
	nameW, cols := agentFileLayout(width)
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	lines := []string{"", agentFileHeaderLine(nameW, cols, cw)}
	cur := candClamp(v.cur, len(rows))
	for i, f := range rows {
		lines = append(lines, agentFileLineOf(f, i == cur, nameW, cols, cw))
	}
	if cur < len(rows) && rows[cur].state == harness.FileEditedNewer {
		// The cursor row's edit sits on an older copy: say so, and what e and
		// r each do with it.
		f := rows[cur]
		newer := "this relevo ships a newer copy"
		if relevo.IsSourceAgent(v.doc.Agents, v.name) {
			newer = "relevo renders a newer copy from your config"
		}
		lines = append(lines, "", "",
			fit("   "+faintStyle.Bold(true).Render(f.kind)+"   "+mutedStyle.Render(tildePath(f.path)), width),
			fit("   "+warnStyle.Render("edit + newer")+mutedStyle.Render("  you edited this file, and "+newer), width),
			fit("   "+textStyle.Render("e")+mutedStyle.Render(" shows your edit; ")+textStyle.Render("r")+mutedStyle.Render(" replaces it with the newer copy"), width),
		)
	}
	return lines
}

// follow scrolls the page so the cursor's table row stays visible (§4).
func (v *agentView) follow(env Env) {
	rows := v.rows()
	if len(rows) == 0 {
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
	if sel < v.top {
		v.top = sel
	}
	if sel >= v.top+avail {
		v.top = sel - avail + 1
	}
	v.top = clamp(v.top, 0, max(0, len(lines)-avail))
}

func (v agentView) Crumbs() []string { return []string{v.name} }

// Capturing is always false: the view owns no text input of its own (§4).
func (v agentView) Capturing() bool { return false }

// Keys are the detail view's keys. A native custom agent's view has no `r`:
// relevo writes no file for it, so a reset has nothing to restore. `esc back`
// is left to the shell's own tail.
func (v agentView) Keys() []KeyHelp {
	keys := []KeyHelp{
		{"↑↓", "move"},
		{"e", "edit in $EDITOR"},
	}
	if v.row.source == "shipped" || relevo.IsSourceAgent(v.doc.Agents, v.name) {
		keys = append(keys, KeyHelp{"r", "reset"})
	}
	return keys
}

// HelpKeys is the help overlay's key list (§2.2): the same set.
func (v agentView) HelpKeys() []KeyHelp { return v.Keys() }

// Context is the agent's summary line (§4): the source and shape, who uses it,
// and how many harness kinds carry a file.
func (v agentView) Context(env Env) (string, string) {
	rows := v.rows()
	left := "   " + mutedStyle.Render(v.row.source+" · "+v.row.shape+" · "+agentUsedText(v.row)) +
		"   " + textStyle.Bold(true).Render(fmt.Sprintf("%d", len(rows))) + mutedStyle.Render(" harnesses")
	return left, ""
}

func (v agentView) Body(env Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if v.err != nil {
		return strings.Join(statsCentered(v.err.Error(), width, height), "\n")
	}
	if !v.loaded {
		return strings.Join(blockLines([]string{"loading…"}, width, height), "\n")
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

func (v agentView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case agentFilesMsg:
		if msg.err != nil {
			v.err = msg.err
		} else {
			v.files = msg.files
			v.err = nil
			v.loaded = true
		}
		v.cur = candClamp(v.cur, len(v.rows()))
		return v, nil

	case agentReloadMsg:
		// The user's editor exited: read the file states again, which is
		// what shows an edit (§4).
		return v, agentFilesCmd(env, v.name)

	case statusMsg:
		return v, agentFilesCmd(env, v.name)

	case tea.KeyMsg:
		return v.updateKey(msg, env)
	}
	return v, nil
}

// updateKey is the detail view's own keys (§4).
func (v agentView) updateKey(k tea.KeyMsg, env Env) (View, tea.Cmd) {
	if env.Actions == nil {
		return v, nil
	}
	rows := v.rows()
	n := len(rows)
	switch k.String() {
	case "up":
		v.cur = candClamp(v.cur-1, n)
		v.follow(env)
	case "down":
		v.cur = candClamp(v.cur+1, n)
		v.follow(env)
	case "home":
		v.cur = 0
		v.follow(env)
	case "end":
		v.cur = candClamp(n-1, n)
		v.follow(env)
	case "pgup":
		v.cur = candClamp(v.cur-bodyHeight(env), n)
		v.follow(env)
	case "pgdown":
		v.cur = candClamp(v.cur+bodyHeight(env), n)
		v.follow(env)
	case "e":
		if n == 0 {
			return v, nil
		}
		return v, v.editCmd(env, rows[v.cur])
	case "r":
		if n == 0 || (v.row.source != "shipped" && !relevo.IsSourceAgent(v.doc.Agents, v.name)) {
			return v, nil
		}
		return v, v.resetCmd(env, rows[v.cur])
	}
	return v, nil
}

// editCmd is the e key (§4): the user's editor on the cursor file, run with
// the terminal released. A file not on disk has nothing to open, and a failure
// to build the command is a notice.
func (v agentView) editCmd(env Env, f agentFileRow) tea.Cmd {
	if f.state == harness.FileMissing {
		return notice(f.kind + " has no " + v.name + " file yet")
	}
	cmd, err := env.Actions.AgentEditor(f.path)
	if err != nil {
		return notice(err.Error())
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return noticeMsg{text: err.Error()}
		}
		return agentReloadMsg{}
	})
}

// copyNoun names the copy a reset writes, as a noun phrase without an article:
// the one relevo ships for a shipped agent, or the one relevo renders from the
// user's config for a custom source agent.
func copyNoun(custom bool) string {
	if custom {
		return "copy relevo renders from your config"
	}
	return "copy this relevo ships"
}

// resetCmd is the r key: a file the user edited, one relevo has a newer copy
// of, or one relevo has never written can be reset; anything else is a notice.
// A reset is a danger confirm, because the edit it discards cannot come back. A
// missing file is a write rather than a reset: nothing is lost, the confirm's
// title, label and note say so.
func (v agentView) resetCmd(env Env, f agentFileRow) tea.Cmd {
	if f.state != harness.FileEdited && f.state != harness.FileEditedNewer && f.state != harness.FileStale && f.state != harness.FileMissing {
		return notice(f.kind + "'s " + v.name + " is up to date")
	}
	kind, name := f.kind, v.name
	noun := copyNoun(relevo.IsSourceAgent(v.doc.Agents, v.name))
	title := "Reset " + kind + "'s " + accentStyle.Bold(true).Render(name) + "?"
	label := "overwrites"
	note := "with the " + noun + "; your edit is lost"
	switch f.state {
	case harness.FileStale:
		note = "with the " + noun + "; relevo has a newer copy"
	case harness.FileEditedNewer:
		note = "with the newer " + noun + "; your edit is lost"
	case harness.FileMissing:
		title = "Write " + kind + "'s " + accentStyle.Bold(true).Render(name) + "?"
		label = "writes"
		note = "the " + noun
	}
	return openOverlay(confirmBox{
		kind:   "reset",
		yes:    "reset",
		danger: true,
		title:  title,
		lines: []string{
			pad(label, 11) + tildePath(f.path),
			pad("", 11) + note,
		},
		onYes: runAction(env.Ctx, "reset agent", kind+"/"+name, func(ctx context.Context) Result {
			return env.Actions.ResetAgentFile(ctx, kind, name)
		}),
	})
}
