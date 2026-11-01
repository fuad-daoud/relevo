// Package board owns the board scenes `relevo board` serves: Excalidraw
// scenes and single-file HTML boards, in the repo scope and the live scope.
package board

import (
	"errors"
	"fmt"
)

// ErrUsage marks a refusal the command line reports as a usage error (exit 2).
var ErrUsage = errors.New("usage")

// usageError is a refusal whose message is the human text alone; it reports
// itself as ErrUsage to errors.Is.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }
func (e *usageError) Unwrap() error { return ErrUsage }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}
