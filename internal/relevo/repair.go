package relevo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/capture"
	"github.com/fuad-daoud/relevo/internal/store"
)

// repairTailLines is how much of the failing gate's log a repair plan carries,
// as fenced text the builder can read without opening the file (#132 part 2).
const repairTailLines = 200

// The noise a gate's output carries that says nothing about why it failed:
// timestamps, durations, big integers, hex digests and temp paths all move
// between two runs of the same failing check. gateSignature drops them so the
// stall bound compares the failure, not the clock.
var (
	gateRFC3339Re = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)
	gateClockRe   = regexp.MustCompile(`\b\d{2}:\d{2}:\d{2}\b`)
	gateDurRe     = regexp.MustCompile(`\d+(?:\.\d+)?(?:ms|s|m)`)
	gateIntRe     = regexp.MustCompile(`\b\d{6,}\b`)
	gateHexRe     = regexp.MustCompile(`\b[0-9a-fA-F]{8,}\b`)
	gateTmpRe     = regexp.MustCompile(`\S*/tmp/\S*`)
)

// gateSignature is a stable fingerprint of a gate log's content (#132 part 2).
// Timestamps, durations, large integers, hex runs and /tmp paths are dropped,
// the remaining lines are trimmed, emptied lines discarded, the rest sorted,
// and the whole thing hashed, so two runs of the same failure hash the same
// however much of the output was clock, timing or tempdir noise. "" when the
// file cannot be read: the caller then skips the stall bound rather than
// treating an unreadable log as a repeat.
func gateSignature(read func(string) ([]byte, error), logPath string) string {
	data, err := read(logPath)
	if err != nil {
		return ""
	}

	var lines []string
	for _, raw := range strings.Split(string(data), "\n") {
		line := gateRFC3339Re.ReplaceAllString(raw, "")
		line = gateClockRe.ReplaceAllString(line, "")
		line = gateDurRe.ReplaceAllString(line, "")
		line = gateIntRe.ReplaceAllString(line, "")
		line = gateHexRe.ReplaceAllString(line, "")
		line = gateTmpRe.ReplaceAllString(line, "")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	sort.Strings(lines)

	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// repairPlan is round failedRound+1's plan text (#132 part 2): the failed
// round's acceptance check did not pass, so fix only what it reports. It names
// the original plan and carries the tail of the gate log inline, because the
// builder gets nothing but this file.
func repairPlan(b store.Binding, failedRound int, planPath, gateLogPath string, tail []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Repair round %d for %s: round %d's gate failed\n", failedRound+1, b.Name, failedRound)
	fmt.Fprintf(&sb, "Round %d's acceptance check (`%s`) did NOT pass. Fix ONLY what the check reports; do not\n", failedRound, b.Gate)
	sb.WriteString("restyle or refactor unrelated code. If the failure is not something a code change can fix, halt and report.\n")
	fmt.Fprintf(&sb, "Original plan: %s   (read it first; the same rules apply)\n", planPath)
	fmt.Fprintf(&sb, "Acceptance check output: %s -- last %d lines:\n", gateLogPath, len(tail))
	sb.WriteString("```\n")
	for _, line := range tail {
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	sb.WriteString("```\n")
	sb.WriteString("When done: run the same check yourself in the foreground, then write your report and create the done marker as before.\n")
	return sb.String()
}

// startRepairRound stages and hands over round failedRound+1 after a failing
// gate (#132 part 2). It is called on the tick that closed round failedRound,
// after the report has been queued to the mastermind, with the binding already
// advanced (`b.Round == failedRound+1`, RoundStartedAt zero).
//
// Two bounds end the loop with NEEDS YOU instead of another repair: the
// repair budget is spent (RepairCount >= Regate), or the new failure's
// normalised signature equals the previous one -- the builder changed nothing
// that mattered. Otherwise it writes round N+1's plan, hands it to the
// builder the way Send does (a fresh process for a headless binding, a prompt
// for a pane), logs `repair k/M` on the round's plan entry, and opens the
// round exactly as a Send would.
//
// Preconditions: the round just closed; rec.Result == "fail"; b.Regate > 0.
func startRepairRound(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, rec store.GateRecord, failedRound int) (store.Binding, error) {
	sig := gateSignature(rt.Store.ReadFile, rec.LogPath)
	if b.RepairCount >= b.Regate {
		return haltBinding(ctx, rt, b, fmt.Sprintf("%s: gate failed after %d repair round(s) (regate %d); see %s", b.Name, b.RepairCount, b.Regate, showCommand(b.Name, failedRound, "gate")))
	}
	if sig != "" && sig == b.LastGateSig {
		return haltBinding(ctx, rt, b, fmt.Sprintf("%s: gate output unchanged after repair; see %s", b.Name, showCommand(b.Name, failedRound, "gate")))
	}

	b.LastGateSig = sig
	b.RepairCount++

	text := repairPlan(b, failedRound, rt.Store.PromptPath(b.Name, failedRound), rec.LogPath, tailLines(rt.Store.ReadFile, rec.LogPath, repairTailLines))
	planPath := rt.Store.PromptPath(b.Name, b.Round)
	if err := os.WriteFile(planPath, []byte(text), 0o644); err != nil {
		return haltBinding(ctx, rt, b, fmt.Sprintf("%s: repair round %d could not stage its plan: %v", b.Name, b.Round, err))
	}

	baseline, head := capture.Baseline(ctx, captureDeps(rt), b)
	prompt := composePrompt(rt, b, planPath, rt.Store.ReportPath(b.Name, b.Round), rt.Store.DonePath(b.Name, b.Round))

	// A repair round must not be handed to a candidate the configured set no
	// longer holds: pick again first, exactly as a switch does.
	b, res, err := repickStale(rt, b, false)
	if err != nil {
		return haltBinding(ctx, rt, b, fmt.Sprintf("%s: repair round %d could not start: %v", b.Name, b.Round, err))
	}
	if res != nil {
		if err := tx.AppendLog(b.Name, pickEntry(rt.Now().UTC(), b.Round, bindingRole(b), *res)); err != nil {
			return haltBinding(ctx, rt, b, fmt.Sprintf("%s: repair round %d could not start: %v", b.Name, b.Round, err))
		}
	}

	started, err := startRound(ctx, rt, tx, b, prompt, false)
	if err != nil {
		return haltBinding(ctx, rt, b, fmt.Sprintf("%s: repair round %d could not start: %v", b.Name, b.Round, err))
	}
	b = started

	if err := tx.AppendLog(b.Name, store.LogEntry{
		TS:        rt.Now().UTC(),
		Round:     b.Round,
		Direction: store.DirToBuilder,
		Kind:      store.KindPrompt,
		Path:      planPath,
		Confirmed: true,
		Tier:      string(effectiveTier(b)),
		Note:      fmt.Sprintf("repair %d/%d", b.RepairCount, b.Regate),
	}); err != nil {
		return haltBinding(ctx, rt, b, fmt.Sprintf("%s: repair round %d could not record its plan: %v", b.Name, b.Round, err))
	}

	b.RoundBaselineTree, b.RoundBaselineHead = baseline, head
	b.RoundClosedTree = ""
	b.RoundStartedAt = rt.Now().UTC()
	b.FinishPending = true
	b.State = store.StateActive
	slog.Info("repair round started", "binding", b.Name, "round", b.Round, "repair", b.RepairCount, "of", b.Regate)
	return b, nil
}
