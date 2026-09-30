package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
)

// serverNoProbe is the state cell's placeholder: the row exists but the probe
// that fills it has not answered yet.
const serverNoProbe = "—"

// serversDocMsg is one servers load's reply: the doc, whose Servers map is the
// table's rows.
type serversDocMsg struct {
	doc relevo.ConfigDoc
	err error
}

// serversProbeMsg is one probe command's reply: every server's health, in name
// order.
type serversProbeMsg struct {
	probes []relevo.ServerProbe
}

// serversView is ':servers': one row per configured server, its state from an
// on-demand probe, and the a/e/d keys. The probe is network work, so it only
// ever runs in a tea.Cmd, never while a frame is drawn.
type serversView struct {
	doc     relevo.ConfigDoc
	probes  map[string]relevo.ServerProbe // by server name; nil until a probe answers
	err     error                         // the last ConfigDoc error
	loaded  bool
	cur     int // cursor over rows()
	top     int // first body line shown (page follows the cursor)
	actions bool
}

// serverRow is one table row, computed per render, never stored.
type serverRow struct {
	name        string
	url         string
	fingerprint string // abbreviated for the table
	full        string // the stored fingerprint, verbatim
	ca          string
	insecure    bool
	state       string
	detail      string // the probe's own detail line, "" when it has none
}

// newServersView builds ':servers' and the command that loads its doc. The doc
// command returns the probe command when it arrives, so the rows render with
// the no-probe placeholder while the network work is still in flight.
// env.Actions must be non-nil: execLine refuses the command otherwise.
func newServersView(env Env) (View, tea.Cmd) {
	v := serversView{actions: env.Actions != nil}
	return v, serversDocCmd(env)
}

// serversDocCmd reads the stored config off the update loop.
func serversDocCmd(env Env) tea.Cmd {
	return func() tea.Msg {
		doc, err := env.Actions.ConfigDoc()
		return serversDocMsg{doc: doc, err: err}
	}
}

// serversProbeCmd probes every configured server off the update loop.
func serversProbeCmd(env Env) tea.Cmd {
	return func() tea.Msg {
		if env.Actions == nil {
			return serversProbeMsg{}
		}
		return serversProbeMsg{probes: env.Actions.ServerProbes(env.Ctx)}
	}
}

// serverNames is the servers section's names, sorted.
func serverNames(servers map[string]remote.ServerEntry) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// serverRows is the table: one row per configured server, its state from the
// probe carrying that name, or the placeholder before a probe answers.
func serverRows(doc relevo.ConfigDoc, probes map[string]relevo.ServerProbe) []serverRow {
	names := serverNames(doc.Servers)
	out := make([]serverRow, 0, len(names))
	for _, name := range names {
		e := doc.Servers[name]
		r := serverRow{
			name:        name,
			url:         e.URL,
			fingerprint: abbrevFingerprint(e.Fingerprint),
			full:        e.Fingerprint,
			ca:          e.CA,
			insecure:    e.Insecure,
			state:       serverNoProbe,
		}
		if p, ok := probes[name]; ok {
			r.state = serverStateText(p)
			r.detail = p.Detail
		}
		out = append(out, r)
	}
	return out
}

// serverStateText is one probe's state in the table's words: an enrolled
// server names the label it answered with, every other state is the probe's
// own word.
func serverStateText(p relevo.ServerProbe) string {
	switch p.State {
	case "enrolled":
		if p.Label == "" {
			return "enrolled"
		}
		return "enrolled as " + p.Label
	case "":
		return serverNoProbe
	}
	return p.State
}

// abbrevFingerprint shortens a stored fingerprint for the table: the scheme
// and the first eight hex digits. An absent pin reads as the placeholder.
func abbrevFingerprint(fp string) string {
	if fp == "" {
		return serverNoProbe
	}
	scheme, hex, pinned := strings.Cut(fp, ":")
	if !pinned {
		return clipMiddle(fp, 12)
	}
	return scheme + ":" + clipMiddle(hex, 8)
}

// clipMiddle trims s to n runes, marking that it was cut.
func clipMiddle(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// serverStateStyle colours one state word: green when the server is enrolled,
// amber for the states the human can fix, red for a failure, faint for the
// placeholder.
func serverStateStyle(state string) lipgloss.Style {
	switch {
	case state == serverNoProbe:
		return faintStyle
	case strings.HasPrefix(state, "enrolled"):
		return greenStyle
	case state == "not enrolled", state == "no key", state == "cert changed":
		return warnStyle
	default:
		return redStyle
	}
}

// serverListLayout is the columns to show at width and the NAME column's room:
// the table spans width-6 cells from column 3, each column followed by two
// spaces, NAME taking the rest. While that would fall under 12 cells, columns
// leave in the order FINGERPRINT, URL.
func serverListLayout(width int) (int, []candCol) {
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	visible := []candCol{
		{"url", "URL", 30},
		{"fingerprint", "FINGERPRINT", 16},
		{"state", "STATE", 22},
	}
	nameW := func(cols []candCol) int {
		fixed := 2 // the two spaces after NAME
		for i, c := range cols {
			fixed += c.w
			if i < len(cols)-1 {
				fixed += 2
			}
		}
		return cw - fixed
	}
	for _, drop := range []string{"fingerprint", "url"} {
		if nameW(visible) >= 12 {
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

// serverHeaderLine is the table's header row, in faint bold.
func serverHeaderLine(nameW int, cols []candCol, cw int) string {
	style := faintStyle.Bold(true)
	cells := []candCell{{text: pad("SERVER", nameW), style: style}}
	for _, c := range cols {
		cells = append(cells, candCell{text: pad(c.head, c.w), style: style})
	}
	return candLine(cells, false, cw)
}

// serverDataLine is one server's table row: the name (bold on the cursor row),
// the url and the abbreviated fingerprint in muted, and the state in its own
// colour.
func serverDataLine(r serverRow, sel bool, nameW int, cols []candCol, cw int) string {
	nameStyle := textStyle
	if sel {
		nameStyle = nameStyle.Bold(true)
	}
	cells := []candCell{{text: pad(r.name, nameW), style: nameStyle}}
	for _, col := range cols {
		text, style := "", mutedStyle
		switch col.key {
		case "url":
			text = r.url
		case "fingerprint":
			text = r.fingerprint
		case "state":
			text, style = r.state, serverStateStyle(r.state)
		}
		cells = append(cells, candCell{text: pad(text, col.w), style: style})
	}
	return candLine(cells, sel, cw)
}

// serverDetailLines is the cursor row's detail block: the full url and pin,
// then the probe's own detail line (the enrollment line for "not enrolled",
// the failure cause for "unreachable").
func serverDetailLines(r serverRow, width int) []string {
	meta := r.url
	if r.full != "" {
		meta += " · " + r.full
	}
	if r.insecure {
		meta += " · insecure"
	}
	first := "   " + faintStyle.Bold(true).Render(r.name) + "   " + mutedStyle.Render(meta)
	out := []string{fit(first, width)}
	if r.detail != "" {
		out = append(out, fit("   "+mutedStyle.Render(r.detail), width))
	}
	return out
}

// bodyLines is the whole body before it is windowed: a blank line under the
// context row, the table, two blank lines and the cursor row's detail block.
func (v serversView) bodyLines(env Env, width int) []string {
	rows := v.rows()
	nameW, cols := serverListLayout(width)
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	lines := []string{""}
	lines = append(lines, serverHeaderLine(nameW, cols, cw))
	cur := candClamp(v.cur, len(rows))
	for i, r := range rows {
		lines = append(lines, serverDataLine(r, i == cur, nameW, cols, cw))
	}
	if len(rows) == 0 {
		return lines
	}
	lines = append(lines, "", "")
	return append(lines, serverDetailLines(rows[cur], width)...)
}

// rows is the table for the newest load.
func (v serversView) rows() []serverRow { return serverRows(v.doc, v.probes) }

// follow scrolls the page so the cursor's table row stays visible.
func (v *serversView) follow(env Env) {
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

func (v serversView) Crumbs() []string { return []string{"servers"} }

// Capturing is always false: the view owns no text input of its own.
func (v serversView) Capturing() bool { return false }

// Keys are the list's keys, shown only when the shell has an Actions seam:
// without one the probe and every edit are hidden.
func (v serversView) Keys() []KeyHelp {
	if !v.actions {
		return nil
	}
	return []KeyHelp{
		{"↑↓", "move"},
		{"r", "probe"},
		{"a", "add"},
		{"e", "edit"},
		{"d", "delete"},
	}
}

// HelpKeys is the help overlay's key list: the same set.
func (v serversView) HelpKeys() []KeyHelp { return v.Keys() }

// Context is the counts line on the left: the servers, and how many of them
// answered as enrolled.
func (v serversView) Context(env Env) (string, string) {
	rows := v.rows()
	enrolled := 0
	for _, r := range rows {
		if strings.HasPrefix(r.state, "enrolled") {
			enrolled++
		}
	}
	word := func(n int, label string) string {
		if n != 1 {
			label += "s"
		}
		return textStyle.Bold(true).Render(fmt.Sprintf("%d", n)) + " " + mutedStyle.Render(label)
	}
	left := "   " + word(len(rows), "server")
	if len(rows) > 0 {
		left += "   " + word(enrolled, "enrolled")
	}
	return left, ""
}

func (v serversView) Body(env Env, width, height int) string {
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
		return strings.Join(statsCentered("no servers configured; relevo config server add <name> <url>", width, height), "\n")
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

func (v serversView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case serversDocMsg:
		if msg.err != nil {
			v.err = msg.err
			return v, nil
		}
		v.doc = msg.doc
		v.err = nil
		v.loaded = true
		v.cur = candClamp(v.cur, len(v.rows()))
		// The rows exist now; the probe follows, off the update loop.
		return v, serversProbeCmd(env)

	case serversProbeMsg:
		v.probes = make(map[string]relevo.ServerProbe, len(msg.probes))
		for _, p := range msg.probes {
			v.probes[p.Name] = p
		}
		return v, nil

	case statusMsg:
		// A refresh from anywhere re-reads the doc and re-probes, which is
		// what reloads the table after an edit.
		return v, serversDocCmd(env)

	case tea.KeyMsg:
		if !v.actions {
			return v, nil
		}
		return v.updateKey(msg, env)
	}
	return v, nil
}

// updateKey is the list's own keys.
func (v serversView) updateKey(k tea.KeyMsg, env Env) (View, tea.Cmd) {
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
	case "r":
		return v, serversProbeCmd(env)
	case "a":
		return v, openOverlay(newAddServerForm(env, v.doc))
	case "enter", "e":
		if n == 0 {
			return v, nil
		}
		return v, openOverlay(newEditServerForm(env, v.doc, rows[v.cur]))
	case "d":
		if n == 0 {
			return v, nil
		}
		return v, v.deleteCmd(env, rows[v.cur])
	}
	return v, nil
}

// deleteCmd is the list's d key: validate the delete, then confirm it as a
// danger box. A refused delete (an actor places its rounds on the server) is a
// notice naming the actor.
func (v serversView) deleteCmd(env Env, r serverRow) tea.Cmd {
	edit, err := relevo.DeleteServer(v.doc, r.name)
	if err != nil {
		return notice(err.Error())
	}
	return openOverlay(confirmBox{
		kind:   "delete",
		yes:    "delete",
		danger: true,
		title:  "Delete server " + accentStyle.Bold(true).Render(r.name) + "?",
		lines: []string{
			pad("url", 11) + r.url,
			pad("removed", 11) + "the server from relevo's config",
			pad("kept", 11) + "this client's key",
		},
		onYes: runAction(env.Ctx, "delete server", r.name, func(ctx context.Context) Result {
			return env.Actions.ApplyConfig(ctx, edit)
		}),
	})
}
