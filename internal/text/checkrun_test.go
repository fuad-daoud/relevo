package text_test

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/text"
)

// TestChainCheckRunLabel pins the line above a check log the gate section
// shows: the run number under a fixed label, so the fallback reads apart
// from the round's own gate log.
func TestChainCheckRunLabel(t *testing.T) {
	t.Parallel()

	if got, want := text.ChainCheckRunLabel(3), "chain check run 3"; got != want {
		t.Errorf("ChainCheckRunLabel(3) = %q, want %q", got, want)
	}
}

// TestCheckLogRun pins which round-file names parse as a round's chain check
// log: only NNN-check-RRR.log for that round. A served check log, another
// round's check log and names of any other shape are rejected.
func TestCheckLogRun(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		file  string
		round int
		run   int
		ok    bool
	}{
		{name: "check log of the round", file: "002-check-3.log", round: 2, run: 3, ok: true},
		{name: "served check log is not a check log", file: "002-served-check.log", round: 2, ok: false},
		{name: "another round's check log", file: "002-check-3.log", round: 1, ok: false},
		{name: "gate log", file: "002-gate.log", round: 2, ok: false},
		{name: "missing suffix", file: "002-check-3", round: 2, ok: false},
		{name: "non-numeric round", file: "abc-check-3.log", round: 2, ok: false},
		{name: "non-numeric run", file: "002-check-x.log", round: 2, ok: false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			run, ok := text.CheckLogRun(c.file, c.round)
			if ok != c.ok || (ok && run != c.run) {
				t.Errorf("CheckLogRun(%q, %d) = (%d, %v), want (%d, %v)", c.file, c.round, run, ok, c.run, c.ok)
			}
		})
	}
}

// TestNewestCheckRun pins which check log the gate fallback picks: the
// highest run number among the round's own logs only. Runs count across the
// chain, so a later round's higher run never wins for an earlier round, and
// no log at all yields run 0.
func TestNewestCheckRun(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		names []string
		round int
		want  int
	}{
		{
			name:  "highest run of the round wins",
			names: []string{"002-check-3.log", "002-check-5.log", "002-check-4.log"},
			round: 2,
			want:  5,
		},
		{
			name:  "another round's higher run is ignored",
			names: []string{"002-check-3.log", "003-check-9.log"},
			round: 2,
			want:  3,
		},
		{
			name:  "no check log for the round is zero",
			names: []string{"002-served-check.log", "003-check-9.log", "002-gate.log"},
			round: 2,
			want:  0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := text.NewestCheckRun(c.names, c.round); got != c.want {
				t.Errorf("NewestCheckRun(%v, %d) = %d, want %d", c.names, c.round, got, c.want)
			}
		})
	}
}
