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
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

// serverNoProbe is the state cell's placeholder: the row exists but the probe
// that fills it has not answered yet.
const serverNoProbe = "—"

type serversDocMsg struct {
	doc relevo.ConfigDoc
	err error
}

// serversProbeMsg is one probe command's reply: every server's health, in name
// order.
type serversProbeMsg struct {
	probes []relevo.ServerProbe
}

// serversView is ':servers': one row per configured server. The probe is
// network work, so it only ever runs in a tea.Cmd, never while a frame is
// drawn.
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
func newServersView(env widget.Env) (widget.View, tea.Cmd) {
	v := serversView{actions: env.Actions != nil}
	return v, serversDocCmd(env)
}

func serversDocCmd(env widget.Env) tea.Cmd {
	return func() tea.Msg {
		doc, err := env.Actions.ConfigDoc()
		return serversDocMsg{doc: doc, err: err}
	}
}

func serversProbeCmd(env widget.Env) tea.Cmd {
	return func() tea.Msg {
		if env.Actions == nil {
			return serversProbeMsg{}
		}
		return serversProbeMsg{probes: env.Actions.ServerProbes(env.Ctx)}
	}
}

func serverNames(servers map[string]remote.ServerEntry) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// serverRows is the table: one row per configured server, or the placeholder
// before a probe answers.
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

func clipMiddle(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func serverStateStyle(state string) lipgloss.Style {
	switch {
	case state == serverNoProbe:
		return widget.FaintStyle
	case strings.HasPrefix(state, "enrolled"):
		return widget.GreenStyle
	case state == "not enrolled", state == "no key", state == "cert changed":
		return widget.WarnStyle
	default:
		return widget.RedStyle
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

func serverHeaderLine(nameW int, cols []candCol, cw int) string {
	style := widget.FaintStyle.Bold(true)
	cells := []candCell{{text: widget.Pad("SERVER", nameW), style: style}}
	for _, c := range cols {
		cells = append(cells, candCell{text: widget.Pad(c.head, c.w), style: style})
	}
	return candLine(cells, false, cw)
}

func serverDataLine(r serverRow, sel bool, nameW int, cols []candCol, cw int) string {
	nameStyle := widget.TextStyle
	if sel {
		nameStyle = nameStyle.Bold(true)
	}
	cells := []candCell{{text: widget.Pad(r.name, nameW), style: nameStyle}}
	for _, col := range cols {
		text, style := "", widget.MutedStyle
		switch col.key {
		case "url":
			text = r.url
		case "fingerprint":
			text = r.fingerprint
		case "state":
			text, style = r.state, serverStateStyle(r.state)
		}
		cells = append(cells, candCell{text: widget.Pad(text, col.w), style: style})
	}
	return candLine(cells, sel, cw)
}

// serverDetailLines is the cursor row's detail block: the full url and pin,
// then the probe's own detail line.
func serverDetailLines(r serverRow, width int) []string {
	meta := r.url
	if r.full != "" {
		meta += " · " + r.full
	}
	if r.insecure {
		meta += " · insecure"
	}
	first := "   " + widget.FaintStyle.Bold(true).Render(r.name) + "   " + widget.MutedStyle.Render(meta)
	out := []string{widget.Fit(first, width)}
	if r.detail != "" {
		out = append(out, widget.Fit("   "+widget.MutedStyle.Render(r.detail), width))
	}
	return out
}

func (v serversView) bodyLines(env widget.Env, width int) []string {
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

func (v serversView) rows() []serverRow { return serverRows(v.doc, v.probes) }

func (v *serversView) follow(env widget.Env) {
	rows := v.rows()
	if len(rows) == 0 {
		v.top = 0
		return
	}
	avail := widget.BodyHeight(env)
	if avail <= 0 {
		v.top = 0
		return
	}
	v.top = followCursor(v.top, v.cur, len(v.bodyLines(env, env.Width)), avail)
}

func (v serversView) Crumbs() []string { return []string{"servers"} }

func (v serversView) Capturing() bool { return false }

// Keys are the list's keys, shown only with a widget.Actions seam; without
// one the probe and every edit stay hidden.
func (v serversView) Keys() []widget.KeyHelp {
	if !v.actions {
		return nil
	}
	return []widget.KeyHelp{
		{Key: "↑↓", Help: "move"},
		{Key: "r", Help: "probe"},
		{Key: "a", Help: "add"},
		{Key: "e", Help: "edit"},
		{Key: "d", Help: "delete"},
	}
}

func (v serversView) HelpKeys() []widget.KeyHelp { return v.Keys() }

func (v serversView) Context(env widget.Env) (string, string) {
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
		return widget.TextStyle.Bold(true).Render(fmt.Sprintf("%d", n)) + " " + widget.MutedStyle.Render(label)
	}
	left := "   " + word(len(rows), "server")
	if len(rows) > 0 {
		left += "   " + word(enrolled, "enrolled")
	}
	return left, ""
}

func (v serversView) Body(env widget.Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if !v.loaded && v.err != nil {
		return strings.Join(widget.Centered(v.err.Error(), width, height), "\n")
	}
	if !v.loaded {
		return strings.Join(widget.BlockLines([]string{"loading…"}, width, height), "\n")
	}
	if len(v.rows()) == 0 {
		return strings.Join(widget.Centered("no servers configured; relevo config server add <name> <url>", width, height), "\n")
	}
	lines := v.bodyLines(env, width)
	top := widget.Clamp(v.top, 0, max(0, len(lines)-height))
	if top > len(lines) {
		top = len(lines)
	}
	end := top + height
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(widget.FitLines(lines[top:end], width, height), "\n")
}

func (v serversView) Update(msg tea.Msg, env widget.Env) (widget.View, tea.Cmd) {
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

func (v serversView) updateKey(k tea.KeyMsg, env widget.Env) (widget.View, tea.Cmd) {
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
		v.cur = candClamp(v.cur-widget.BodyHeight(env), n)
		v.follow(env)
	case "pgdown":
		v.cur = candClamp(v.cur+widget.BodyHeight(env), n)
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

// deleteCmd validates the delete, then confirms it as a danger box; a refused
// delete (an actor places its rounds on the server) is a notice naming the
// actor.
func (v serversView) deleteCmd(env widget.Env, r serverRow) tea.Cmd {
	edit, err := relevo.DeleteServer(v.doc, r.name)
	if err != nil {
		return notice(err.Error())
	}
	return openOverlay(confirmBox{
		kind:   "delete",
		yes:    "delete",
		danger: true,
		title:  "Delete server " + widget.AccentStyle.Bold(true).Render(r.name) + "?",
		lines: []string{
			widget.Pad("url", 11) + r.url,
			widget.Pad("removed", 11) + "the server from relevo's config",
			widget.Pad("kept", 11) + "this client's key",
		},
		onYes: runAction(env.Ctx, "delete server", r.name, func(ctx context.Context) widget.Result {
			return env.Actions.ApplyConfig(ctx, edit)
		}),
	})
}
