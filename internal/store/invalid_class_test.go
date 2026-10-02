package store

import (
	"errors"
	"strings"
	"testing"
)

// TestValidatorsCarryTheirClass pins the class each store validator's error
// carries, because the CLI maps on errors.Is and nothing else: a validator that
// returned a bare error would send a name or ticket the caller typed straight
// to internal, with `relevo bugreport` as the way out.
func TestValidatorsCarryTheirClass(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name  string
		class error
		err   error
	}{
		{"empty name", ErrInvalidName, ValidName("")},
		{"too long name", ErrInvalidName, ValidName(strings.Repeat("a", MaxAgentNameLen+1))},
		{"bad first letter", ErrInvalidName, ValidName("Plb")},
		{"upper case inside", ErrInvalidName, ValidName("plB")},
		{"leading digit", ErrInvalidName, ValidName("1plb")},
		{"empty feature", ErrInvalidFeature, ValidFeature("")},
		{"feature too long", ErrInvalidFeature, ValidFeature(strings.Repeat("a", 65))},
		{"feature bad char", ErrInvalidFeature, ValidFeature("a/b")},
		{"ticket without hash", ErrInvalidTicket, ValidTicket("12")},
		{"ticket bad number", ErrInvalidTicket, ValidTicket("#0")},
		{"parse ticket garbage", ErrInvalidTicket, mustTicketErr(t, "not a ticket")},
		{"parse ticket bad url number", ErrInvalidTicket, mustTicketErr(t, "owner/repo/issues/abc")},
	} {
		if c.err == nil {
			t.Errorf("%s: validator returned nil, want a refusal", c.name)
			continue
		}
		if !errors.Is(c.err, c.class) {
			t.Errorf("%s: errors.Is(%v, class) = false, want true", c.name, c.err)
		}
	}
}

// TestValidatorsKeepTheirMessage pins that classifying an error did not rewrite
// what the user reads: the message is the validator's own wording, with no
// class word appended by a %w tail.
func TestValidatorsKeepTheirMessage(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		want string
		got  error
	}{
		{"upper case inside", `binding name "plB" has an invalid character "B"`, ValidName("plB")},
		{"empty name", "binding name is empty", ValidName("")},
	} {
		if c.got.Error() != c.want {
			t.Errorf("%s: message = %q, want %q", c.name, c.got.Error(), c.want)
		}
	}

	// The class must not leak into a message that a test downstream compares.
	if got := ValidName("plB").Error(); strings.Contains(got, ErrInvalidName.Error()) {
		t.Errorf("message %q leaks the class %q", got, ErrInvalidName)
	}
}

// TestValidNamesCarryNoError keeps the happy path honest: a legal name is not
// made to fail by the class the wrapper added.
func TestValidNamesCarryNoError(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"plb", "a", "a_b-c9", strings.Repeat("a", MaxAgentNameLen)} {
		if err := ValidName(name); err != nil {
			t.Errorf("ValidName(%q) = %v, want nil", name, err)
		}
	}
}

// mustTicketErr is ParseTicket's error for a raw input, or a failure when the
// input unexpectedly parsed.
func mustTicketErr(t *testing.T, raw string) error {
	t.Helper()
	_, err := ParseTicket(raw, "")
	if err == nil {
		t.Fatalf("ParseTicket(%q) parsed, want a refusal", raw)
	}
	return err
}
