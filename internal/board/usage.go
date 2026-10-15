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

// usagef builds that refusal.
func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}
