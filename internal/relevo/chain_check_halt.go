package relevo

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// haltCheckTailLines is how much of a failed check's log a halt reason quotes:
// enough to name the failing test or error, short enough for a notice. The
// gate's own tail is shorter because it serves a one-line status.
const haltCheckTailLines = 20

// haltCheckResults are the settled check results that make a halt a check's
// doing.
var haltCheckResults = map[string]bool{"fail": true, "timeout": true, "error": true}

// chainHaltReason is the reason a chain that ends halted or stopped is saved
// with: the engine's own reason, followed by the newest check's result, the
// command that reads its log and the log's tail when that check settled as a
// non-pass. Any other end keeps the engine's wording exactly.
func chainHaltReason(rt Runtime, tx *store.Tx, c db.ChainRow, s workflow.State) string {
	if s.Status != workflow.StatusHalted && s.Status != workflow.StatusStopped {
		return s.Reason
	}
	row, ok := chainNewestCheck(tx, c.Name)
	if !ok || !haltCheckResults[row.Result] {
		return s.Reason
	}
	member, round, found := rt.Store.CheckLogTarget(row.Log)
	if !found {
		return s.Reason
	}
	// The tail comes before the closing sentence so the reason still ends in
	// prose: a notice appends to it, and must not run on from a log line.
	var b strings.Builder
	b.WriteString(s.Reason)
	if tail := tailLines(rt.Store.ReadFile, row.Log, haltCheckTailLines); len(tail) > 0 {
		fmt.Fprintf(&b, "\nLast %d lines of the check's log:\n%s", len(tail), strings.Join(tail, "\n"))
	}
	fmt.Fprintf(&b, "\n%s `%s` reads its log.", checkOutcome(rt, row), showCommand(member, round, "gate"))
	return b.String()
}

// chainNewestCheck is a chain's highest-numbered check row, and false when it
// has none or cannot be read.
func chainNewestCheck(tx *store.Tx, name string) (db.ChainCheckRow, bool) {
	var newest db.ChainCheckRow
	found := false
	for run := 1; ; run++ {
		row, err := tx.ChainCheck(name, run)
		if errors.Is(err, store.ErrNotFound) {
			return newest, found
		}
		if err != nil {
			return db.ChainCheckRow{}, false
		}
		newest, found = row, true
	}
}

// checkOutcome words how the newest check ended. A timeout is not a failure
// and names the policy key that raises its limit; an error carries the row's
// note because it has no exit code worth quoting.
func checkOutcome(rt Runtime, row db.ChainCheckRow) string {
	switch row.Result {
	case "timeout":
		return fmt.Sprintf("The last check timed out after %s; raise the limit with `relevo config set policy.gate.timeout_ms <ms>` (now %d ms).",
			(time.Duration(row.DurationMS) * time.Millisecond).Round(time.Millisecond), rt.Policy.GateTimeout().Milliseconds())
	case "error":
		return fmt.Sprintf("The last check could not run: %s.", row.Note)
	case "fail":
		return fmt.Sprintf("The last check failed (exit code %d);", row.ExitCode)
	default:
		return fmt.Sprintf("The last check ended %s (exit code %d);", row.Result, row.ExitCode)
	}
}
