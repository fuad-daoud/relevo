package consult

import (
	"errors"
	"strings"
	"testing"
)

// TestStrandKeepsTheCauseWhenRecordingAlsoFails pins an error path that
// discarded the only useful half. When the process started but recording the
// consult failed, strand records the cause -- and if that record's Save ALSO
// failed, the save error was returned and the spawn failure, the half that
// explains what actually went wrong, was thrown away.
func TestStrandKeepsTheCauseWhenRecordingAlsoFails(t *testing.T) {
	t.Parallel()

	cause := errors.New(`start consult "webshop-reviewer-7f2a": spawn refused`)
	saveErr := errors.New("state dir is read-only")

	got := strandError(cause, saveErr)

	if !errors.Is(got, cause) {
		t.Errorf("strandError lost the cause: %v", got)
	}
	if !strings.Contains(got.Error(), "read-only") {
		t.Errorf("strandError lost the save failure: %v", got)
	}
}

// TestStrandReturnsTheBareCauseWhenRecordingSucceeds keeps the common path
// unwrapped: callers match on the spawn failure and the successful record adds
// nothing worth saying.
func TestStrandReturnsTheBareCauseWhenRecordingSucceeds(t *testing.T) {
	t.Parallel()

	cause := errors.New("spawn refused")

	if got := strandError(cause, nil); !errors.Is(got, cause) {
		t.Errorf("strandError(cause, nil) = %v, want the cause unchanged", got)
	}
}
