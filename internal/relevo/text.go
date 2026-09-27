package relevo

import (
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/view"
)

// The result lines of the three pick-able verbs live here, not in cmd/relevo,
// because the picker's result screen (internal/pick) prints the same text the
// terminal does. Each returns the exact bytes cmd/relevo printed before #15,
// without a trailing newline; the caller adds one.

// showCommand renders the `relevo show` command that prints one round's
// artifact, the durable way to name a closed round whose files may be sealed
// into the database.
func showCommand(name string, round int, section string) string {
	return fmt.Sprintf("relevo show %s --round %d --%s", name, round, section)
}

// brief reduces an error to one line, for a reason or note field where a
// multi-line git message would break the line's shape.
func brief(err error) string {
	if err == nil {
		return ""
	}
	s := strings.TrimSpace(err.Error())
	if idx := strings.Index(s, "\n"); idx != -1 {
		s = strings.TrimSpace(s[:idx])
	}
	return s
}

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
func RestoreText(res Resolution) string {
	if res.RestoredWorktree == "" {
		return ""
	}
	lines := []string{
		fmt.Sprintf("restored worktree %s on %s", res.RestoredWorktree, res.RestoredBranch),
	}
	return strings.Join(lines, "\n")
}

// StopText is what `relevo stop` says on success: what happened to the round.
func StopText(name string, res StopResult) string {
	switch res.Action {
	case "killed":
		return fmt.Sprintf("%s round %d stopped: process killed; round closed without a report unless one was on disk", name, res.Round)
	case "reaped":
		return fmt.Sprintf("%s round %d stopped: reaped the round's scope (its runner was already gone); round closed without a report unless one was on disk", name, res.Round)
	case "gone":
		return fmt.Sprintf("%s round %d stopped: its runner was already gone and nothing was left running; round closed without a report unless one was on disk", name, res.Round)
	case "dequeued":
		return fmt.Sprintf("%s round %d stopped: dropped from the server queue before it started; round closed without a report", name, res.Round)
	default:
		return fmt.Sprintf("%s has no open round; nothing to stop", name)
	}
}

// RenderDryRun is what `relevo send --dry-run` prints: the round it would open,
// the builder and where it would go, the paths, and the head of the prompt. It
// names only what the preflight read; nothing here was sent (#149).
func RenderDryRun(d DryRun) string {
	lines := []string{
		fmt.Sprintf("would send round %d to %s", d.Round, d.Name),
		fmt.Sprintf("  %-8s  %s", "runner", dryRunBuilderLine(d)),
		fmt.Sprintf("  %-8s  %s", "where", d.Where),
		fmt.Sprintf("  %-8s  %s", "tier", d.Tier),
		fmt.Sprintf("  %-8s  %s  (staged from %s, %s)", "plan", d.PlanPath, d.PlanFrom, view.HumanBytes(d.PlanBytes)),
		fmt.Sprintf("  %-8s  %s", "report", d.ReportPath),
		fmt.Sprintf("  %-8s  %s", "marker", d.DonePath),
	}
	first := ""
	if len(d.PromptHead) > 0 {
		first = d.PromptHead[0]
	}
	lines = append(lines, fmt.Sprintf("  %-8s  %s", "prompt", first))
	for _, cont := range d.PromptHead[1:] {
		lines = append(lines, "            "+cont)
	}
	return strings.Join(lines, "\n") + "\n"
}

// dryRunBuilderLine is the builder line's value: the mode and candidate, with
// the advisory gate note in parentheses when one applies. A1 §4.4: the
// candidate's short name is printed when it has one.
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
