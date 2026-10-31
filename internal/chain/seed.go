package chain

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// SeedMissing renders an input the seed cannot name as one path-free
// clause: "not available: " plus the reason. A *fs.PathError keeps only its
// underlying reason; any other error keeps its first line. The input's own
// path is never repeated, so nothing in the seed reads as an openable path.
func SeedMissing(err error) string {
	reason := err.Error()
	var perr *fs.PathError
	if errors.As(err, &perr) {
		reason = perr.Err.Error()
	} else if i := strings.IndexByte(reason, '\n'); i >= 0 {
		reason = reason[:i]
	}
	return "not available: " + reason
}

// MemberReportPath is the path the planning member's report entry for round
// carries: the correction or fix plan artifact the planner wrote, which is
// what a builder round on that plan must be handed. A planner with no such
// entry, or one with no path, is a failure of the send rather than a silent
// plan copy.
func MemberReportPath(tx *store.Tx, planner string, round int) (string, error) {
	entries, err := tx.ReadLog(planner)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && e.Kind == store.KindReport && e.Round == round && e.Path != "" {
			return e.Path, nil
		}
	}
	return "", fmt.Errorf("chain planner %s has no plan for round %d", planner, round)
}

// PlanDiffFrom is the commit a reviewer diffs the plan from when no
// cumulative diff was captured: the recorded plan-start commit, else the
// chain's base for plan 1, else none. A chain started before the plan-start
// commit was recorded names none on a later plan, and the seed says so.
func PlanDiffFrom(c db.ChainRow, s State) string {
	if c.PlanStartCommit != "" {
		return c.PlanStartCommit
	}
	if s.Plan == 1 {
		return c.Base
	}
	return ""
}

// ReaderStatus reads the relevo status a reader's own block carries, from
// the same block its outcomes were parsed from: the newest body whose block
// names a declared outcome. A block that carries outcomes but no status line
// reads done; a status the tail contract does not admit reads done too.
func ReaderStatus(bodies [][]byte, outputs workflow.Outputs) string {
	for _, body := range bodies {
		if !BlockCarriesOutcome(body, outputs) {
			continue
		}
		if s, ok := reporttail.BlockValue(body, "status"); ok {
			switch s {
			case reporttail.OutcomeDone, reporttail.OutcomeHalted, reporttail.OutcomeBlocked, reporttail.OutcomeDeferred:
				return s
			}
		}
		return reporttail.OutcomeDone
	}
	return reporttail.OutcomeDone
}

// BlockCarriesOutcome reports whether a body's relevo block names any declared
// outcome key.
func BlockCarriesOutcome(body []byte, outputs workflow.Outputs) bool {
	for _, key := range outputs.Outcomes() {
		if _, ok := reporttail.BlockValue(body, key); ok {
			return true
		}
	}
	return false
}

// RoundGate reads the gate the builder's round wrote on its report entry:
// the KindReport entry for round in the builder's log, or nil when the round
// ran no check. The close writes that record on the entry, so a read under
// the same lock sees it.
func RoundGate(tx *store.Tx, builder string, round int) (*store.GateRecord, error) {
	entries, err := tx.ReadLog(builder)
	if err != nil {
		return nil, err
	}
	var rec *store.GateRecord
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && e.Kind == store.KindReport && e.Round == round {
			rec = e.Gate
		}
	}
	return rec, nil
}

// GateResult is the builder close's gate word: none when no check ran,
// green when one passed, red otherwise. A red gate has already spent the
// regate budget, so it still reaches the reviewer rather than a halt.
func GateResult(gate *store.GateRecord) string {
	if gate == nil {
		return GateNone
	}
	if gate.Result == "pass" {
		return GateGreen
	}
	return GateRed
}

// NewestClosedRound is a member's newest closed round: the highest round
// its log holds a report entry for, or 0 when the record is gone or no round
// has closed. It is the round a resume's seed points its artifacts at.
func NewestClosedRound(tx *store.Tx, name string) int {
	if name == "" {
		return 0
	}
	entries, err := tx.ReadLog(name)
	if err != nil {
		return 0
	}
	newest := 0
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && e.Kind == store.KindReport && e.Round > newest {
			newest = e.Round
		}
	}
	return newest
}
