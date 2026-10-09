package main

import (
	"errors"
	"path/filepath"
	"testing"
)

// countFreshen replaces the hint sender with a counter for one test, so no
// test dials a daemon.
func countFreshen(t *testing.T) *int {
	t.Helper()
	n := 0
	prev := freshenHint
	freshenHint = func() { n++ }
	t.Cleanup(func() { freshenHint = prev })
	return &n
}

func TestShouldSendFreshen(t *testing.T) {
	for _, tc := range []struct {
		name string
		line bool
		err  error
		want bool
	}{
		{"full read", false, nil, true},
		{"statusline", true, nil, false},
		{"failed read", false, errors.New("boom"), false},
		{"failed statusline", true, errors.New("boom"), false},
	} {
		if got := shouldSendFreshen(tc.line, tc.err); got != tc.want {
			t.Errorf("%s: shouldSendFreshen = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestStatuslineSendsNoFreshenHint(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	sent := countFreshen(t)

	_, _, _ = captureOutput(t, func() error { return run([]string{"status", "--line"}) })
	_, _, _ = captureOutput(t, func() error { return run([]string{"status", "--line", "--json"}) })

	if *sent != 0 {
		t.Errorf("the statusline sent %d freshen hints, want none", *sent)
	}
}

func TestFullStatusSendsOneFreshenHint(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	sent := countFreshen(t)

	_, _, _ = captureOutput(t, func() error { return run([]string{"status"}) })

	if *sent != 1 {
		t.Errorf("a full status sent %d freshen hints, want 1", *sent)
	}
}
