package delivery

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

func TestOriginLine(t *testing.T) {
	t.Parallel()

	t.Run("to runner byte-exact", func(t *testing.T) {
		got := OriginLine("b1", 2, store.DirToBuilder, store.KindPrompt)
		want := `relevo: round 2 · to runner "b1" · from the MasterMind (not the human)`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("to mastermind report byte-exact", func(t *testing.T) {
		got := OriginLine("b1", 2, store.DirToMasterMind, store.KindReport)
		want := `relevo: round 2 · to MasterMind · about runner "b1" (not the human)`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("to mastermind question byte-exact", func(t *testing.T) {
		got := OriginLine("b1", 3, store.DirToMasterMind, store.KindQuestion)
		want := `relevo: round 3 · to MasterMind · about runner "b1" (not the human)`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("to mastermind findings byte-exact", func(t *testing.T) {
		got := OriginLine("b1", 1, store.DirToMasterMind, store.KindFindings)
		want := `relevo: consult · to MasterMind · about runner "b1" (not the human)`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("to runner findings stays the runner line", func(t *testing.T) {
		got := OriginLine("b1", 2, store.DirToBuilder, store.KindFindings)
		want := `relevo: round 2 · to runner "b1" · from the MasterMind (not the human)`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

// TestOriginLineHaltDistinctReportUnchanged pins the origin split: a halt
// gets a line of its own, so a halt row cannot be mistaken for the report of
// the same round by a read-back that keys on the origin string, and the report's
// own line is left byte-identical.
//
// The substring constraint is the load-bearing half. opencode's read-back is a
// `LIKE '%origin%'`, so if either line contained the other the match would
// survive the split and the shadowing would come straight back.
func TestOriginLineHaltDistinctReportUnchanged(t *testing.T) {
	t.Parallel()

	halt := OriginLine("b1", 2, store.DirToMasterMind, store.KindHalt)
	report := OriginLine("b1", 2, store.DirToMasterMind, store.KindReport)

	t.Run("halt line is its own", func(t *testing.T) {
		want := `relevo: round 2 · halt · to MasterMind · about runner "b1" (not the human)`
		if halt != want {
			t.Errorf("halt origin = %q, want %q", halt, want)
		}
		if halt == report {
			t.Errorf("halt and report share one origin line %q", halt)
		}
	})

	t.Run("report line is byte-identical to before", func(t *testing.T) {
		want := `relevo: round 2 · to MasterMind · about runner "b1" (not the human)`
		if report != want {
			t.Errorf("report origin = %q, want the unchanged %q", report, want)
		}
	})

	t.Run("neither line contains the other", func(t *testing.T) {
		if strings.Contains(halt, report) {
			t.Errorf("the halt line %q contains the report line %q", halt, report)
		}
		if strings.Contains(report, halt) {
			t.Errorf("the report line %q contains the halt line %q", report, halt)
		}
	})

	t.Run("halt stays distinct from findings", func(t *testing.T) {
		findings := OriginLine("b1", 2, store.DirToMasterMind, store.KindFindings)
		if strings.Contains(halt, findings) || strings.Contains(findings, halt) {
			t.Errorf("halt %q and findings %q are not substring-distinct", halt, findings)
		}
	})

	t.Run("both halt lines end in the same suffix a LIKE must not cross", func(t *testing.T) {
		// A same-round report row and a same-round halt row are both real; the
		// query the read-back runs is only ever asked about one of them, and
		// the LIKE is on the whole line, so the two have to be told apart by the
		// whole line and nothing else.
		for _, kind := range []store.Kind{store.KindReport, store.KindHalt} {
			line := OriginLine("b1", 2, store.DirToMasterMind, kind)
			if !strings.HasPrefix(line, "relevo: round 2 · ") {
				t.Errorf("origin line for %q = %q, want the round prefix", kind, line)
			}
		}
	})
}

func TestWithOrigin(t *testing.T) {
	t.Parallel()

	origin := `relevo: round 1 · to MasterMind · about runner "b1" (not the human)`

	t.Run("plain payload inserts exactly one blank line", func(t *testing.T) {
		payload := "The runner finished round 1\nReport: /path/to/report"
		got := WithOrigin(payload, origin)
		want := origin + "\n\n" + payload
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("already prefixed returns unchanged", func(t *testing.T) {
		prefixed := origin + "\n\nThe runner finished round 1"
		got := WithOrigin(prefixed, origin)
		if got != prefixed {
			t.Fatalf("got %q, want %q", got, prefixed)
		}
	})

	t.Run("already prefixed with leading whitespace returns unchanged", func(t *testing.T) {
		prefixed := "  " + origin + "\n\nThe runner finished round 1"
		got := WithOrigin(prefixed, origin)
		if got != prefixed {
			t.Fatalf("got %q, want %q", got, prefixed)
		}
	})
}
