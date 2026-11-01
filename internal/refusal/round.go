package refusal

import (
	"errors"
	"fmt"
)

// ErrRoundNotFound is the class of every out-of-range --round refusal: the caller
// named a round the binding does not have. It is distinct from ErrNoCompletedRound,
// which reports that NO round is readable yet rather than that the one asked for
// is absent; the CLI maps it to round_not_found (exit 1) instead of internal.
var ErrRoundNotFound = errors.New("round not found")

// roundOutOfRange is an out-of-range round carrying the ErrRoundNotFound class while
// rendering only its own message: the CLI classifies on errors.Is, so the message
// the user reads stays exactly as written.
type roundOutOfRange struct {
	msg string
}

func (e *roundOutOfRange) Error() string { return e.msg }

func (e *roundOutOfRange) Unwrap() error { return ErrRoundNotFound }

// Rangef builds the out-of-range round error from a format and arguments, the
// class coming from Unwrap rather than the message.
func Rangef(format string, args ...any) error {
	return &roundOutOfRange{msg: fmt.Sprintf(format, args...)}
}

// RoundOutOfRange names the binding, the round asked for and the rounds it has,
// with the --drift marker when drift was asked for.
func RoundOutOfRange(name string, round, rounds int, drift bool) error {
	if drift {
		return Rangef("binding %q: round %d is out of range (--drift); binding has %d rounds", name, round, rounds)
	}
	return Rangef("binding %q: round %d is out of range; binding has %d rounds", name, round, rounds)
}
