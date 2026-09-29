package relevo

import (
	"testing"
	"time"
)

// TestWatchedNilSafety pins §3's contract: a nil *Watched is valid, has
// seen nothing, and Mark on it is a no-op -- which is what lets a CLI
// one-shot leave Runtime.Watched nil.
func TestWatchedNilSafety(t *testing.T) {
	t.Parallel()

	var w *Watched
	w.Mark(4242, 1_700_000_000) // must not panic
	if w.Seen(4242, 1_700_000_000) {
		t.Error("a nil *Watched must have seen nothing")
	}
	if w.Seen(0, 0) {
		t.Error("a nil *Watched must answer false")
	}
}

// TestWatchedMarkThenSeen pins the set's identity: it is the (pid,
// startedAt) pair that counts, so a reused pid is not the same process.
func TestWatchedMarkThenSeen(t *testing.T) {
	t.Parallel()

	w := NewWatched()
	if w.Seen(100, 5) {
		t.Fatal("a fresh Watched must have seen nothing")
	}
	w.Mark(100, 5)
	if !w.Seen(100, 5) {
		t.Error("Seen(pid, startedAt) must be true after Mark")
	}
	if w.Seen(100, 6) {
		t.Error("a different start time is a different process")
	}
	if w.Seen(101, 5) {
		t.Error("a different pid is a different process")
	}
}

// TestWatchedIgnoresZeroValueInputs pins the two inputs Mark must not
// record: a pid that cannot be real and a start time that was never
// measured. Recording either would make a later Seen answer for a process
// relevo cannot actually identify.
func TestWatchedIgnoresZeroValueInputs(t *testing.T) {
	t.Parallel()

	w := NewWatched()
	w.Mark(0, 1_700_000_000)
	w.Mark(-1, 1_700_000_000)
	w.Mark(100, 0)
	if len(w.seen) != 0 {
		t.Errorf("seen = %+v, want empty", w.seen)
	}
}

// TestLostToRestart is the table for the one lost-to-a-restart predicate
// (#370, spec §4.2). The last case is the #244 compatibility rule: a nil
// Watched has seen nothing, so it answers exactly as relevo did before this
// round.
func TestLostToRestart(t *testing.T) {
	t.Parallel()

	daemonStart := time.Unix(2_000_000_000, 0)
	before := daemonStart.Add(-time.Hour).Unix()
	after := daemonStart.Add(time.Hour).Unix()

	t.Run("a CLI process (zero StartedAt) never answers true", func(t *testing.T) {
		rt := Runtime{Watched: NewWatched()}
		if lostToRestart(rt, 1, before) {
			t.Error("lostToRestart must be false when rt.StartedAt is zero")
		}
	})

	t.Run("a process started after the daemon is not lost", func(t *testing.T) {
		rt := Runtime{StartedAt: daemonStart, Watched: NewWatched()}
		if lostToRestart(rt, 1, after) {
			t.Error("a process started after the daemon is not lost to a restart")
		}
	})

	t.Run("before the daemon and not seen is lost", func(t *testing.T) {
		rt := Runtime{StartedAt: daemonStart, Watched: NewWatched()}
		if !lostToRestart(rt, 1, before) {
			t.Error("a process started before the daemon and never seen is lost")
		}
	})

	t.Run("before the daemon but seen is not lost", func(t *testing.T) {
		w := NewWatched()
		w.Mark(1, before)
		rt := Runtime{StartedAt: daemonStart, Watched: w}
		if lostToRestart(rt, 1, before) {
			t.Error("a process this daemon saw alive is never lost")
		}
	})

	t.Run("a nil Watched is the #244 rule", func(t *testing.T) {
		rt := Runtime{StartedAt: daemonStart}
		if !lostToRestart(rt, 1, before) {
			t.Error("a nil Watched has seen nothing: a process started before the daemon is lost")
		}
	})

	t.Run("an unrecorded start time is never lost", func(t *testing.T) {
		rt := Runtime{StartedAt: daemonStart, Watched: NewWatched()}
		if lostToRestart(rt, 1, 0) {
			t.Error("a process with no recorded start time must not be judged lost")
		}
	})
}
