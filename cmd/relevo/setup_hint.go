package main

import (
	"fmt"
	"io"
	"os"
)

// setupHint is the one stderr line a verb on an unconfigured machine speaks
// before it continues: the command the user ran fails on its own refusal
// anyway, so the line only misdirects when candidates exist.
const setupHint = "relevo is not set up yet: run relevo setup"

// activeVerb is the subcommand this invocation dispatched, read beside the
// runtime construction. The exempt verbs never build a runtime, so a verb
// that reaches one already needs config; the table below is the closed list
// of what never reaches one.
var activeVerb string

// setupHintStream is the hint's writer, swappable so a test can read it.
var setupHintStream io.Writer = os.Stderr

// needsSetupHint is the pure rule for the unconfigured-machine hint: no
// candidates section, and the verb is not one of the five exempted. help and
// version spell their aliases close by so the table reads complete.
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

// printUnconfiguredHint prints the hint once per invocation, always to
// stderr: stdout stays the result document the caller asked for, with and
// without --json.
func printUnconfiguredHint(verb string, hasCandidates bool) {
	if !needsSetupHint(verb, hasCandidates) {
		return
	}
	fmt.Fprintln(setupHintStream, setupHint)
}
