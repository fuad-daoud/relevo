package remote

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

func TestRoundStateOf(t *testing.T) {
	t.Parallel()

	// Arm 1: StateNeedsYou
	b1 := store.Binding{
		State: store.StateNeedsYou,
		Round: 1,
	}
	if got := RoundStateOf(b1, nil); got != RoundNeedsYou {
		t.Fatalf("arm 1 (needs_you): got %v, want %v", got, RoundNeedsYou)
	}

	// Arm 2: RoundRunning (plan sent, report not yet sent)
	b2 := store.Binding{
		State: store.StateActive,
		Round: 1,
	}
	entries2 := []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
	}
	if got := RoundStateOf(b2, entries2); got != RoundRunning {
		t.Fatalf("arm 2 (running): got %v, want %v", got, RoundRunning)
	}

	// Arm 3: RoundClosed (Serve.ClosedRound > Serve.AckedRound)
	b3 := store.Binding{
		State: store.StateActive,
		Round: 2,
		Serve: &store.ServeFacts{
			ClosedRound: 1,
			AckedRound:  0,
		},
	}
	// Entries has both plan and report for round 1
	entries3 := []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport},
	}
	if got := RoundStateOf(b3, entries3); got != RoundClosed {
		t.Fatalf("arm 3 (closed): got %v, want %v", got, RoundClosed)
	}

	// Arm 4: RoundIdle
	b4 := store.Binding{
		State: store.StateActive,
		Round: 2,
		Serve: &store.ServeFacts{
			ClosedRound: 1,
			AckedRound:  1,
		},
	}
	if got := RoundStateOf(b4, entries3); got != RoundIdle {
		t.Fatalf("arm 4 (idle): got %v, want %v", got, RoundIdle)
	}
}

// TestRoundStateOfBroken pins the broken arm: a binding whose builder is gone
// reports broken with its round open, and an un-acked close still outranks it,
// so a broken binding whose close the owner has not collected reports closed.
//
// Mutation check: delete the broken arm from RoundStateOf and the first case
// fails; the closed case passes either way.
func TestRoundStateOfBroken(t *testing.T) {
	t.Parallel()

	entries := []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
	}

	open := store.Binding{
		State: store.StateBroken,
		Round: 1,
	}
	if got := RoundStateOf(open, entries); got != RoundBroken {
		t.Fatalf("broken + open round: got %v, want %v", got, RoundBroken)
	}

	closed := store.Binding{
		State: store.StateBroken,
		Round: 2,
		Serve: &store.ServeFacts{
			ClosedRound: 1,
			AckedRound:  0,
		},
	}
	if got := RoundStateOf(closed, entries); got != RoundClosed {
		t.Fatalf("broken + un-acked close: got %v, want %v (close outranks broken)", got, RoundClosed)
	}
}

// TestRoundStateOfSwitchableBrokenReportsRunning pins the guard on the broken
// arm: only a break the server will not fix by itself goes on the wire as
// broken. A switchable one the server retries on its next tick is routine, and
// a laptop told broken halts with its own fallback reason and queues an entry
// for it -- advice the laptop cannot follow on a remote binding.
//
// Mutation check: drop the !BindingSwitchable(b) guard and this reads broken.
func TestRoundStateOfSwitchableBrokenReportsRunning(t *testing.T) {
	t.Parallel()

	entries := []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
	}

	switchable := store.Binding{
		State:            store.StateBroken,
		Round:            1,
		BuilderCandidate: "claude/first/m",
		RoundStartedAt:   time.Unix(1_700_000_000, 0),
		Builder:          store.Endpoint{PID: 4242},
	}
	if got := RoundStateOf(switchable, entries); got != RoundRunning {
		t.Fatalf("switchable broken: got %v, want %v: the daemon still fixes this one", got, RoundRunning)
	}

	// No process behind it: nothing retries the switch, so it is broken for
	// the owner.
	stuck := switchable
	stuck.Builder = store.Endpoint{}
	if got := RoundStateOf(stuck, entries); got != RoundBroken {
		t.Fatalf("broken with no process: got %v, want %v", got, RoundBroken)
	}
}

// TestRoundStateOfQueued pins that an open plan entry with a non-zero
// QueuedAt is queued, not running; zero QueuedAt is running as before; and
// needs_you still wins over queued, exactly as it wins over running.
//
// Mutation check: drop the `!b.QueuedAt.IsZero()` arm from RoundStateOf and
// this fails on the first case.
func TestRoundStateOfQueued(t *testing.T) {
	t.Parallel()

	entries := []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
	}

	queued := store.Binding{
		State:    store.StateActive,
		Round:    1,
		QueuedAt: time.Unix(1_700_000_000, 0),
	}
	if got := RoundStateOf(queued, entries); got != RoundQueued {
		t.Fatalf("open plan + QueuedAt set: got %v, want %v", got, RoundQueued)
	}

	running := store.Binding{
		State: store.StateActive,
		Round: 1,
	}
	if got := RoundStateOf(running, entries); got != RoundRunning {
		t.Fatalf("open plan + zero QueuedAt: got %v, want %v", got, RoundRunning)
	}

	needsYou := store.Binding{
		State:    store.StateNeedsYou,
		Round:    1,
		QueuedAt: time.Unix(1_700_000_000, 0),
	}
	if got := RoundStateOf(needsYou, entries); got != RoundNeedsYou {
		t.Fatalf("needs_you + QueuedAt set: got %v, want %v (needs_you wins)", got, RoundNeedsYou)
	}
}
