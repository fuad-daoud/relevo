// Package capture records what a round changed: its starting snapshot, closing
// diff, report-to-send drift and commit facts.
package capture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/text"
)

// Git is the slice of the git CLI capture needs. *git.Client satisfies it.
type Git interface {
	SnapshotTree(ctx context.Context, dir string) (string, error)
	HeadCommit(ctx context.Context, dir string) (string, error)
	DiffTrees(ctx context.Context, dir, from, to string) (git.Diff, error)
	RevListCount(ctx context.Context, dir, from, to string) (int, error)
	Dirty(ctx context.Context, dir string) (bool, error)
}

// Deps is what capture needs from the runtime it runs in: git for the
// comparisons and the store for binding records and patches. A nil Git means
// capture reports the artifact unavailable and makes no git call.
type Deps struct {
	Git   Git
	Store *store.Store
}

// DiffResult is what one round-end capture attempt produced. A zero value means
// "nothing to say about this round's diff", which is a normal outcome.
type DiffResult struct {
	Available bool     // a comparison actually ran
	Path      string   // patch file; "" when there is no body to read
	Stat      git.Stat // exact whenever Available
	Truncated bool     // patch omitted because it exceeded the cap
	Reason    string   // why Available is false; "" when it is true

	// The tree snapshotted at round close. Non-empty whenever the snapshot itself
	// succeeded -- including when the subsequent DiffTrees failed, because a
	// successful snapshot is a valid drift origin regardless of what the comparison
	// did.
	EndTree string
}

func formatFiles(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// commitClause renders the commit facts for a diff note (forLine false) or
// the mastermind's Diff: line (forLine true), or "" when there is nothing to
// say: unknown facts with no reason.
func commitClause(facts CommitResult, branch string, forLine bool) string {
	if !facts.Known {
		if facts.Reason == "" {
			return ""
		}
		return "commits unknown (" + facts.Reason + ")"
	}
	tree := "clean"
	if facts.Dirty {
		tree = "dirty"
	}
	if !forLine {
		if facts.Commits == 0 {
			return "no commits, " + tree
		}
		return fmt.Sprintf("%s, %s", formatCommits(facts.Commits), tree)
	}
	if facts.Commits == 0 {
		if facts.Dirty {
			return "no commits; changes are uncommitted in the worktree"
		}
		return "no commits, tree clean"
	}
	on := ""
	if branch != "" {
		on = " on " + branch
	}
	return fmt.Sprintf("%s%s, tree %s", formatCommits(facts.Commits), on, tree)
}

func formatCommits(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", n)
}

// DiffSummary returns the human summary for the diff log entry, with the
// round's commit facts appended after "; " unless the diff is empty.
func DiffSummary(res DiffResult, facts CommitResult) string {
	var base string
	switch {
	case !res.Available && res.Reason != "":
		base = fmt.Sprintf("unavailable: %s", res.Reason)
	case !res.Available:
		base = "unavailable"
	case res.Stat.Empty():
		return "no changes"
	case res.Truncated:
		base = "truncated"
	default:
		base = fmt.Sprintf("%s, +%d -%d", formatFiles(res.Stat.FilesChanged), res.Stat.Insertions, res.Stat.Deletions)
	}
	if clause := commitClause(facts, "", false); clause != "" {
		return base + "; " + clause
	}
	return base
}

// Baseline returns the tree a round starts from, or "" when no snapshot is
// possible. It NEVER returns an error: a handoff must not fail because a
// snapshot did. "" whenever d.Git is nil, b.CWD is not a repository, or git
// failed; a tree id otherwise. head is HEAD's commit id when the snapshot
// succeeded and HeadCommit did, "" otherwise: a tree without a head is normal
// (unborn HEAD), reported as commits unknown (no baseline).
func Baseline(ctx context.Context, d Deps, b store.Binding) (tree, head string) {
	if d.Git == nil || b.CWD == "" {
		return "", ""
	}
	tree, err := d.Git.SnapshotTree(ctx, b.CWD)
	if err != nil {
		return "", ""
	}
	head, err = d.Git.HeadCommit(ctx, b.CWD)
	if err != nil {
		return tree, ""
	}
	return tree, head
}

// RoundDiff closes out the diff for b's current round: it snapshots the tree
// again and compares it against b.RoundBaselineTree, storing the patch as a
// round_file row when there is a body worth keeping. With no baseline it makes
// no git calls at all, checked ahead of the snapshot because the snapshot is
// the expensive half and this runs with the state lock held. It NEVER returns
// an error: a round advance must not be blocked by a failed diff. Available is
// false with an empty Reason when d.Git is nil or the tree is not a repository,
// false with "no baseline" without a baseline, and false with a Reason when git
// failed. When Available is true Stat is exact and Path resolves through
// Store.ReadFile (a round_file row, no file on disk) unless the diff was empty
// or truncated; EndTree is the snapshotted tree whenever the snapshot succeeded,
// empty otherwise, independently of Available.
func RoundDiff(ctx context.Context, d Deps, tx *store.Tx, b store.Binding) DiffResult {
	if d.Git == nil {
		return DiffResult{Available: false}
	}
	if b.RoundBaselineTree == "" {
		return DiffResult{Available: false, Reason: "no baseline"}
	}

	end, err := d.Git.SnapshotTree(ctx, b.CWD)
	if errors.Is(err, git.ErrNotRepo) {
		return DiffResult{Available: false}
	}
	if err != nil {
		return DiffResult{Available: false, Reason: text.Brief(err)}
	}

	diff, err := d.Git.DiffTrees(ctx, b.CWD, b.RoundBaselineTree, end)
	if errors.Is(err, git.ErrNotRepo) {
		return DiffResult{Available: false, EndTree: end}
	}
	if err != nil {
		return DiffResult{Available: false, Reason: text.Brief(err), EndTree: end}
	}

	if diff.Stat.Empty() {
		return DiffResult{Available: true, Stat: diff.Stat, EndTree: end}
	}

	if diff.Truncated {
		return DiffResult{Available: true, Stat: diff.Stat, Truncated: true, EndTree: end}
	}

	patchPath := d.Store.DiffPath(b.Name, b.Round)
	if err := tx.PutRoundFile(b.Name, b.Round, patchPath, diff.Patch); err != nil {
		return DiffResult{Available: false, Reason: text.Brief(err), EndTree: end}
	}

	return DiffResult{Available: true, Path: patchPath, Stat: diff.Stat, EndTree: end}
}

// PlanDiffSpec is what one plan's cumulative diff needs: the binding the patch
// is stored against, the worktree to diff, the plan-start commit, the closing
// round's closed tree, and the round_file key to store the patch at.
type PlanDiffSpec struct {
	Name           string
	Round          int
	Dir, From, End string
	Path           string
}

// PlanDiff closes out a plan's cumulative diff: the closing round's closed tree
// against the plan-start commit, stored at spec.Path. It is RoundDiff's twin for
// a span crossing rounds. It NEVER returns an error, exactly as RoundDiff does,
// and takes its trees as given rather than snapshotting, so it makes no
// SnapshotTree call and adds no git method to the Git seam. Available is false
// with an empty Reason when d.Git is nil or the tree is not a repository, false
// with "no baseline" without a From or End, false with "no path" when a patch
// needs storing but no path was given, and false with a Reason when git failed.
// When Available is true Stat is exact and Path resolves through Store.ReadFile
// unless the diff was empty or truncated.
func PlanDiff(ctx context.Context, d Deps, tx *store.Tx, spec PlanDiffSpec) DiffResult {
	if d.Git == nil {
		return DiffResult{Available: false}
	}
	if spec.From == "" || spec.End == "" {
		return DiffResult{Available: false, Reason: "no baseline"}
	}

	diff, err := d.Git.DiffTrees(ctx, spec.Dir, spec.From, spec.End)
	if errors.Is(err, git.ErrNotRepo) {
		return DiffResult{Available: false}
	}
	if err != nil {
		return DiffResult{Available: false, Reason: text.Brief(err)}
	}

	if diff.Stat.Empty() {
		return DiffResult{Available: true, Stat: diff.Stat}
	}
	if diff.Truncated {
		return DiffResult{Available: true, Stat: diff.Stat, Truncated: true}
	}
	if spec.Path == "" {
		return DiffResult{Available: false, Reason: "no path"}
	}
	if err := tx.PutRoundFile(spec.Name, spec.Round, spec.Path, diff.Patch); err != nil {
		return DiffResult{Available: false, Reason: text.Brief(err)}
	}
	return DiffResult{Available: true, Path: spec.Path, Stat: diff.Stat}
}

// CommitResult is what one round-end commit-facts capture produced. Known is
// all-or-nothing: either both facts were captured or neither was, and Reason
// names the step that failed ("" for a non-repository, which is not worth a
// sentence).
type CommitResult struct {
	Known   bool   // both facts were captured
	Commits int    // rev-list --count RoundBaselineHead..HEAD; 0 when Known is false
	Dirty   bool   // uncommitted or untracked changes; false when Known is false
	Reason  string // why Known is false; "" when it is true
}

// CommitFacts captures how many commits b's current round added and whether its
// tree is dirty, for the round closing. It NEVER returns an error: the facts are
// informational and a round advance must not be blocked by them. It runs under
// the state lock, like RoundDiff. Known is false with an empty Reason when
// d.Git is nil or the tree is not a repository, false with a Reason naming the
// step when RoundBaselineHead is empty or a git call failed, true with both
// facts otherwise; HeadCommit, RevListCount, Dirty stop at the first failure.
func CommitFacts(ctx context.Context, d Deps, b store.Binding) CommitResult {
	if d.Git == nil {
		return CommitResult{}
	}
	if b.RoundBaselineHead == "" {
		return CommitResult{Reason: "no baseline"}
	}
	head, err := d.Git.HeadCommit(ctx, b.CWD)
	if errors.Is(err, git.ErrNotRepo) {
		return CommitResult{}
	}
	if err != nil {
		return CommitResult{Reason: "head: " + text.Brief(err)}
	}
	n, err := d.Git.RevListCount(ctx, b.CWD, b.RoundBaselineHead, head)
	if errors.Is(err, git.ErrNotRepo) {
		return CommitResult{}
	}
	if err != nil {
		return CommitResult{Reason: "rev-list: " + text.Brief(err)}
	}
	dirty, err := d.Git.Dirty(ctx, b.CWD)
	if errors.Is(err, git.ErrNotRepo) {
		return CommitResult{}
	}
	if err != nil {
		return CommitResult{Reason: "dirty check: " + text.Brief(err)}
	}
	return CommitResult{Known: true, Commits: n, Dirty: dirty}
}

// showCommand renders the mastermind-facing command that prints one round's
// artifact, so a line names the durable command instead of the patch's path:
// a closed round's files may be sealed into the database.
func showCommand(name string, round int, section string) string {
	return fmt.Sprintf("relevo show %s --round %d --%s", name, round, section)
}

// DiffLine renders the report-payload Diff: line from a fresh diff result, or ""
// when the result says nothing worth telling the mastermind. The commit facts
// follow " -- " unless the diff is empty; branch is "" for a tree relevo did
// not create.
func DiffLine(res DiffResult, facts CommitResult, branch, name string, round int) string {
	var base string
	switch {
	case !res.Available && res.Reason == "":
		return ""
	case !res.Available:
		base = fmt.Sprintf("Diff: unavailable (%s)", res.Reason)
	case res.Stat.Empty():
		return "Diff: no file changes"
	case res.Truncated:
		base = fmt.Sprintf("Diff: %s, +%d -%d (patch omitted, over the 4 MiB cap)",
			formatFiles(res.Stat.FilesChanged), res.Stat.Insertions, res.Stat.Deletions)
	default:
		base = fmt.Sprintf("Diff: %s (%s, +%d -%d)",
			showCommand(name, round, "diff"), formatFiles(res.Stat.FilesChanged), res.Stat.Insertions, res.Stat.Deletions)
	}
	if clause := commitClause(facts, branch, true); clause != "" {
		return base + " -- " + clause
	}
	return base
}

// DiffLineFromNote renders the report-payload Diff: line from a diff log entry's
// stored facts rather than a fresh DiffResult: the shape a remote binding's
// catchUp writes, since the wire carries the server's note and commit facts,
// never a patch. It reuses commitClause so the two never drift on wording, and
// reproduces DiffLine exactly when the note alone says everything it would
// ("no changes", "unavailable[: reason]"); for every other shape it keeps the
// note's own wording, since the Stat and patch path never crossed the wire.
func DiffLineFromNote(note string, commits int, tree, branch string) string {
	if note == "" {
		return ""
	}

	base := note
	if idx := strings.Index(base, "; "); idx != -1 {
		base = base[:idx]
	}

	switch {
	case base == "no changes":
		return "Diff: no file changes"
	case base == "unavailable":
		// Matches DiffLine's own !Available && Reason == "" case: nothing
		// worth telling the mastermind.
		return ""
	case strings.HasPrefix(base, "unavailable: "):
		base = fmt.Sprintf("unavailable (%s)", strings.TrimPrefix(base, "unavailable: "))
	}

	line := "Diff: " + base
	facts := CommitResult{Known: tree != "", Commits: commits, Dirty: tree == "dirty"}
	if clause := commitClause(facts, branch, true); clause != "" {
		line += " -- " + clause
	}
	return line
}

// pathsClauseRe matches the changed_paths mismatch clause a diff note carries,
// "paths: report N, diff M". The clause can sit anywhere in the note, so it is
// found by search rather than anchored.
var pathsClauseRe = regexp.MustCompile(`paths: report (\d+), diff (\d+)`)

// PathsLine renders the report-payload line for a changed_paths mismatch.
// report is the paths the report listed, diff the files the diff touched. Pure.
func PathsLine(report, diff int) string {
	return fmt.Sprintf("Paths: the report's changed_paths lists %d, the diff has %s -- check the diff, not the list",
		report, formatFiles(diff))
}

// PathsLineFromNote renders PathsLine from a KindDiff note -- the shape a remote
// binding's catchUp holds, since the wire carries the note and never a patch --
// or "" when the note carries no "paths: report N, diff M" clause. Pure.
func PathsLineFromNote(note string) string {
	m := pathsClauseRe.FindStringSubmatch(note)
	if m == nil {
		return ""
	}
	report, _ := strconv.Atoi(m[1])
	diff, _ := strconv.Atoi(m[2])
	return PathsLine(report, diff)
}

// ReadDiff returns the stored patch for one round, and whether one exists.
// It is the read path behind `relevo show --diff`.
//
// Errors: store.ErrNotFound for an unknown binding; a wrapped read error.
func ReadDiff(s *store.Store, name string, round int) ([]byte, bool, error) {
	if _, err := s.Load(name); err != nil {
		return nil, false, err
	}

	path := s.DiffPath(name, round)
	data, err := s.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read diff %s: %w", path, err)
	}
	return data, true, nil
}
