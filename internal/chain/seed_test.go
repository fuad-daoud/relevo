package chain

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// TestSeedMissingKeepsOnlyTheReason pins the path-free clause: a plain
// error keeps its first line, and a path error keeps only its underlying
// reason, never the path.
func TestSeedMissingKeepsOnlyTheReason(t *testing.T) {
	t.Parallel()

	if got, want := SeedMissing(errors.New("boom")), "not available: boom"; got != want {
		t.Errorf("SeedMissing(boom) = %q, want %q", got, want)
	}
	if got, want := SeedMissing(errors.New("first\nsecond")), "not available: first"; got != want {
		t.Errorf("SeedMissing(multiline) = %q, want %q", got, want)
	}
	perr := &fs.PathError{Op: "open", Path: "secret/path.md", Err: errors.New("denied")}
	if got, want := SeedMissing(perr), "not available: denied"; got != want {
		t.Errorf("SeedMissing(path error) = %q, want %q", got, want)
	}
}

// TestPlanDiffFromPrefersTheRecordedCommit pins the diff base: the recorded
// plan-start commit, else the chain's base on plan 1, else none.
func TestPlanDiffFromPrefersTheRecordedCommit(t *testing.T) {
	t.Parallel()

	row := db.ChainRow{PlanStartCommit: "abc", Base: "main"}
	if got := PlanDiffFrom(row, State{Plan: 1}); got != "abc" {
		t.Errorf("PlanDiffFrom(recorded) = %q, want %q", got, "abc")
	}
	row.PlanStartCommit = ""
	if got := PlanDiffFrom(row, State{Plan: 1}); got != "main" {
		t.Errorf("PlanDiffFrom(plan 1) = %q, want %q", got, "main")
	}
	if got := PlanDiffFrom(row, State{Plan: 2}); got != "" {
		t.Errorf("PlanDiffFrom(later plan) = %q, want empty", got)
	}
}

func verdictOutputs() workflow.Outputs {
	return workflow.Outputs{"verdict": {Kind: workflow.OutputOneOf, Values: []string{"pass", "changes"}}}
}

// TestReaderStatusReadsTheNewestOutcomeBlock pins that the status comes from
// the newest body whose block names a declared outcome, that a missing or
// unadmitted status reads done, and that bodies without outcomes are skipped.
func TestReaderStatusReadsTheNewestOutcomeBlock(t *testing.T) {
	t.Parallel()

	outputs := verdictOutputs()
	halted := []byte("```relevo\nverdict: changes\nstatus: halted\n```\n")
	pass := []byte("```relevo\nverdict: pass\n```\n")
	if got := ReaderStatus([][]byte{halted, pass}, outputs); got != reporttail.OutcomeHalted {
		t.Errorf("ReaderStatus(newest first) = %q, want %q", got, reporttail.OutcomeHalted)
	}
	if got := ReaderStatus([][]byte{pass}, outputs); got != reporttail.OutcomeDone {
		t.Errorf("ReaderStatus(no status) = %q, want %q", got, reporttail.OutcomeDone)
	}
	bogus := []byte("```relevo\nverdict: pass\nstatus: bogus\n```\n")
	if got := ReaderStatus([][]byte{bogus}, outputs); got != reporttail.OutcomeDone {
		t.Errorf("ReaderStatus(unadmitted status) = %q, want %q", got, reporttail.OutcomeDone)
	}
	plain := []byte("```relevo\nnote: hi\n```\n")
	if got := ReaderStatus([][]byte{plain, pass}, outputs); got != reporttail.OutcomeDone {
		t.Errorf("ReaderStatus(skips outcome-less) = %q, want %q", got, reporttail.OutcomeDone)
	}
	if got := ReaderStatus(nil, outputs); got != reporttail.OutcomeDone {
		t.Errorf("ReaderStatus(empty) = %q, want %q", got, reporttail.OutcomeDone)
	}
}

// TestBlockCarriesOutcomeNamesDeclaredKeys pins that a body counts only when
// its block names a declared outcome key.
func TestBlockCarriesOutcomeNamesDeclaredKeys(t *testing.T) {
	t.Parallel()

	outputs := verdictOutputs()
	if !BlockCarriesOutcome([]byte("```relevo\nverdict: pass\n```\n"), outputs) {
		t.Error("BlockCarriesOutcome(verdict block) = false, want true")
	}
	if BlockCarriesOutcome([]byte("```relevo\nnote: hi\n```\n"), outputs) {
		t.Error("BlockCarriesOutcome(other block) = true, want false")
	}
	if BlockCarriesOutcome([]byte("no block here"), outputs) {
		t.Error("BlockCarriesOutcome(plain text) = true, want false")
	}
}

// TestGateResultWordsFollowTheRecord pins the seed's gate word: none when no
// check ran, green on pass, red otherwise.
func TestGateResultWordsFollowTheRecord(t *testing.T) {
	t.Parallel()

	if got := GateResult(nil); got != GateNone {
		t.Errorf("GateResult(nil) = %q, want %q", got, GateNone)
	}
	if got := GateResult(&store.GateRecord{Result: "pass"}); got != GateGreen {
		t.Errorf("GateResult(pass) = %q, want %q", got, GateGreen)
	}
	for _, raw := range []string{"fail", "timeout", "error"} {
		if got := GateResult(&store.GateRecord{Result: raw}); got != GateRed {
			t.Errorf("GateResult(%q) = %q, want %q", raw, got, GateRed)
		}
	}
}
