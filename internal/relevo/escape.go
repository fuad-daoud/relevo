package relevo

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/store"
)

// EscapeOutcome is what a headless round's close decided about worktree
// escape (#192): a headless builder that ran its shell somewhere else and
// executed the round against a directory relevo never gave it, leaving its
// own worktree untouched while the source repo it actually worked in
// changed.
type EscapeOutcome int

const (
	EscapeNone EscapeOutcome = iota // nothing to say
	EscapeNote                      // annotate the report entry with "escaped"
	EscapeHalt                      // halt NEEDS YOU instead of switching
)

// escapeNote is the report-entry annotation an EscapeNote outcome adds.
const escapeNote = "escaped"

// escapeOutcome is the pure decision behind escapeCheck: given what the
// round's worktree and source repo look like at close, and whether a report
// was produced, what should relevo say about it.
//
//   - !treeUnchanged || !repoMoved: nothing suspicious -- EscapeNone.
//   - treeUnchanged && repoMoved && hasReport: the builder still produced a
//     report, so the round closes normally with an "escaped" note.
//   - treeUnchanged && repoMoved && !hasReport: no report and no sign the
//     builder touched its own tree while the source repo moved -- halt
//     rather than switch, since switching would dispatch a new builder into
//     the same broken setup.
func escapeOutcome(treeUnchanged, repoMoved, hasReport bool) EscapeOutcome {
	if !treeUnchanged || !repoMoved {
		return EscapeNone
	}
	if hasReport {
		return EscapeNote
	}
	return EscapeHalt
}

// escapeApplies is the pure precondition behind escapeCheck: does this
// binding have the two directories the check compares?
//   - !b.Builder.Headless()      -> false (pane builders are out of scope, #192)
//   - b.Repo == ""               -> false
//   - b.RoundBaselineTree == ""  -> false
//   - b.Serve != nil             -> false (served binding: b.Repo is the bare
//     mirror, there is no source checkout to
//     escape into and `git status` cannot run
//     in a bare repo)
//   - otherwise                  -> true
func escapeApplies(b store.Binding) bool {
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

// escapeCheck gathers escapeOutcome's inputs and runs it, for one headless
// round close (#192). It never fails the close it is called from: any git
// error is a skipped check (EscapeNone), logged at Warn, not a reason to
// hold up the round.
//
// Preconditions for running at all -- otherwise EscapeNone without touching
// git: b.Builder.Headless(), b.Repo != "", b.RoundBaselineTree != "",
// b.Serve == nil (served bindings use a bare mirror as b.Repo; git status
// cannot run in a bare repo and there is no source checkout to escape into),
// and rt.Git != nil.
func escapeCheck(ctx context.Context, rt Runtime, b store.Binding, hasReport bool) EscapeOutcome {
	if !escapeApplies(b) || rt.Git == nil {
		return EscapeNone
	}

	tree, err := rt.Git.SnapshotTree(ctx, b.CWD)
	if err != nil {
		slog.Warn("escape check skipped", "binding", b.Name, "round", b.Round, "err", err)
		return EscapeNone
	}
	treeUnchanged := tree == b.RoundBaselineTree

	moved, err := repoMoved(ctx, rt, b)
	if err != nil {
		slog.Warn("escape check skipped", "binding", b.Name, "round", b.Round, "err", err)
		return EscapeNone
	}

	outcome := escapeOutcome(treeUnchanged, moved, hasReport)
	if outcome != EscapeNone {
		slog.Warn("worktree escape suspected", "binding", b.Name, "round", b.Round, "worktree", b.CWD, "repo", b.Repo)
	}
	return outcome
}

// repoMoved reports whether the source checkout changed during the round. A
// repo the human left dirty before the send is the normal state for a round
// that fails fast, so only a change against the send's snapshot counts; a
// round sent without one falls back to the plain dirty check.
func repoMoved(ctx context.Context, rt Runtime, b store.Binding) (bool, error) {
	if b.RoundRepoBaselineTree == "" {
		return rt.Git.Dirty(ctx, b.Repo)
	}
	tree, err := rt.Git.SnapshotTree(ctx, b.Repo)
	if err != nil {
		return false, err
	}
	return tree != b.RoundRepoBaselineTree, nil
}

// escapeRepoBaseline snapshots b's source checkout at send for repoMoved, or
// returns "" when the escape check will not run on this binding. A failed
// snapshot is "" too: the close then falls back to the dirty check rather
// than holding up the send.
//
// tree is the worktree's own baseline, taken at the same send: a binding that
// runs in the checkout itself has one tree, so it is not snapshotted twice.
func escapeRepoBaseline(ctx context.Context, rt Runtime, b store.Binding, tree string) string {
	b.RoundBaselineTree = tree
	if !escapeApplies(b) || rt.Git == nil {
		return ""
	}
	if b.Repo == b.CWD {
		return tree
	}
	tree, err := rt.Git.SnapshotTree(ctx, b.Repo)
	if err != nil {
		slog.Warn("escape baseline skipped", "binding", b.Name, "err", err)
		return ""
	}
	return tree
}

// escapeDiagnosis is the halt message for an EscapeHalt outcome: it names
// both directories and the transcript command, so the human reading NEEDS YOU
// has enough to go straight to the evidence.
func escapeDiagnosis(b store.Binding, codeText string) string {
	return fmt.Sprintf(
		"%s: builder exited (code %s) without a report; worktree %s unchanged since the round began while %s changed -- the builder likely worked outside its tree; see %s",
		b.Name, codeText, b.CWD, b.Repo, showCommand(b.Name, b.Round, "transcript"))
}
