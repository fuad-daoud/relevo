package config

import (
	"errors"
	"strings"
	"testing"
)

// TestSafeHookComponentRefusesEachRule pins the path rules one at a time, so
// removing one of them is a failing test rather than a rule that still happens
// to be caught by the directory check further along. A name that is not one
// plain component cannot be turned into a directory, whatever else refuses it.
func TestSafeHookComponentRefusesEachRule(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		want string
	}{
		{"", "empty"},
		{".", "directory name"},
		{"..", "directory name"},
		{"a/b", "slash"},
		{`a\b`, "backslash"},
		{"a\x00b", "NUL"},
		{strings.Repeat("a", maxHookComponent+1), "cap"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := SafeHookComponent(tc.name)
			if err == nil {
				t.Fatalf("SafeHookComponent(%q) accepted the name; want a refusal", tc.name)
			}
			if !errors.Is(err, ErrHookPath) {
				t.Errorf("SafeHookComponent(%q) = %v, want an ErrHookPath", tc.name, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("SafeHookComponent(%q) = %q, want it to say %q", tc.name, err, tc.want)
			}
		})
	}
}

// TestSafeHookComponentAcceptsPlainNames is the other half: the rule refuses
// what is not one component, and accepts what is.
func TestSafeHookComponentAcceptsPlainNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"a", "check.sh", "PreToolUse", "a-b_c.1", strings.Repeat("a", maxHookComponent)} {
		if err := SafeHookComponent(name); err != nil {
			t.Errorf("SafeHookComponent(%q) = %v, want it accepted", name, err)
		}
	}
}
