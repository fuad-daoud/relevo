package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/mastermind"
)

// TestWriteErrorUnknownMasterMindIsMastermindNotFound pins the write-path
// classification: an unknown mastermind ref is mastermind_not_found (exit 1),
// never internal. The env form's next names the unset; the --mastermind form
// keeps the catalog's listing hint. Dropping the case from writeError kills
// every row (the code becomes internal).
func TestWriteErrorUnknownMasterMindIsMastermindNotFound(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name    string
		err     error
		next    string
		wantVar string
	}{
		{"env RELEVO_MASTERMIND", mastermind.ErrUnknownMasterMindEnv{Var: "RELEVO_MASTERMIND", Ref: "nope"}, "unset RELEVO_MASTERMIND or relevo mastermind list", "RELEVO_MASTERMIND"},
		{"env RELEVO_PLANNER", mastermind.ErrUnknownMasterMindEnv{Var: "RELEVO_PLANNER", Ref: "nope"}, "unset RELEVO_PLANNER or relevo mastermind list", "RELEVO_PLANNER"},
		{"flag", mastermind.ErrUnknownMasterMind{Ref: "nope"}, "relevo mastermind list", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got := writeError(fmt.Errorf("bind: %w", c.err))
			coded, next, ok := codedOf(t, got)
			if !ok {
				t.Fatalf("writeError(%v) is not a coded error", c.err)
			}
			if coded != codeMastermindNotFound {
				t.Errorf("code = %q, want %q", coded, codeMastermindNotFound)
			}
			if next != c.next {
				t.Errorf("next = %q, want %q", next, c.next)
			}
			if e := catalogExit(coded); e != 1 {
				t.Errorf("exit = %d, want 1", e)
			}
			if msg := got.Error(); !strings.Contains(msg, `"nope"`) {
				t.Errorf("message %q must name the ref", msg)
			}
			if c.wantVar != "" {
				if msg := got.Error(); !strings.Contains(msg, c.wantVar) {
					t.Errorf("message %q must name the variable", msg)
				}
			}
		})
	}
}
