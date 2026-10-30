package refusal

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestNewUnwrapsToErrRefused(t *testing.T) {
	err := New("binding %q is missing its file", "b4")
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("New(...) = %v, want errors.Is ErrRefused", err)
	}
	if want := `binding "b4" is missing its file`; err.Error() != want {
		t.Fatalf("New(...).Error() = %q, want %q", err.Error(), want)
	}
}

func TestSentinelUnwrapsToItselfAndErrRefused(t *testing.T) {
	sentinel := Sentinel("the previous process is still running")
	wrapped := fmt.Errorf("send: %w", sentinel)
	if !errors.Is(wrapped, sentinel) {
		t.Fatalf("wrapped sentinel %v does not unwrap to itself", wrapped)
	}
	if !errors.Is(wrapped, ErrRefused) {
		t.Fatalf("wrapped sentinel %v does not unwrap to ErrRefused", wrapped)
	}
	if got := sentinel.Error(); got != "the previous process is still running" {
		t.Fatalf("Sentinel(...).Error() = %q, want the message alone", got)
	}
}

func TestBadInputfUnwrapsToErrBadInput(t *testing.T) {
	err := BadInputf("flag %q the binding does not honour", "--x")
	if !errors.Is(err, ErrBadInput) {
		t.Fatalf("BadInputf(...) = %v, want errors.Is ErrBadInput", err)
	}
	if errors.Is(err, ErrRefused) {
		t.Fatalf("BadInputf(...) = %v, must not unwrap to ErrRefused", err)
	}
	if want := `flag "--x" the binding does not honour`; err.Error() != want {
		t.Fatalf("BadInputf(...).Error() = %q, want %q", err.Error(), want)
	}
}

func TestClassedUnwrapsToItsClass(t *testing.T) {
	class := errors.New("unknown role")
	err := Classed(class, "server refused the role")
	if !errors.Is(err, class) {
		t.Fatalf("Classed(...) = %v, want errors.Is the class sentinel", err)
	}
	if errors.Is(err, ErrRefused) {
		t.Fatalf("Classed(...) = %v, must not unwrap to ErrRefused", err)
	}
	if got := err.Error(); got != "server refused the role" {
		t.Fatalf("Classed(...).Error() = %q, want the message alone", got)
	}
}

func TestRangefUnwrapsToErrRoundNotFound(t *testing.T) {
	err := Rangef("binding %q: round %d is out of range", "b4", 9)
	if !errors.Is(err, ErrRoundNotFound) {
		t.Fatalf("Rangef(...) = %v, want errors.Is ErrRoundNotFound", err)
	}
	if want := `binding "b4": round 9 is out of range`; err.Error() != want {
		t.Fatalf("Rangef(...).Error() = %q, want %q", err.Error(), want)
	}
}

func TestRoundOutOfRange(t *testing.T) {
	plain := RoundOutOfRange("b4", 9, 3, false)
	if !errors.Is(plain, ErrRoundNotFound) {
		t.Fatalf("RoundOutOfRange(...) = %v, want errors.Is ErrRoundNotFound", plain)
	}
	if want := `binding "b4": round 9 is out of range; binding has 3 rounds`; plain.Error() != want {
		t.Fatalf("RoundOutOfRange(...).Error() = %q, want %q", plain.Error(), want)
	}

	drift := RoundOutOfRange("b4", 9, 3, true)
	if !errors.Is(drift, ErrRoundNotFound) {
		t.Fatalf("RoundOutOfRange(drift) = %v, want errors.Is ErrRoundNotFound", drift)
	}
	if want := `binding "b4": round 9 is out of range (--drift); binding has 3 rounds`; drift.Error() != want {
		t.Fatalf("RoundOutOfRange(drift).Error() = %q, want %q", drift.Error(), want)
	}

	for _, err := range []error{plain, drift} {
		if strings.Contains(err.Error(), ErrRoundNotFound.Error()) {
			t.Fatalf("%q renders with the class word appended", err.Error())
		}
	}
}
