package relevo

import (
	"errors"
	"fmt"
)

// The answer a stop gives a chain that has halted with no open member round.
// It is its own class rather than ErrNothingToStop: there really is nothing to
// stop, but the useful thing to say is why the verb cannot do anything and what
// closes the chain instead -- the answer a human needs is `relevo done`, not a
// member to go and stop. A stopped chain keeps the plain nothing-to-stop
// answer: it has already been ended, and `relevo done` is a release rather
// than the way out of a halt.
var ErrChainHalted = errors.New("the chain is halted")

// ChainHaltedStopText is the halted chain's stop answer, built from the name so
// the human line, the JSON document and the cockpit action read the same bytes
// rather than each spelling it out.
func ChainHaltedStopText(name string) string {
	return fmt.Sprintf("chain %s is halted: `relevo done %s` closes it", name, name)
}

// ChainHaltedError is the halted chain's stop refusal, carrying the chain's
// name so the wording is built in one place. It is exported so a caller that
// classifies the refusal -- the CLI's own tests, a script -- can build the
// exact error the verb returns rather than a stand-in that can drift from it.
type ChainHaltedError struct {
	// Name is the halted chain's own name.
	Name string
}

// Error renders the answer the chain's name gives.
func (e *ChainHaltedError) Error() string { return ChainHaltedStopText(e.Name) }

// Unwrap yields ErrChainHalted, the class the CLI maps to the conflict family.
func (e *ChainHaltedError) Unwrap() error { return ErrChainHalted }

// haltedChainStop builds the refusal a stop makes on a halted chain, carrying
// ErrChainHalted so the CLI maps it to the conflict family an answer about the
// chain's own state belongs to.
func haltedChainStop(name string) error {
	return &ChainHaltedError{Name: name}
}

// Refused builds a refusal from a format and its arguments, the same
// constructor the library's own refusals use. It is exported so a caller
// outside this package can assert on the class a refusal is classified under
// without reaching for an unexported helper or a hand-rolled stand-in.
func Refused(format string, args ...any) error { return refuse(format, args...) }
