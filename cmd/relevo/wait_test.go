package main

import (
	"context"
	"testing"
)

func TestWaitFailureContextCanceled(t *testing.T) {
	t.Parallel()

	err := waitFailure(context.Canceled)
	ce, ok := err.(*cliError)
	if !ok {
		t.Fatalf("waitFailure(context.Canceled) = %T, want *cliError", err)
	}
	if ce.code != codeInternal {
		t.Errorf("code = %q, want %q", ce.code, codeInternal)
	}
	if ce.message != "wait was cancelled" {
		t.Errorf("message = %q, want %q", ce.message, "wait was cancelled")
	}
}
