package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// TestWriteRoundCapIsCoded pins the classification of a cap refusal: the write
// classifier renders a round_cap code and a next hint that is not the bug
// bundle, so a script can tell a spent cap from an internal failure.
func TestWriteRoundCapIsCoded(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("binding %q hit its round cap of %d: %w", "cc-w2", 20, relevo.ErrRoundCap)

	var ce *cliError
	if !errors.As(writeError(wrapped), &ce) {
		t.Fatalf("writeError(%v) is not a coded error", wrapped)
	}
	if ce.code != codeRoundCap {
		t.Errorf("code = %q, want %q", ce.code, codeRoundCap)
	}
	if ce.next == "" || strings.Contains(ce.next, "bugreport") {
		t.Errorf("next = %q, want a way out that is not the bug bundle", ce.next)
	}
	if !strings.HasPrefix(ce.next, "relevo ") {
		t.Errorf("next = %q, want a relevo command", ce.next)
	}
	if !errors.Is(ce, relevo.ErrRoundCap) {
		t.Errorf("coded error does not unwrap to ErrRoundCap")
	}

	var buf bytes.Buffer
	if code := report(&buf, writeError(wrapped), false); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if got := buf.String(); !strings.Contains(got, "round_cap:") || !strings.Contains(got, "next: relevo bind") {
		t.Errorf("report wrote %q, want the round_cap code and its next hint", got)
	}
}
