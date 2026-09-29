// Package pick is the interactive mode behind `relevo done --pick` and
// `relevo unbind --pick` (#15): a list of bindings, the verb run on the chosen
// one, and the result held on screen until a key. It is built for a terminal
// popup pane, which closes when the command exits.
package pick

import (
	"errors"

	"github.com/fuad-daoud/relevo/internal/view"
)

// Verb is the relevo command a picker runs on the chosen binding.
type Verb string

const (
	VerbDone   Verb = "done"
	VerbUnbind Verb = "unbind"
)

// Options is what the command passes in. Archive is `unbind --archive`; the
// other verbs ignore it.
type Options struct {
	Verb    Verb
	Archive bool
}

// The non-zero outcomes of Run. The picker has already shown the text for
// each on screen, so the command maps all three to a silent exit 1: a
// second "relevo: ..." line would land in the plugin log, not in front of the
// human (spec §3).
var (
	ErrCancelled     = errors.New("cancelled")
	ErrNothingToPick = errors.New("nothing to pick")
	// ErrVerbFailed covers the verb returning an error and the status fetch
	// before the list failing: both end on the result screen with the error
	// shown.
	ErrVerbFailed = errors.New("verb failed")
)

// rowsFor is the spec §4 table: what each verb can act on. done cannot act
// on a DONE binding; unbind clears DONE bindings, so it lists them.
func rowsFor(verb Verb, rep view.Report) []view.BindingStatus {
	switch verb {
	case VerbDone:
		return view.HideDone(rep).Bindings
	default:
		return rep.Bindings
	}
}

// emptyText is the one line shown when rowsFor is empty (spec §4).
func emptyText(verb Verb) string {
	switch verb {
	case VerbDone:
		return "no bindings to mark done"
	default:
		return "nothing bound"
	}
}

// needsConfirm is the #103 rule: done and unbind stop for a `y` before
// acting on any row that is not DONE. A terminal popup takes focus the instant
// it opens, so a keystroke already in flight lands on it -- and the most
// common such key is Enter. The list showed the row's state; nobody had
// read it yet. DONE rows under unbind are what gc clears anyway and run at
// once.
func needsConfirm(verb Verb, r view.BindingStatus) bool {
	switch verb {
	case VerbDone, VerbUnbind:
		return r.Display != "DONE"
	default:
		return false
	}
}
