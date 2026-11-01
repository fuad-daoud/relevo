package widget

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

// HelpKeyer is optional: a view that has it gives the help overlay an
// extended key list.
type HelpKeyer interface{ HelpKeys() []KeyHelp }

// OffKeyer names which of a view's Keys do not apply right now: the footer
// draws those greyed out. A view without it has no off keys.
type OffKeyer interface{ OffKeys(env Env) []string }

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
	// view can compute the body height it will be given (BodyHeight).
	ErrRows int
	// Actions is the shell's write seam. Nil (serve ui) means the
	// action keys do nothing.
	Actions Actions
	// Running is the shell's in-flight actions, keyed by binding key:
	// a second action on the same binding is refused.
	Running map[string]string
	// ActionLog is the session's cockpit actions, oldest first (:log).
	ActionLog []ActionEntry
}

type ActionEntry struct {
	At   time.Time // when the result arrived (the shell's clock)
	Verb string    // the action's verb
	Key  string    // the binding key acted on (may be "")
	Text string    // the result text, or the error text
	Err  bool
}

// Stack messages. A view returns these as commands; only the shell acts on
// them.
type PushMsg struct {
	View View
	Init tea.Cmd
}                    // push View on top
type PopMsg struct{} // pop the top view (no-op at depth 1)
type RootMsg struct {
	Views []View
	Init  tea.Cmd
}                                        // replace the whole stack (a ':' command); len(Views) >= 1
type NoticeMsg struct{ Text string }     // set the sticky footer notice
type PrefMsg struct{ Key, Value string } // Key is one of "sort", "dashboard", "dashboard_sort"

// HelpMsg opens the help overlay; the ':' command line returns it for the
// `help` command, which cannot reach the shell's own field.
type HelpMsg struct{}

// LogMsg opens the ':log' view: the shell's own session scrollback of action
// results, which the ':' command line cannot reach.
type LogMsg struct{}
