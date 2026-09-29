package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// agentsMsg is one ':agents' load's reply (§4): the doc, and each agent's
// definition state. A failure from the doc or from any AgentFiles is err, and
// renders centred like candidates' doc error.
type agentsMsg struct {
	doc   relevo.ConfigDoc
	files map[string][]harness.AgentFile
	err   error
}

// agentsView is ':agents' (§4): relevo's shipped agents and the config's
// custom ones, each agent's definition state per installed harness, the
// cursor row's detail block, and the d key.
type agentsView struct {
	doc     relevo.ConfigDoc
	files   map[string][]harness.AgentFile // per agent name, from the load command
	err     error
	loaded  bool
	cur     int // cursor over rows()
	top     int // first body line shown (page follows the cursor)
	actions bool
}

// agentRow is one table row (§3), computed per render, never stored.
type agentRow struct {
	name   string
	shape  string              // "writer" | "reader"
	source string              // "shipped" | "custom"
	usedBy []string            // the actors that run it, plus those whose agent requires it, sorted
	files  []harness.AgentFile // nil for a custom agent
}

// agentFileRow is one row of the agent detail's table (§4): the harness kind,
// the definition file's path, its model pin and its state. The custom rows the
// list's detail block synthesizes use the same shape.
type agentFileRow struct {
	kind  string
	path  string
	model string
	state harness.FileState
}

// newAgentsView builds ':agents' and the command that loads its doc and file
// states (§4). env.Actions must be non-nil: execLine refuses the command
// otherwise.
func newAgentsView(env Env) (View, tea.Cmd) {
	v := agentsView{actions: env.Actions != nil}
	return v, agentsCmd(env)
}

// agentsCmd reads the doc and every agent's definition state off the update
// loop (§4). File reads are cheap, so they happen here, not in Body.
func agentsCmd(env Env) tea.Cmd {
	return func() tea.Msg {
		doc, err := env.Actions.ConfigDoc()
		if err != nil {
			return agentsMsg{err: err}
		}
		files := make(map[string][]harness.AgentFile)
		for _, name := range agentNames(doc) {
			fs, err := env.Actions.AgentFiles(name)
			if err != nil {
				return agentsMsg{err: err}
			}
			files[name] = fs
		}
		return agentsMsg{doc: doc, files: files}
	}
}

// agentNames is every agent with a row (§3): the shipped table in order, then
// the doc's custom agents by name.
func agentNames(doc relevo.ConfigDoc) []string {
	shipped := roles.ShippedAgents()
	names := make([]string, 0, len(shipped)+len(doc.Agents))
	for _, s := range shipped {
		names = append(names, s.Name)
	}
	custom := make([]string, 0, len(doc.Agents))
	for name := range doc.Agents {
		custom = append(custom, name)
	}
	sort.Strings(custom)
	return append(names, custom...)
}

// agents is the table (§3): every agent's row, from the doc and the loaded
// file states.
func agentRows(doc relevo.ConfigDoc, files map[string][]harness.AgentFile) []agentRow {
	use := agentUse(doc)
	names := agentNames(doc)
	out := make([]agentRow, 0, len(names))
	for _, name := range names {
		r := agentRow{name: name, usedBy: use[name]}
		if s, ok := roles.Shipped(name); ok {
			r.shape, r.source, r.files = string(s.Shape), "shipped", files[name]
		} else {
			r.shape, r.source = agentShapeOf(doc, name), "custom"
			if relevo.IsSourceAgent(doc.Agents, name) {
				r.files = files[name]
			}
		}
		out = append(out, r)
	}
	return out
}

// agentUse is every agent's user list (§3): for each actor, the agent it runs,
// plus the Requires of that agent when relevo ships it. A custom agent
// requires nothing. Each list is sorted.
func agentUse(doc relevo.ConfigDoc) map[string][]string {
	seen := make(map[string]map[string]bool, len(doc.Actors))
	add := func(agent, actor string) {
		if agent == "" {
			return
		}
		if seen[agent] == nil {
			seen[agent] = map[string]bool{}
		}
		seen[agent][actor] = true
	}
	for _, actor := range actorOrder(doc.Actors) {
		agent := doc.Actors[actor].Agent
		add(agent, actor)
		if s, ok := roles.Shipped(agent); ok {
			for _, req := range s.Requires {
				add(req, actor)
			}
		}
	}
	out := make(map[string][]string, len(seen))
	for agent, users := range seen {
		names := make([]string, 0, len(users))
		for actor := range users {
			names = append(names, actor)
		}
		sort.Strings(names)
		out[agent] = names
	}
	return out
}

// agentKinds is the kind column set (§4): the union of the loaded file states,
// in harness.All() order. A kind whose binary is not installed has no file
// state, so it has no column.
func agentKinds(files map[string][]harness.AgentFile) []string {
	present := map[string]bool{}
	for _, fs := range files {
		for _, f := range fs {
			present[f.Kind] = true
		}
	}
	var out []string
	for _, h := range harness.All() {
		if present[h.Kind] {
			out = append(out, h.Kind)
		}
	}
	return out
}

// agentListLayout is the columns to show at width and the AGENT column's room
// (§4): the table spans width-6 cells from column 3, each column followed by
// two spaces, AGENT taking the rest. While that would fall under 14 cells,
// columns leave in the order USED BY, SOURCE.
func agentListLayout(width int, kinds []string) (int, []candCol) {
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	visible := []candCol{
		{"shape", "SHAPE", 6},
		{"source", "SOURCE", 7},
		{"used", "USED BY", 19},
	}
	for _, kind := range kinds {
		visible = append(visible, candCol{"kind:" + kind, strings.ToUpper(kind), 8})
	}
	agentW := func(cols []candCol) int {
		fixed := 2 // the two spaces after AGENT
		for i, c := range cols {
			fixed += c.w
			if i < len(cols)-1 {
				fixed += 2
			}
		}
		return cw - fixed
	}
	for _, drop := range []string{"used", "source"} {
		if agentW(visible) >= 14 {
			break
		}
		for i, c := range visible {
			if c.key == drop {
				visible = append(append([]candCol(nil), visible[:i]...), visible[i+1:]...)
				break
			}
		}
	}
	return agentW(visible), visible
}

// agentHeaderLine is the list's header row (§4), in faint bold.
func agentHeaderLine(nameW int, cols []candCol, cw int) string {
	style := faintStyle.Bold(true)
	cells := []candCell{{text: pad("AGENT", nameW), style: style}}
	for _, c := range cols {
		cells = append(cells, candCell{text: pad(c.head, c.w), style: style})
	}
	return candLine(cells, false, cw)
}

// agentStateCell is one agent's cell on kind: ok green when the file is up to
// date, stale or edited amber, and `·` faint when there is no file state for
// the agent on that kind, as for a custom agent relevo does not write.
func agentStateCell(r agentRow, kind string) (string, lipgloss.Style) {
	for _, f := range r.files {
		if f.Kind != kind {
			continue
		}
		switch f.State {
		case harness.FileUpToDate:
			return "ok", greenStyle
		case harness.FileStale:
			return "stale", warnStyle
		case harness.FileEdited, harness.FileEditedNewer:
			return "edited", warnStyle
		}
		break
	}
	return "·", faintStyle
}

// agentUsedCell is the USED BY cell (§4): the actor names, or `no actor` when
// no actor runs the agent.
func agentUsedCell(r agentRow) string {
	if len(r.usedBy) == 0 {
		return "no actor"
	}
	return strings.Join(r.usedBy, ", ")
}

// agentUsedText is the `used by …` phrase of a detail line or a context row
// (§4): `used by a, b`, or `no actor uses it`.
func agentUsedText(r agentRow) string {
	if len(r.usedBy) == 0 {
		return "no actor uses it"
	}
	return "used by " + strings.Join(r.usedBy, ", ")
}

// agentDataLine is one agent's table row (§4). A row no actor uses is faint
// throughout; the cursor row's name is bold.
func agentDataLine(r agentRow, sel bool, nameW int, cols []candCol, cw int) string {
	base, meta := textStyle, mutedStyle
	if len(r.usedBy) == 0 {
		base, meta = faintStyle, faintStyle
	}
	nameStyle := base
	if sel {
		nameStyle = nameStyle.Bold(true)
	}
	cells := []candCell{{text: pad(r.name, nameW), style: nameStyle}}
	for _, col := range cols {
		text, style := "", meta
		switch {
		case col.key == "shape":
			text = r.shape
		case col.key == "source":
			text = r.source
		case col.key == "used":
			text = agentUsedCell(r)
		default:
			text, style = agentStateCell(r, strings.TrimPrefix(col.key, "kind:"))
		}
		cells = append(cells, candCell{text: pad(text, col.w), style: style})
	}
	return candLine(cells, sel, cw)
}

// tildePath is path with the user's home directory shown as `~` (§4), the same
// substitution repoCell makes.
func tildePath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return strings.Replace(path, home, "~", 1)
	}
	return path
}

// customAgentRows is a custom agent's rows (§4): one per kind its native entry
// names, else one per kind its source renders to. The path is the kind's
// convention path -- relevo never writes a custom agent, but the harness looks
// there (harness.DefinitionPath) -- home-resolved, so a caller can show it as
// `~`. relevo knows no state for it, so state stays empty.
func customAgentRows(doc relevo.ConfigDoc, name string) []agentFileRow {
	e, ok := doc.Agents[name]
	if !ok {
		return nil
	}
	path := func(kind, agent string) (string, bool) {
		rel, ok := harness.DefinitionPath(kind, agent)
		if !ok {
			return "", false
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, rel), true
		}
		return rel, true
	}
	var out []agentFileRow
	if e.Native != nil {
		for _, kind := range nativeAgentKinds(e) {
			if p, ok := path(kind, e.Native[kind].Agent); ok {
				out = append(out, agentFileRow{kind: kind, path: p})
			}
		}
		return out
	}
	src, err := agentsrc.Parse([]byte(e.Source))
	if err != nil {
		return nil
	}
	for _, kind := range agentsrc.RenderedKinds(src) {
		if p, ok := path(kind, name); ok {
			out = append(out, agentFileRow{kind: kind, path: p})
		}
	}
	return out
}

// nativeAgentKinds is a custom native agent's kinds, in harness.All() order.
func nativeAgentKinds(e roles.AgentEntry) []string {
	var out []string
	for _, h := range harness.All() {
		if _, ok := e.Native[h.Kind]; ok {
			out = append(out, h.Kind)
		}
	}
	return out
}

// agentFileLine is one definition-file line (§4): the kind (10, muted), the
// file path (pathW), the state (14, muted) when there is one, then `model <pin>`
// when the file carries a pin. The caller passes the path already in its `~`
// form.
func agentFileLine(kind, path, state, model string, pathW int) string {
	line := "   " + mutedStyle.Render(pad(kind, 10)) + pad(path, pathW)
	if state != "" {
		line += mutedStyle.Render(pad(state, 14))
	}
	if model != "" {
		line += mutedStyle.Render("model " + model)
	}
	return line
}

// agentDetailLines is the cursor row's detail block: the name and its triple,
// then one line per kind with the definition file's `~` path, its state and its
// model pin. A shipped agent and a custom source agent both list the files
// relevo manages; a custom native agent lists the file at the kind's convention
// path instead, which relevo never writes. The path column is one width for the
// block -- 46, or the longest `~` path it draws plus 2 -- so a long path keeps
// a gap before the state and every state starts in one column.
func agentDetailLines(doc relevo.ConfigDoc, r agentRow, width int) []string {
	meta := r.source + " · " + r.shape + " · " + agentUsedText(r)
	first := "   " + faintStyle.Bold(true).Render(r.name) + "   " + mutedStyle.Render(meta)
	out := []string{fit(first, width)}
	// pathW is measured over the file lines drawn below, then held for all of
	// them.
	pathW := 46
	grow := func(path string) {
		if w := lipgloss.Width(path) + 2; w > pathW {
			pathW = w
		}
	}
	if r.source == "custom" && !relevo.IsSourceAgent(doc.Agents, r.name) {
		rows := customAgentRows(doc, r.name)
		for _, f := range rows {
			grow(tildePath(f.path))
		}
		for _, f := range rows {
			out = append(out, fit(agentFileLine(f.kind, tildePath(f.path), "", "", pathW), width))
		}
		return out
	}
	for _, f := range r.files {
		grow(tildePath(f.Path))
	}
	for _, f := range r.files {
		model := ""
		if f.Model != "" {
			model = f.Model
		}
		out = append(out, fit(agentFileLine(f.Kind, tildePath(f.Path), string(f.State), model, pathW), width))
	}
	return out
}

// bodyLines is the whole body before it is windowed: a blank line under the
// context row, the table, two blank lines and the cursor row's detail block
// (§4).
func (v agentsView) bodyLines(env Env, width int) []string {
	rows := v.rows()
	nameW, cols := agentListLayout(width, agentKinds(v.files))
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	lines := []string{""}
	lines = append(lines, agentHeaderLine(nameW, cols, cw))
	cur := candClamp(v.cur, len(rows))
	for i, r := range rows {
		lines = append(lines, agentDataLine(r, i == cur, nameW, cols, cw))
	}
	if len(rows) == 0 {
		return lines
	}
	lines = append(lines, "", "")
	return append(lines, agentDetailLines(v.doc, rows[cur], width)...)
}

// rows is the table for the newest load (§4).
func (v agentsView) rows() []agentRow { return agentRows(v.doc, v.files) }

// follow scrolls the page so the cursor's table row stays visible (§4).
func (v *agentsView) follow(env Env) {
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

func (v agentsView) Crumbs() []string { return []string{"agents"} }

// Capturing is always false: the view owns no text input of its own (§4).
func (v agentsView) Capturing() bool { return false }

// Keys are the list's keys (§4), shown only when the shell has an Actions
// seam. `d delete` is listed even on a shipped row: the footer has no dimmed
// key (§9), so a shipped row answers a press with a notice instead.
func (v agentsView) Keys() []KeyHelp {
	if !v.actions {
		return nil
	}
	return []KeyHelp{
		{"↑↓", "move"},
		{"enter", "open"},
		{"d", "delete"},
	}
}

// HelpKeys is the help overlay's key list (§2.2): the same set.
func (v agentsView) HelpKeys() []KeyHelp { return v.Keys() }

// Context is the counts line on the left (§4): the agents, the writer and
// reader counts, and the custom count when there is one.
func (v agentsView) Context(env Env) (string, string) {
	rows := v.rows()
	writers, readers, custom := 0, 0, 0
	for _, r := range rows {
		if r.shape == string(agentsrc.ShapeWriter) {
			writers++
		} else {
			readers++
		}
		if r.source == "custom" {
			custom++
		}
	}
	word := func(n int, label string) string {
		if n != 1 {
			label += "s"
		}
		return textStyle.Bold(true).Render(fmt.Sprintf("%d", n)) + " " + mutedStyle.Render(label)
	}
	left := "   " + word(len(rows), "agent")
	left += "   " + word(writers, "writer")
	left += "   " + word(readers, "reader")
	if custom > 0 {
		left += "   " + textStyle.Bold(true).Render(fmt.Sprintf("%d", custom)) + " " + mutedStyle.Render("custom")
	}
	return left, ""
}

func (v agentsView) Body(env Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if !v.loaded && v.err != nil {
		return strings.Join(statsCentered(v.err.Error(), width, height), "\n")
	}
	if !v.loaded {
		return strings.Join(blockLines([]string{"loading…"}, width, height), "\n")
	}
	if len(v.rows()) == 0 {
		return strings.Join(statsCentered("no agents", width, height), "\n")
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

func (v agentsView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case agentsMsg:
		if msg.err != nil {
			v.err = msg.err
		} else {
			v.doc = msg.doc
			v.files = msg.files
			v.err = nil
			v.loaded = true
		}
		v.cur = candClamp(v.cur, len(v.rows()))
		return v, nil

	case statusMsg:
		// A refresh from anywhere re-reads the doc and the file states,
		// which is what reloads the table after a delete or a reset (§4).
		return v, agentsCmd(env)

	case tea.KeyMsg:
		if !v.actions {
			return v, nil
		}
		return v.updateKey(msg, env)
	}
	return v, nil
}

// updateKey is the list's own keys (§4).
func (v agentsView) updateKey(k tea.KeyMsg, env Env) (View, tea.Cmd) {
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
	case "enter":
		if n == 0 {
			return v, nil
		}
		return v, openAgent(env, v.doc, rows[v.cur])
	case "d":
		if n == 0 {
			return v, nil
		}
		return v, v.deleteCmd(env, rows[v.cur])
	}
	return v, nil
}

// openAgent pushes the cursor agent's detail view (§4).
func openAgent(env Env, doc relevo.ConfigDoc, r agentRow) tea.Cmd {
	v, init := newAgentView(env, doc, r)
	return push(v, init)
}

// deleteCmd is the list's d key (§4): a shipped agent answers with a notice; a
// custom one is validated with relevo.DeleteAgent, whose refusal (a shipped
// agent, or one an actor uses) is a notice too, then confirmed as a danger
// box.
func (v agentsView) deleteCmd(env Env, r agentRow) tea.Cmd {
	if r.source == "shipped" {
		return notice(r.name + " ships with relevo; it can't be deleted")
	}
	edit, err := relevo.DeleteAgent(v.doc, r.name)
	if err != nil {
		return notice(err.Error())
	}
	name := r.name
	return openOverlay(confirmBox{
		kind:   "delete",
		yes:    "delete",
		danger: true,
		title:  "Delete agent " + accentStyle.Bold(true).Render(name) + "?",
		lines: []string{
			pad("used by", 11) + "no actor",
			pad("removes", 11) + "the agent from relevo's config",
		},
		onYes: runAction(env.Ctx, "delete agent", name, func(ctx context.Context) Result {
			return env.Actions.ApplyConfig(ctx, edit)
		}),
	})
}
