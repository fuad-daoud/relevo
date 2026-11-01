package setup

import (
	"fmt"
	"io"
	"os"
)

// setupHint is the one stderr line a verb on an unconfigured machine speaks
// before it continues: the command the user ran fails on its own refusal
// anyway, so the line only misdirects when candidates exist.
const setupHint = "relevo is not set up yet: run relevo setup"

// setupHintStream is the hint's writer, swappable so a test can read it; nil
// is os.Stderr as it stands at print time.
var setupHintStream io.Writer

// needsSetupHint is the pure rule for the unconfigured-machine hint: no
// candidates section, and the verb is not one of the exempted.
func needsSetupHint(verb string, hasCandidates bool) bool {
	if hasCandidates {
		return false
	}
	switch verb {
	case "", "setup", "config", "help", "-h", "--help", "version", "-v", "--version", "doctor":
		return false
	default:
		return true
	}
}

// setupHintDue records that this invocation owes the hint; it prints after
// the verb's own output, so a failure's report stays first on stderr.
var setupHintDue bool

// NoteUnconfigured marks the hint due when the rule says so.
func NoteUnconfigured(verb string, hasCandidates bool) {
	if needsSetupHint(verb, hasCandidates) {
		setupHintDue = true
	}
}

func FlushHint() {
	if !setupHintDue {
		return
	}
	setupHintDue = false
	w := setupHintStream
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintln(w, setupHint)
}
