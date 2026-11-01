package main

import (
	"context"
	"errors"
	"testing"
)

func TestWaitFailureContextCanceled(t *testing.T) {
	t.Parallel()

	err := waitFailure(context.Canceled)
	var ce *cliError
	if !errors.As(err, &ce) {
		t.Fatalf("waitFailure(context.Canceled) = %T, want *cliError", err)
	}
	if ce.code != codeInternal {
		t.Errorf("code = %q, want %q", ce.code, codeInternal)
	}
	if ce.message != "wait was cancelled" {
		t.Errorf("message = %q, want %q", ce.message, "wait was cancelled")
	}
}
