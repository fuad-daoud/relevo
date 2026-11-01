package relevo

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/text"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// haltCheckTailLines is how much of a failed check's log a halt reason
// quotes: enough to name the failure, short enough for a notice.
const haltCheckTailLines = 20

// haltCheckResults are the settled check results that make a halt a check's
// doing.
var haltCheckResults = map[string]bool{"fail": true, "timeout": true, "error": true}

// chainHaltReason is the reason a halted or stopped chain is saved with: the
// engine's reason, plus the newest non-pass check's result, log reader and
// tail. Any other end keeps the engine's wording.
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
	fmt.Fprintf(&b, "\n%s `%s` reads its log.", checkOutcome(rt, row), text.ShowCommand(member, round, "gate"))
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
