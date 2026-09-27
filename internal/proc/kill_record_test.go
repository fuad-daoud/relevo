//go:build unix

package proc

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

func TestParseKillRecord(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input     string
		wantPID   int
		wantStart int64
		wantOK    bool
	}{
		{"4242 1700000000\n", 4242, 1700000000, true},
		{"4242 1700000000", 4242, 1700000000, true}, // without newline
		{"99999999999 1700000000\n", 99999999999, 1700000000, true},
		{"", 0, 0, false},
		{"4242\n", 0, 0, false},
		{"4242 1700000000 extra\n", 0, 0, false},
		{"notanumber 1700000000\n", 0, 0, false},
		{"4242 notanumber\n", 0, 0, false},
		{"-1 1700000000\n", -1, 1700000000, true}, // negative start: parses but can never match a real handle
	}
	for _, c := range cases {
		pid, start, ok := parseKillRecord(c.input)
		if ok != c.wantOK {
			t.Errorf("parseKillRecord(%q): ok=%v, want %v", c.input, ok, c.wantOK)
			continue
		}
		if ok && (pid != c.wantPID || start != c.wantStart) {
			t.Errorf("parseKillRecord(%q): pid=%d start=%d, want pid=%d start=%d", c.input, pid, start, c.wantPID, c.wantStart)
		}
	}
}

// TestKilledProcessReadsUnknownEvenWhenTheStreamEndsInATrailer is the
// deterministic test that fails when the fix is reverted: Kill writes the
// record before the signal, so ExitCode returns ok=false even when a racing
// shell already appended a trailer.
func TestKilledProcessReadsUnknownEvenWhenTheStreamEndsInATrailer(t *testing.T) {
	r := New()
	r.KillGrace = 2 * time.Second
	h, _, stream := start(t, r, "sleep", "60")
	t.Cleanup(func() { _ = syscall.Kill(-h.PID, syscall.SIGKILL) })

	// Simulate what a racing shell leaves: partial content plus a trailer.
	if err := os.WriteFile(stream, []byte("partial\n"+spawn.ExitTrailer+"143\n"), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	if err := r.Kill(context.Background(), h, stream); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if _, err := os.Stat(killRecordPath(stream)); err != nil {
		t.Errorf("kill record not found after Kill: %v", err)
	}
	if _, ok := r.ExitCode(context.Background(), h, stream); ok {
		t.Error("ExitCode must be ok=false when a kill was recorded, even when the stream ends in a trailer")
	}
}

// TestKillOnADeadProcessRecordsNothing: Kill on a process that already exited
// returns nil and leaves no record, so its trailer still reads as its code.
func TestKillOnADeadProcessRecordsNothing(t *testing.T) {
	r := New()
	r.KillGrace = 2 * time.Second
	h, _, stream := start(t, r, "sh", "-c", "exit 7")
	waitGone(t, r, h, 5*time.Second)

	if err := r.Kill(context.Background(), h, stream); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if _, err := os.Stat(killRecordPath(stream)); err == nil {
		t.Error("kill record must not exist when Kill is called on an already-dead process")
	}
	code, ok := r.ExitCode(context.Background(), h, stream)
	if !ok || code != 7 {
		t.Errorf("ExitCode = %d, %v; want 7, true", code, ok)
	}
}

// TestASecondProcessOnTheSameStreamKeepsItsOwnTrailer: a first process is
// killed (its record stays), then a second process on the same stream exits
// normally. ExitCode for the second handle must read its own trailer (5, true)
// even though the kill record for the first handle is still beside the stream.
func TestASecondProcessOnTheSameStreamKeepsItsOwnTrailer(t *testing.T) {
	r := New()
	r.KillGrace = 2 * time.Second

	// First process: sleep, killed, record written.
	h1, _, stream := start(t, r, "sleep", "60")
	t.Cleanup(func() { _ = syscall.Kill(-h1.PID, syscall.SIGKILL) })
	if err := r.Kill(context.Background(), h1, stream); err != nil {
		t.Fatalf("Kill h1: %v", err)
	}

	// Second process: uses the same stream path, exits on its own.
	h2, err := r.Start(context.Background(), spawn.ProcSpec{
		Dir:        t.TempDir(),
		Argv:       []string{"sh", "-c", "exit 5"},
		LogPath:    stream + ".log",
		StreamPath: stream,
	})
	if err != nil {
		t.Fatalf("Start h2: %v", err)
	}
	waitGone(t, r, h2, 5*time.Second)

	// The kill record names h1, not h2, so it is ignored for h2.
	code, ok := r.ExitCode(context.Background(), h2, stream)
	if !ok || code != 5 {
		t.Errorf("ExitCode(h2) = %d, %v; want 5, true", code, ok)
	}
}

// TestABuilderKilledByASignalLeavesItsCode: a builder killed by a signal while
// its supervisor lives keeps its trailer -- the oom re-queue relies on this.
func TestABuilderKilledByASignalLeavesItsCode(t *testing.T) {
	r := New()
	r.KillGrace = 2 * time.Second
	// The supervisor writes the trailer for a builder killed by signal (137).
	h, _, stream := start(t, r, "sh", "-c", "kill -KILL $$")
	waitGone(t, r, h, 5*time.Second)

	code, ok := r.ExitCode(context.Background(), h, stream)
	if !ok || code != 137 {
		t.Errorf("ExitCode = %d, %v; want 137, true (builder killed by signal, supervisor writes trailer)", code, ok)
	}
}
