package relevo

import (
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/text"
	"github.com/fuad-daoud/relevo/internal/view"
)

// The pick-able verbs' result lines live here, not in cmd/relevo: the picker's
// result screen prints the same text the terminal does, so each returns the
// exact bytes cmd/relevo printed, without a trailing newline.

// DoneText is what `relevo done` says on success: one line for the binding,
// then at most one for its worktree.
func DoneText(name string, r DoneResult) string {
	lines := []string{
		fmt.Sprintf("%s marked done; relaying stopped (relevo unbind --done archives it when you are finished with it)", name),
	}
	switch {
	case r.WorktreeRemoved != "" && r.Branch != "":
		lines = append(lines, fmt.Sprintf("removed worktree %s (branch %s is free to check out)", r.WorktreeRemoved, r.Branch))
	case r.WorktreeRemoved != "":
		lines = append(lines, fmt.Sprintf("removed worktree %s", r.WorktreeRemoved))
	case r.WorktreeKept != "":
		lines = append(lines, fmt.Sprintf("kept worktree %s (%s); relevo unbind --done retries when it is clean", r.WorktreeKept, r.KeptReason))
	case r.WorktreeGone != "":
		lines = append(lines, fmt.Sprintf("worktree %s was already gone", r.WorktreeGone))
	}
	return strings.Join(lines, "\n")
}

// RestoreText is what `relevo bind --resume` prints when a missing worktree was restored.
func RestoreText(res resolve.Resolution) string {
	if res.RestoredWorktree == "" {
		return ""
	}
	lines := []string{
		fmt.Sprintf("restored worktree %s on %s", res.RestoredWorktree, res.RestoredBranch),
	}
	return strings.Join(lines, "\n")
}

// PlacementText is the clause a fresh create's line appends when the actor's
// own placement list chose the server: exactly the bytes the pick note already
// appends. placementClause stays unexported and unedited, which is what keeps
// the pick note byte-identical.
func PlacementText(p resolve.PlacementResolution) string {
	if p.How != resolve.PlacementActor {
		return ""
	}
	return resolve.PlacementClause(p)
}

// StopText is what `relevo stop` says on success: what happened to the round.
func StopText(name string, res StopResult) string {
	clause := text.WithoutArtifact(res.Shape == store.ShapeReader)
	switch res.Action {
	case "killed":
		return fmt.Sprintf("%s round %d stopped: process killed; round closed %s unless one was on disk", name, res.Round, clause)
	case "reaped":
		return fmt.Sprintf("%s round %d stopped: reaped the round's scope (its runner was already gone); round closed %s unless one was on disk", name, res.Round, clause)
	case "gone":
		return fmt.Sprintf("%s round %d stopped: its runner was already gone and nothing was left running; round closed %s unless one was on disk", name, res.Round, clause)
	case "dequeued":
		return fmt.Sprintf("%s round %d stopped: dropped from the server queue before it started; round closed %s", name, res.Round, clause)
	default:
		return fmt.Sprintf("%s has no open round; nothing to stop", name)
	}
}

// RenderDryRun is what `relevo send --dry-run` prints: the round it would open,
// the builder and where it would go, the paths, and the head of the prompt. It
// names only what the preflight read; nothing here was sent.
func RenderDryRun(d DryRun) string {
	tierVal := d.Tier
	if d.TierFrom != "" {
		tierVal = fmt.Sprintf("%s  (%s)", d.Tier, d.TierFrom)
	}
	lines := []string{
		fmt.Sprintf("would send round %d to %s", d.Round, d.Name),
		fmt.Sprintf("  %-8s  %s", "runner", dryRunBuilderLine(d)),
		fmt.Sprintf("  %-8s  %s", "where", d.Where),
		fmt.Sprintf("  %-8s  %s", "tier", tierVal),
		fmt.Sprintf("  %-8s  %s  (staged from %s, %s)", "prompt", d.PromptPath, d.PromptFrom, view.HumanBytes(d.PromptBytes)),
		fmt.Sprintf("  %-8s  %s", "report", d.ReportPath),
		fmt.Sprintf("  %-8s  %s", "marker", d.DonePath),
	}
	first := ""
	if len(d.PromptHead) > 0 {
		first = d.PromptHead[0]
	}
	lines = append(lines, fmt.Sprintf("  %-8s  %s", "head", first))
	for _, cont := range d.PromptHead[1:] {
		lines = append(lines, "            "+cont)
	}
	return strings.Join(lines, "\n") + "\n"
}

// dryRunBuilderLine is the builder line's value: the mode and candidate, with
// the advisory gate note in parentheses when one applies and the candidate's
// short name when it has one.
func dryRunBuilderLine(d DryRun) string {
	candidate := d.Candidate
	if d.CandidateName != "" {
		candidate = d.CandidateName
	}
	line := d.Mode + " " + candidate
	if d.GateNote != "" {
		line += "  (" + d.GateNote + ")"
	}
	return line
}

// UnbindText is what `relevo unbind` says on success: one line for the
// binding, then at most one for its worktree, then at most one for a headless process.
func UnbindText(name string, res UnbindResult) string {
	var lines []string
	if res.Archived {
		lines = append(lines, fmt.Sprintf("archived %s", name))
	} else {
		lines = append(lines, fmt.Sprintf("unbound %s", name))
	}
	switch {
	case res.WorktreeRemoved != "":
		lines = append(lines, fmt.Sprintf("removed worktree %s", res.WorktreeRemoved))
	case res.WorktreeKept != "":
		lines = append(lines, fmt.Sprintf("kept worktree %s (%s)\n  remove by hand: git -C %s worktree remove %s",
			res.WorktreeKept, res.KeptReason, res.WorktreeKept, res.WorktreeKept))
	case res.WorktreeGone != "":
		lines = append(lines, fmt.Sprintf("worktree %s was already gone", res.WorktreeGone))
	}
	switch {
	case res.ProcessStopped != 0:
		lines = append(lines, fmt.Sprintf("stopped builder process %d", res.ProcessStopped))
	case res.ProcessErr != "":
		lines = append(lines, fmt.Sprintf("could not stop builder process (%s); check for it yourself", res.ProcessErr))
	}
	return strings.Join(lines, "\n")
}
