package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// auditChangeText renders one change line's styled text, cut with an
// ellipsis to width.
func auditChangeText(cl relevo.ChangeLine, width int) string {
	if width < 0 {
		width = 0
	}
	field := ""
	if cl.Field != "" {
		field = dimStyle.Render(" " + cl.Field)
	}
	var b strings.Builder
	switch cl.Op {
	case "~":
		b.WriteString(accentStyle.Render("~ ") + textStyle.Render(cl.Subject) + field)
		b.WriteString("   " + dimStyle.Render(cl.Before) + faintStyle.Render(" → ") + textStyle.Render(cl.After))
	case "+":
		b.WriteString(greenStyle.Render("+ ") + textStyle.Render(cl.Subject) + field)
		b.WriteString("   " + dimStyle.Render(cl.After))
	case "-":
		b.WriteString(redStyle.Render("- ") + textStyle.Render(cl.Subject) + field)
		b.WriteString("   " + dimStyle.Render(cl.Before))
	case "*":
		b.WriteString(dimStyle.Render("* ") + textStyle.Render(cl.Subject))
	default:
		b.WriteString(textStyle.Render(strings.TrimSpace(cl.Op + " " + cl.Subject)))
	}
	return ansi.Truncate(b.String(), width, "…")
}

// auditChangeLine is one change line inside the 3-cell margin.
func auditChangeLine(cl relevo.ChangeLine, width int) string {
	return fit("   "+auditChangeText(cl, width-3), width)
}

// auditRevView is `audit › #N`: every change of one revision, scrollable,
// and the same R key the list has.
type auditRevView struct {
	rev     db.RevisionRow
	newest  int64
	lines   []relevo.ChangeLine
	err     error
	loaded  bool
	top     int
	actions bool
}

// newAuditRevView pushes the cursor revision's changes. cached says the
// list already holds its changes (or their error); when it does not, the view
// asks for them itself.
func newAuditRevView(env Env, rev db.RevisionRow, newest int64, lines []relevo.ChangeLine, err error, cached bool) (View, tea.Cmd) {
	v := auditRevView{
		rev: rev, newest: newest, lines: lines, err: err,
		loaded: cached, actions: env.Actions != nil,
	}
	if cached {
		return v, nil
	}
	return v, auditChangesCmd(env, rev.Rev)
}

func (v auditRevView) Crumbs() []string { return []string{fmt.Sprintf("#%d", v.rev.Rev)} }

// Capturing is always false: the view owns no text input of its own.
func (v auditRevView) Capturing() bool { return false }

// Keys are the detail view's keys: `esc back` is left to the shell's own
// tail, and R needs the shell's write seam.
func (v auditRevView) Keys() []KeyHelp {
	if !v.actions {
		return nil
	}
	return []KeyHelp{{"R", "roll back to here"}}
}

// HelpKeys is the help overlay's key list: the same set plus the
// movement the footer leaves out.
func (v auditRevView) HelpKeys() []KeyHelp {
	return append([]KeyHelp{{"↑↓", "move"}}, v.Keys()...)
}

// Context is the revision's own line.
func (v auditRevView) Context(env Env) (string, string) {
	meta := fmt.Sprintf("%s · %s · %s", v.rev.Source, v.rev.At.Local().Format("Jan 2 15:04"), v.rev.Message)
	return "   " + mutedStyle.Render(meta), ""
}

func (v auditRevView) Body(env Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if !v.loaded {
		return strings.Join(blockLines([]string{"loading…"}, width, height), "\n")
	}
	if v.err != nil {
		return strings.Join(statsCentered(v.err.Error(), width, height), "\n")
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

// bodyLines is the revision's whole body: one line per change, and, for
// the baseline, the one dim line that says what it is.
func (v auditRevView) bodyLines(env Env, width int) []string {
	if v.rev.Rev == 1 {
		return []string{fit("   "+dimStyle.Render("the config as it was when revisions began"), width)}
	}
	lines := []string{""}
	for _, l := range v.lines {
		lines = append(lines, auditChangeLine(l, width))
	}
	return lines
}

func (v auditRevView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case auditChangesMsg:
		if msg.rev != v.rev.Rev {
			return v, nil
		}
		if msg.err != nil {
			v.err = msg.err
		} else {
			v.lines = msg.lines
			v.err = nil
		}
		v.loaded = true
		return v, nil

	case auditPreviewMsg:
		return v, auditPreviewOpen(env, v.newest, msg)

	case statusMsg:
		if env.Actions == nil {
			return v, nil
		}
		return v, auditChangesCmd(env, v.rev.Rev)

	case tea.KeyMsg:
		return v.updateKey(msg, env)
	}
	return v, nil
}

// updateKey is the detail view's own keys: scrolling, and R.
func (v auditRevView) updateKey(k tea.KeyMsg, env Env) (View, tea.Cmd) {
	switch k.String() {
	case "up":
		v.scroll(env, -1)
	case "down":
		v.scroll(env, 1)
	case "pgup":
		v.scroll(env, -bodyHeight(env))
	case "pgdown":
		v.scroll(env, bodyHeight(env))
	case "home":
		v.top = 0
	case "end":
		v.top = v.maxTop(env)
	case "R":
		if !v.actions {
			return v, nil
		}
		return v, auditPreviewCmd(env, v.rev.Rev)
	}
	return v, nil
}

// scroll moves the window by delta body lines, clamped to the body.
func (v *auditRevView) scroll(env Env, delta int) {
	v.top = clamp(v.top+delta, 0, v.maxTop(env))
}

// maxTop is the largest first-line index that still fills the body.
func (v auditRevView) maxTop(env Env) int {
	return max(0, len(v.bodyLines(env, env.Width))-bodyHeight(env))
}
