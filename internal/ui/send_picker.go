package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
	"github.com/fuad-daoud/relevo/internal/view"
)

type planFile struct {
	path string
	mod  time.Time
}

// recentPlans lists up to n *.md files in dir, newest first by modification
// time, filtered to names containing filter (case-insensitive substring).
// Pure apart from the directory read.
func recentPlans(dir, filter string, n int) []planFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	needle := strings.ToLower(filter)
	var out []planFile
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(name), needle) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, planFile{path: filepath.Join(dir, name), mod: info.ModTime()})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].mod.After(out[j].mod) })
	if n >= 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// sendPicker is the s key's plan picker.
type sendPicker struct {
	env   widget.Env
	b     view.BindingStatus
	input textinput.Model

	// root is the binding's tree (b.CWD): relative typed values resolve
	// against it. "" means values are used as typed.
	root string

	pick int // the selected list row, -1 when the typed value is used

	base    string // the value the first tab completed from
	compSel int
	tabbed  bool

	err string
}

func (p sendPicker) resolve(v string) string {
	if v == "" || filepath.IsAbs(v) || p.root == "" {
		return v
	}
	return filepath.Join(p.root, v)
}

func (p sendPicker) keys() []widget.KeyHelp {
	return []widget.KeyHelp{
		{Key: "↑↓", Help: "choose"},
		{Key: "tab", Help: "complete"},
		{Key: "enter", Help: "choose"},
		{Key: "ctrl+e", Help: "$EDITOR"},
		{Key: "esc", Help: "cancel"},
	}
}

func (p sendPicker) update(k tea.KeyMsg) (overlay, tea.Cmd, bool) {
	switch k.String() {
	case "esc":
		return p, nil, true
	case "ctrl+e":
		return p, editorCmd(p.env, p.b), true
	case "up", "ctrl+p":
		p.pick = p.step(-1)
		return p, nil, false
	case "down", "ctrl+n":
		p.pick = p.step(1)
		return p, nil, false
	case "tab", "shift+tab", "back_tab":
		return p.completeTab(), nil, false
	case "enter":
		return p.enter()
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(k)
	// A new value restarts the completion and drops the list selection.
	p.base, p.tabbed = "", false
	p.pick = -1
	return p, cmd, false
}

// listDir is the directory the list reads: the resolved directory part of the
// typed value, or the plan directory's own path when the value has no
// directory.
func (p sendPicker) listDir() string {
	v := p.input.Value()
	if strings.ContainsRune(v, filepath.Separator) {
		return filepath.Dir(p.resolve(v))
	}
	return p.resolve(plansDir(p.b))
}

// filter is the typed base name the list narrows to, "" when none is typed.
func (p sendPicker) filter() string {
	v := p.input.Value()
	if v == "" || strings.HasSuffix(v, string(filepath.Separator)) {
		return ""
	}
	return filepath.Base(v)
}

func (p sendPicker) plans() []planFile {
	dir := p.listDir()
	if dir == "" {
		return nil
	}
	return recentPlans(dir, p.filter(), 6)
}

// step moves the list selection by dir, skipping nothing (the rows are files),
// wrapping at the ends.
func (p sendPicker) step(dir int) int {
	n := len(p.plans())
	if n == 0 {
		return -1
	}
	if p.pick < 0 || p.pick >= n {
		if dir < 0 {
			return n - 1
		}
		return 0
	}
	return (p.pick + dir + n) % n
}

func (p sendPicker) completeTab() sendPicker {
	if !p.tabbed {
		p.base = p.input.Value()
		p.compSel = 0
	}
	matches := pathCompletion(p.resolve(p.base))
	if len(matches) == 0 {
		return p
	}
	p.tabbed = true
	p.compSel %= len(matches)
	p.input.SetValue(matches[p.compSel])
	p.input.CursorEnd()
	p.compSel = (p.compSel + 1) % len(matches)
	p.pick = -1
	return p
}

func (p sendPicker) enter() (overlay, tea.Cmd, bool) {
	if list := p.plans(); p.pick >= 0 && p.pick < len(list) {
		p.input.SetValue(list[p.pick].path)
		p.input.CursorEnd()
	}
	file := strings.TrimSpace(p.input.Value())
	if file == "" {
		p.err = "plan file is required"
		return p, nil, false
	}
	resolved := p.resolve(file)
	info, err := os.Stat(resolved)
	if err != nil {
		p.err = err.Error()
		return p, nil, false
	}
	if info.IsDir() {
		p.err = fmt.Sprintf("%s is a directory", file)
		return p, nil, false
	}
	p.err = ""
	key := p.b.Key()
	return p, openOverlay(confirmBox{
		kind:   "send",
		yes:    "send the plan",
		danger: false,
		title:  sendConfirmTitle(p.b, file),
		lines:  sendConfirmLines(p.b),
		onYes: runAction(p.env.Ctx, "send", key, func(ctx context.Context) widget.Result {
			return p.env.Actions.Send(ctx, key, resolved)
		}),
	}), true
}

func (p sendPicker) view(width int) []string {
	_, rows, _, _ := p.modal(width)
	return rows
}

func (p sendPicker) modal(width int) (string, []string, int, bool) {
	const want = 80
	innerW := modalInnerW(want, width)

	header := widget.AccentStyle.Bold(true).Render("Send a plan to "+p.b.Key()) +
		widget.FaintStyle.Render(fmt.Sprintf("  as round %d · on %s", p.b.Round, candidateText(p.b)))

	rows := []string{
		header,
		"",
		widget.FaintStyle.Render(widget.Pad("plan file", 10)) + p.input.View(),
		"",
	}
	for i, pf := range p.plans() {
		note := ""
		if age := ago(pf.mod, p.env.Now); age != "" {
			note = age + " ago"
		}
		rows = append(rows, modalRow(i == p.pick, filepath.Base(pf.path), "", note, innerW, widget.TextStyle, widget.MutedStyle))
	}
	rows = append(rows, "")
	rows = append(rows, widget.FaintStyle.Render("ctrl+e  write it in $EDITOR instead"))
	if p.err != "" {
		rows = append(rows, widget.ErrorStyle.Render(p.err))
	}
	rows = append(rows, "")
	rows = append(rows, widget.Chip(widget.KbdStyle, "enter")+widget.MutedStyle.Render(" choose")+
		"   "+widget.Chip(widget.KbdStyle, "tab")+widget.MutedStyle.Render(" complete")+
		"   "+widget.Chip(widget.KbdStyle, "esc")+widget.MutedStyle.Render(" cancel"))
	return "send", rows, want, false
}
