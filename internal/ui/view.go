package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/view"
)

// View is one screen of the cockpit. Views are values: Update returns the
// next value.
type View interface {
	Crumbs() []string                     // breadcrumb segments this view adds, e.g. {"webshop", "r4"}; never empty
	Context(env Env) (left, right string) // row 2, styled; each side may be ""
	Keys() []KeyHelp                      // this view's keys, in footer order (the shell appends the global ones)
	Capturing() bool                      // true while the view owns every key (an open text input)
	Update(msg tea.Msg, env Env) (View, tea.Cmd)
	Body(env Env, width, height int) string // exactly height lines, none wider than width
}

// KeyHelp is one key's footer hint. Key is as displayed.
type KeyHelp struct{ Key, Help string }

// helpKeyer is optionally implemented by a view that provides an extended
// key list for the help overlay (§2.2, §4).
type helpKeyer interface{ HelpKeys() []KeyHelp }

// offKeyer is optionally implemented by a view that can name which of its
// Keys do not apply right now: the footer draws those greyed out. A view
// without it has no off keys.
type offKeyer interface{ OffKeys(env Env) []string }

// Env is what the shell lends a view on every call. Views never keep it.
type Env struct {
	Ctx      context.Context
	Src      Source
	Report   view.Report // newest good status (zero before the first)
	Loaded   bool        // a status has arrived at least once
	StatusAt time.Time   // when it arrived
	Now      time.Time   // the shell's clock, read once per call
	Width    int         // terminal columns
	Height   int         // terminal rows
	// ErrRows is the line count of the shell's error block this frame, so a
	// view can compute the body height it will be given (bodyHeight).
	ErrRows int
	// Actions is the shell's write seam (§4.3). Nil (serve ui) means the
	// action keys do nothing.
	Actions Actions
	// Running is the shell's in-flight actions, keyed by binding key (§4.3):
	// a second action on the same binding is refused.
	Running map[string]string
	// ActionLog is the session's cockpit actions, oldest first (:log).
	ActionLog []actionEntry
}

// Stack messages. A view returns these as commands; only the shell acts on
// them.
type pushMsg struct {
	v    View
	init tea.Cmd
}                    // push v on top
type popMsg struct{} // pop the top view (no-op at depth 1)
type rootMsg struct {
	vs   []View
	init tea.Cmd
}                                        // replace the whole stack (a ':' command); len(vs) >= 1
type noticeMsg struct{ text string }     // set the sticky footer notice
type prefMsg struct{ key, value string } // key is one of "sort", "dashboard", "dashboard_sort"

// helpMsg opens the help overlay; the ':' command line returns it for the
// `help` command, which cannot reach the shell's own field.
type helpMsg struct{}

// logMsg opens the ':log' view: the shell's own session scrollback of action
// results, which the ':' command line cannot reach (§4.3).
type logMsg struct{}

// push returns a command that pushes v and then runs init.
func push(v View, init tea.Cmd) tea.Cmd {
	return func() tea.Msg { return pushMsg{v, init} }
}

// pop returns a command that pops the top view.
func pop() tea.Cmd { return func() tea.Msg { return popMsg{} } }

// root returns a command that replaces the whole stack.
func root(vs ...View) tea.Cmd { return rootThen(nil, vs...) }

// rootThen returns a command that replaces the whole stack and then runs init.
func rootThen(init tea.Cmd, vs ...View) tea.Cmd {
	return func() tea.Msg { return rootMsg{vs, init} }
}

// notice returns a command that sets the sticky footer notice.
func notice(text string) tea.Cmd { return func() tea.Msg { return noticeMsg{text} } }
