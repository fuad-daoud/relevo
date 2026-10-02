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

// sentinelled is a refusal that also names the specific reason it was
// refused, so the CLI can classify it as a refusal AND a caller probing the
// specific sentinel with errors.Is still finds it. The two sentinels that
// name a busy round are built from it, which is why a %w wrap of them at the
// call site carries ErrRefused without the call site naming it twice.
type sentinelled struct {
	msg string
}

func (s *sentinelled) Error() string { return s.msg }

func (s *sentinelled) Unwrap() []error { return []error{ErrRefused} }

// refusalSentinel builds a refusal sentinel whose message is the reason.
func refusalSentinel(msg string) error {
	return &sentinelled{msg: msg}
}

// ErrBadInput is the class of a refusal about the caller's own arguments: a
// mutually exclusive flag pair, a flag the binding's shape does not honour, a
// missing working directory, a name already taken. Nothing was created and a
// corrected invocation is the whole way out, so the CLI maps it to usage
// (exit 2) with `relevo help` -- distinct from ErrRefused, which covers a
// refusal about the state of a thing rather than about the shape of the call.
var ErrBadInput = errors.New("invalid input")

// badInput is ErrBadInput carrying the call site's own message, so the CLI can
// classify on errors.Is without a %w tail appending the class word to the
// wording the user reads.
type badInput struct {
	msg string
}

func (e *badInput) Error() string { return e.msg }

func (e *badInput) Unwrap() error { return ErrBadInput }

// badInputf builds a caller-input refusal from a format and its arguments.
func badInputf(format string, args ...any) error {
	return &badInput{msg: fmt.Sprintf(format, args...)}
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
