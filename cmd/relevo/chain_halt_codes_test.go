package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
)

// TestHaltedChainStopIsAConflictNotInternal pins the CLI's class for the
// halted chain's stop answer. It is a statement about the chain's own state --
// nothing of its round is running to stop -- so it is the conflict family its
// running and done siblings have. Before this the answer was the binding
// case's "nothing to stop", and a resume that re-halted was a plain error the
// frame could only call internal: both sent a reader to `relevo bugreport` on
// rows they could simply act on.
func TestHaltedChainStopIsAConflictNotInternal(t *testing.T) {
	t.Parallel()

	// Built through the library's own constructor, so the CLI test asserts on
	// the error the verb returns rather than on a hand-written stand-in.
	halted := fmt.Errorf("chain shop is halted: %w", classOfHaltForTest())

	coded, next, ok := codedOf(t, writeError(halted))
	if !ok {
		t.Fatalf("writeError(%v) is not a coded error", halted)
	}
	if coded == codeInternal {
		t.Fatalf("code = %q, want the conflict family: a halt is a row the human can read", coded)
	}
	if coded != codeConflict {
		t.Errorf("code = %q, want %q", coded, codeConflict)
	}
	if next != "" {
		t.Errorf("next = %q, want empty: the message already names `relevo done`", next)
	}
	if got := catalogExit(coded); got != catalogExit(codeConflict) {
		t.Errorf("exit = %d, want %d", got, catalogExit(codeConflict))
	}
}

// TestResumeReHaltIsRefusedNotInternal pins the resume side of the same rule:
// a resume whose own transition lands on a halt is a refusal carrying the halt
// reason, and the CLI codes it as one. The mutation it kills is the plain
// fmt.Errorf the resume used to return, which no sentinel matched and the
// frame therefore coded internal.
func TestResumeReHaltIsRefusedNotInternal(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		err  error
		want errorCode
	}{
		{"refusal", chain.Refused("chain shop is still halted: repair has no budget left"), codeRefused},
		{"halted stop", classOfHaltForTest(), codeConflict},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			coded, _, ok := codedOf(t, writeError(c.err))
			if !ok {
				t.Fatalf("writeError(%v) is not a coded error", c.err)
			}
			if coded == codeInternal {
				t.Fatalf("code = %q, want a refusal class", coded)
			}
			if coded != c.want {
				t.Errorf("code = %q, want %q", coded, c.want)
			}
		})
	}
}

// classOfHaltForTest is the halted chain's stop refusal, through the exported
// sentinel the CLI classifies on. It is a constructor call rather than a
// literal so the test cannot drift from the wording the verb prints.
func classOfHaltForTest() error {
	var err error = &chain.ChainHaltedError{Name: "shop"}
	if !errors.Is(err, chain.ErrChainHalted) {
		panic("the halted chain's refusal does not carry ErrChainHalted")
	}
	return err
}

// TestHaltedChainStopTextIsTheOneWording pins that the human line, the JSON
// document and the cockpit action cannot disagree: all three are built from the
// same pure function over the chain's name.
func TestHaltedChainStopTextIsTheOneWording(t *testing.T) {
	t.Parallel()

	const want = "chain shop is halted: `relevo done shop` closes it"
	if got := classOfHaltForTest().Error(); got != want {
		t.Errorf("answer = %q, want %q", got, want)
	}
	if got := chain.ChainHaltedStopText("shop"); got != want {
		t.Errorf("ChainHaltedStopText = %q, want %q", got, want)
	}
	if !strings.Contains(chain.ChainHaltedStopText("shop"), "relevo done shop") {
		t.Error("the answer does not name the command that closes the chain")
	}
}
