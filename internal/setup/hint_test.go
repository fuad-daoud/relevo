package setup

import (
	"bytes"
	"testing"
)

// TestUnconfiguredHintOnStderrOnly pins the unconfigured-machine hint: a
// config-needing verb speaks the exact one line on stderr, whether the caller
// asked for --json or not, an exempt verb stays silent, and candidates
// present silence it too.
func TestUnconfiguredHintOnStderrOnly(t *testing.T) {
	for _, c := range []struct {
		verb  string
		hints bool
	}{
		{"", false},
		{"setup", false},
		{"config", false},
		{"help", false},
		{"-h", false},
		{"--help", false},
		{"version", false},
		{"-v", false},
		{"--version", false},
		{"doctor", false},
		{"status", true},
		{"bind", true},
		{"daemon", true},
	} {
		if got := needsSetupHint(c.verb, false); got != c.hints {
			t.Errorf("needsSetupHint(%q, no candidates) = %v, want %v", c.verb, got, c.hints)
		}
		// A configured machine never needs the hint, exempt verb or not.
		if got := needsSetupHint(c.verb, true); got {
			t.Errorf("needsSetupHint(%q, candidates) = true, want false", c.verb)
		}
	}

	old := setupHintStream
	t.Cleanup(func() { setupHintStream = old })
	buf := &bytes.Buffer{}

	setupHintStream = buf
	t.Cleanup(func() { setupHintDue = false })
	NoteUnconfigured("status", false)
	FlushHint()
	if got := buf.String(); got != setupHint+"\n" {
		t.Errorf("hint = %q, want the exact line", got)
	}

	buf.Reset()
	NoteUnconfigured("status", true)
	FlushHint()
	if buf.Len() != 0 {
		t.Errorf("a configured verb printed %q", buf.String())
	}

	buf.Reset()
	NoteUnconfigured("doctor", false)
	FlushHint()
	if buf.Len() != 0 {
		t.Errorf("doctor printed %q", buf.String())
	}
}
