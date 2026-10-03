package ui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// chainTraceMsg is one chain's trace, read off the update loop.
type chainTraceMsg struct {
	name string
	doc  relevo.ChainTraceDoc
	err  error
}

// chainTraceView is one chain's trace as a read-only scroll: the same
// rendered lines `relevo show <chain> --trace` prints, one per event, with
// no key but scrolling and esc.
type chainTraceView struct {
	name   string
	lines  []string
	err    error
	loaded bool
	vp     viewport.Model
	// crumbs replaces the default chain-and-trace breadcrumb when the trace was
	// reached through a view that already names the chain, so the trail reads
	// chains › chain › trace instead of naming the chain twice.
	crumbs []string
}

// newChainTraceView pushes a chain's trace and the read that fills it. Crumbs
// are the trail this view contributes on its own: the chain's name plus trace
// when it was pushed straight from the chains list, and the segments the caller
// supplies when a view below it already names the chain.
func newChainTraceView(env Env, name string, crumbs ...string) View {
	v := chainTraceView{name: name, vp: viewport.New(maxInt(env.Width-6, 20), maxInt(bodyHeight(env), 1))}
	v.crumbs = crumbs
	return v
}

// maxInt is the larger of two ints.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// chainTraceCmd reads the trace off the update loop.
func chainTraceCmd(env Env, name string) tea.Cmd {
	return func() tea.Msg {
		if env.Actions == nil {
			return chainTraceMsg{name: name}
		}
		doc, err := env.Actions.ChainTrace(env.Ctx, name)
		return chainTraceMsg{name: name, doc: doc, err: err}
	}
}

// Crumbs is the trail this view contributes: the chain and its trace when the
// chains list pushed it, and the caller's own segments when the step list did,
// since that view already contributes the chain's name.
func (v chainTraceView) Crumbs() []string {
	if len(v.crumbs) > 0 {
		return v.crumbs
	}
	return []string{v.name, "trace"}
}

func (v chainTraceView) Capturing() bool { return false }

func (v chainTraceView) Keys() []KeyHelp {
	return []KeyHelp{
		{"↑↓", "scroll"},
	}
}

func (v chainTraceView) HelpKeys() []KeyHelp { return v.Keys() }

// Context is the trace context row: how many events the trace holds, or the
// reason it holds none.
func (v chainTraceView) Context(env Env) (string, string) {
	if v.err != nil && !v.loaded {
		return "   " + errorStyle.Render(sanitizeText(v.err.Error())), ""
	}
	if !v.loaded {
		return "   " + mutedStyle.Render("loading…"), ""
	}
	n := len(v.lines)
	left := "   " + textStyle.Bold(true).Render(strconv.Itoa(n)) + " " + mutedStyle.Render("events")
	if v.err != nil {
		return left, warnStyle.Render("stale: "+sanitizeText(v.err.Error())) + "   "
	}
	return left, ""
}

// Body is the scrolled trace, sized to the shell's body box.
func (v chainTraceView) Body(env Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if v.err != nil && !v.loaded {
		return strings.Join(statsCentered(sanitizeText(v.err.Error()), width, height), "\n")
	}
	if !v.loaded {
		return strings.Join(blockLines([]string{"loading…"}, width, height), "\n")
	}
	if len(v.lines) == 0 {
		return strings.Join(statsCentered("no trace: this chain has no events", width, height), "\n")
	}
	v.vp.Width = maxInt(width-6, 20)
	v.vp.Height = height
	body := fitLines(strings.Split(v.vp.View(), "\n"), width-6, height)
	for i, l := range body {
		body[i] = "   " + l
	}
	return strings.Join(body, "\n")
}

func (v chainTraceView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case chainTraceMsg:
		if msg.name != v.name {
			return v, nil
		}
		if msg.err != nil {
			v.err = msg.err
			return v, nil
		}
		v.err = nil
		v.loaded = true
		v.lines = sanitizeLines(strings.Split(strings.TrimRight(relevo.RenderTrace(msg.doc), "\n"), "\n"))
		v.vp.SetContent(wrapBody(strings.Join(v.lines, "\n"), v.vp.Width))
		return v, nil

	case tea.WindowSizeMsg:
		v.vp.Width = maxInt(env.Width-6, 20)
		v.vp.Height = maxInt(bodyHeight(env), 1)
		return v, nil

	case tea.KeyMsg:
		if k := msg.String(); k == "esc" {
			return v, pop()
		}
		var cmd tea.Cmd
		v.vp, cmd = v.vp.Update(msg)
		return v, cmd
	}
	return v, nil
}

// sanitizeLines makes every trace line safe to draw. A trace row carries a
// member name, a step and a reason the human wrote, so it is untrusted text
// like any other.
func sanitizeLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = sanitizeText(l)
	}
	return out
}
