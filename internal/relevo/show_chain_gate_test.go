package relevo

import (
	"context"
	"os"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// putCheckLog seeds one chain check run's log the way a check's end leaves
// it: a round_file row, with no file on disk.
func putCheckLog(t *testing.T, s *store.Store, name string, round, run int, body string) {
	t.Helper()
	if err := s.WithLock(func(tx *store.Tx) error {
		return tx.PutRoundFile(name, round, s.CheckLogPath(name, round, run), []byte(body))
	}); err != nil {
		t.Fatalf("PutRoundFile check run %d: %v", run, err)
	}
}

// TestShowGateReadsASealedChainCheckLog pins the gate section's fallback: a
// round whose only gate output is a sealed chain check log -- held as run 3,
// with no runs 1 or 2 for that round -- is shown with its run label. The
// missing lower runs are the point: the round's files are listed, not probed
// from run 1.
func TestShowGateReadsASealedChainCheckLog(t *testing.T) {
	t.Parallel()

	s := newShowLiveStore(t)
	putCheckLog(t, s, "fixture", 2, 3, "check run 3 failed\n")
	rt := Runtime{Store: s}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 2, Section: ShowGate})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Missing {
		t.Fatal("Missing = true, want the round's chain check log")
	}
	want := "chain check run 3\ncheck run 3 failed\n"
	if res.Text != want {
		t.Errorf("Text = %q, want %q", res.Text, want)
	}
}

// TestShowGateShowsTheNewestCheckRun pins which check log the fallback picks
// with several runs on one round: the highest run number, the attempt the
// round was left at.
func TestShowGateShowsTheNewestCheckRun(t *testing.T) {
	t.Parallel()

	s := newShowLiveStore(t)
	putCheckLog(t, s, "fixture", 2, 3, "check run 3 failed\n")
	putCheckLog(t, s, "fixture", 2, 5, "check run 5 failed\n")
	rt := Runtime{Store: s}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 2, Section: ShowGate})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Missing {
		t.Fatal("Missing = true, want the newest chain check log")
	}
	want := "chain check run 5\ncheck run 5 failed\n"
	if res.Text != want {
		t.Errorf("Text = %q, want %q", res.Text, want)
	}
}

// TestShowGateStillMissingWithoutGateOrCheckLog pins that a round with no
// gate log and no check log keeps the section's missing answer, and that
// another round's check log is not shown for it.
func TestShowGateStillMissingWithoutGateOrCheckLog(t *testing.T) {
	t.Parallel()

	s := newShowLiveStore(t)
	putCheckLog(t, s, "fixture", 2, 5, "check run 5 failed\n")
	rt := Runtime{Store: s}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 1, Section: ShowGate})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !res.Missing {
		t.Errorf("Missing = false, want the section missing (Text = %q)", res.Text)
	}
	if res.Text != "" {
		t.Errorf("Text = %q, want empty", res.Text)
	}
}

// TestShowGatePrefersTheRoundGateLog pins that a round with a gate log of its
// own is answered from it, even when the round also holds a chain check log.
func TestShowGatePrefersTheRoundGateLog(t *testing.T) {
	t.Parallel()

	s := newShowLiveStore(t)
	putCheckLog(t, s, "fixture", 2, 5, "check run 5 failed\n")
	if err := os.WriteFile(s.GateLogPath("fixture", 2), []byte("gate output\n"), 0o644); err != nil {
		t.Fatalf("write the gate log: %v", err)
	}
	rt := Runtime{Store: s}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 2, Section: ShowGate})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Missing {
		t.Fatal("Missing = true, want the round's own gate log")
	}
	if res.Text != "gate output\n" {
		t.Errorf("Text = %q, want %q", res.Text, "gate output\n")
	}
}
