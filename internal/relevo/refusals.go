package relevo

import (
	"errors"
	"fmt"
)

// ErrRefused reports a refusal: the caller's own arguments or files are wrong,
// so the command changed nothing and a corrected invocation is the way on. It
// is the input boundary's class -- internal is for relevo's own failures -- and
// the CLI maps it to the refused code and exit 2.
var ErrRefused = errors.New("refused")

// refusal is one refused input. Error() is the human wording alone, so the
// CLI's own "refused: " prefix never reads twice; Unwrap yields ErrRefused.
type refusal struct {
	msg string
}

func (r *refusal) Error() string { return r.msg }

func (r *refusal) Unwrap() error { return ErrRefused }

// refuse builds a refusal from a format and its arguments.
func refuse(format string, args ...any) error {
	return &refusal{msg: fmt.Sprintf(format, args...)}
}

// classedError is a refusal that belongs to a class other than ErrRefused: a
// server's answer re-typed locally, its message the server's own, its Unwrap
// the sentinel the CLI already knows (ErrUnknownRole and the like).
type classedError struct {
	msg   string
	class error
}

func (e *classedError) Error() string { return e.msg }

func (e *classedError) Unwrap() error { return e.class }

// classed rebuilds a server refusal's own message under a local sentinel.
func classed(class error, msg string) error {
	return &classedError{msg: msg, class: class}
}
