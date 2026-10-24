package relevo

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// The two ways a full disk words itself: errno ENOSPC as libc and Go print it,
// and the Windows-style message some tools carry.
const (
	phraseNoSpace   = "no space left on device"
	phraseNoStorage = "no storage space"
)

// diskFullScanBytes bounds how much of a check log is read for a full disk: the
// error is written where the command died, so the end of the log holds it.
const diskFullScanBytes = 64 << 10

// environmentPrefix is the head of every EnvironmentError's text, which is how
// a stored note tells an environment problem from a command's own failure.
const environmentPrefix = "environment: "

// diskFullPhrase returns the phrase tail reports, matched case-insensitively.
// Only the error form counts: real ENOSPC output ends "<something>: <phrase>",
// while a file a builder printed or a test message quoting the phrase does not
// carry it directly after ": ".
func diskFullPhrase(tail string) (string, bool) {
	low := strings.ToLower(tail)
	for _, p := range []string{phraseNoSpace, phraseNoStorage} {
		if strings.Contains(low, ": "+p) {
			return p, true
		}
	}
	return "", false
}

func diskFullIn(tail string) bool {
	_, ok := diskFullPhrase(tail)
	return ok
}

// diskFullNote is the environment note naming phrase as the evidence.
func diskFullNote(phrase string) string {
	return (&EnvironmentError{Reason: fmt.Sprintf("disk full: the output reports %q", phrase)}).Error()
}

// isEnvironmentNote reports whether note blames the machine rather than the code.
func isEnvironmentNote(note string) bool {
	return strings.HasPrefix(note, environmentPrefix)
}

// readTailBytes reads at most max trailing bytes of the file at path; an
// unreadable file reads as empty, because the scan is a convenience.
func readTailBytes(path string, max int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > max {
		if _, err := f.Seek(-max, io.SeekEnd); err != nil {
			return ""
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, max))
	if err != nil {
		return ""
	}
	return string(data)
}

// diskFullLogNote is the environment note for the check log at path, or ""
// when its bounded tail does not report a full disk.
func diskFullLogNote(path string) string {
	if phrase, ok := diskFullPhrase(readTailBytes(path, diskFullScanBytes)); ok {
		return diskFullNote(phrase)
	}
	return ""
}

// nameDiskFull sets the note of a check that did not pass when its log reports
// a full disk. Result stays as it was, so no downstream result handling moves.
func nameDiskFull(rec *store.GateRecord) {
	if rec.Result == "pass" {
		return
	}
	if note := diskFullLogNote(rec.LogPath); note != "" {
		rec.Note = note
	}
}

// haltOnDiskFullExit halts a round whose builder exited without a report, whose
// stream tail reports a full disk and whose worktree is short of space now. The
// candidate did not fail, so the halt is neither a switch nor an exclusion.
//
// The tail holds the builder's own tool output, which can carry the phrase
// while the builder exits for an unrelated reason, so the text alone is not
// proof: the free-space probe must refuse as well. A full disk that has already
// cleared, such as a check's temp dir that was removed, falls through to the
// normal exit handling.
func haltOnDiskFullExit(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, tail string) (store.Binding, bool, error) {
	phrase, ok := diskFullPhrase(tail)
	if !ok {
		return b, false, nil
	}
	tree := b.Worktree
	if tree == "" {
		tree = b.CWD
	}
	perr := preflightDisk(rt, tree)
	if perr == nil {
		return b, false, nil
	}
	next, err := haltAndSettle(ctx, rt, tx, b, fmt.Sprintf("%s: %s (the output also reports %q); see %s", b.Name, perr, phrase, showCommand(b.Name, b.Round, "log")))
	return next, true, err
}

// haltOnEnvironmentGate halts a binding whose just-closed round's gate carries
// an environment note, before any repair round is bought for it.
func haltOnEnvironmentGate(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, rec *store.GateRecord, closedRound int) (store.Binding, bool, error) {
	if rec == nil || !isEnvironmentNote(rec.Note) || b.State == store.StateNeedsYou {
		return b, false, nil
	}
	next, err := haltAndSettle(ctx, rt, tx, b, fmt.Sprintf("%s: gate: %s; see %s", b.Name, rec.Note, showCommand(b.Name, closedRound, "gate")))
	return next, true, err
}

// chainHaltOnEnvironment halts the chain when the check run that just settled
// carries an environment note: a full disk is not red code for the repair loop.
func chainHaltOnEnvironment(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, run int, ev workflow.Event) (bool, error) {
	row, err := tx.ChainCheck(c.Name, run)
	if err != nil || !isEnvironmentNote(row.Note) {
		return false, err
	}
	def, err := chainWorkflowDef(c)
	if err != nil {
		return false, err
	}
	before, err := chainWorkflowState(c)
	if err != nil {
		return false, err
	}
	next := cloneFlowState(before)
	next.Status = workflow.StatusHalted
	next.Reason = fmt.Sprintf("check %s: %s", row.Step, row.Note)
	return true, chainTerminalWF(ctx, rt, tx, c, def, before, next, ev, workflow.Action{Kind: workflow.ActionHalt, Reason: next.Reason})
}
