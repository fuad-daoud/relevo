package git

import (
	"github.com/fuad-daoud/relevo/internal/store"
)

// EscapeOutcome is what a headless round's close decided about worktree
// escape: a headless builder that ran its shell somewhere else and
// executed the round against a directory relevo never gave it, leaving its
// own worktree untouched while the source repo it actually worked in
// changed.
type EscapeOutcome int

const (
	EscapeNone EscapeOutcome = iota // nothing to say
	EscapeNote                      // annotate the report entry with "escaped"
	EscapeHalt                      // halt NEEDS YOU instead of switching
)

const EscapedNote = "escaped"

// DecideEscape is the pure decision behind escapeCheck.
//
//   - !treeUnchanged || !repoMoved: nothing suspicious -- EscapeNone.
//   - treeUnchanged && repoMoved && hasReport: the builder still produced a
//     report, so the round closes normally with an "escaped" note.
//   - treeUnchanged && repoMoved && !hasReport: no report and no sign the
//     builder touched its own tree while the source repo moved -- halt
//     rather than switch, since switching would dispatch a new builder into
//     the same broken setup.
func DecideEscape(treeUnchanged, repoMoved, hasReport bool) EscapeOutcome {
	if !treeUnchanged || !repoMoved {
		return EscapeNone
	}
	if hasReport {
		return EscapeNote
	}
	return EscapeHalt
}

// EscapeApplies is the pure precondition behind escapeCheck: does this
// binding have the two directories the check compares?
//   - !b.Builder.Headless()      -> false (pane builders are out of scope)
//   - b.Repo == ""               -> false
//   - b.RoundBaselineTree == ""  -> false
//   - b.Serve != nil             -> false (served binding: b.Repo is the bare
//     mirror, with no source checkout to escape into and no `git status` in a
//     bare repo)
//   - otherwise                  -> true
func EscapeApplies(b store.Binding) bool {
	if b.Shape == store.ShapeReader {
		// A reader never touches b.CWD: it runs in a scratch copy, so a dirty
		// binding tree next to it says nothing about an escape.
		return false
	}
	if !b.Builder.Headless() {
		return false
	}
	if b.Repo == "" {
		return false
	}
	if b.RoundBaselineTree == "" {
		return false
	}
	if b.Serve != nil {
		return false
	}
	return true
}
