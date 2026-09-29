package hooks

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// testRunLog returns a KVLog over a fresh temp database.
func testRunLog(t *testing.T) *KVLog {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return NewKVLog(db.TxKV{DB: d})
}

func TestKVLogAppendAndRead(t *testing.T) {
	log := testRunLog(t)
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	if err := log.Append(HookRun{At: at, Event: "state_changed", Argv: []string{"/bin/true"}, Output: "ok\n"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := log.Append(HookRun{At: at, Event: "round_started", Argv: []string{"/bin/false"}, ExitCode: 1, Error: "exit status 1"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	runs, err := log.Runs()
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("Runs returned %d entries, want 2", len(runs))
	}
	if runs[0].Event != "state_changed" || runs[0].Output != "ok\n" {
		t.Errorf("first run = %+v", runs[0])
	}
	if runs[1].Event != "round_started" || runs[1].Error != "exit status 1" || runs[1].ExitCode != 1 {
		t.Errorf("second run = %+v", runs[1])
	}
	if !runs[1].At.Equal(at) {
		t.Errorf("At = %v, want %v", runs[1].At, at)
	}
}

func TestKVLogCapsAt200(t *testing.T) {
	log := testRunLog(t)
	for i := 0; i < runLogCap+5; i++ {
		if err := log.Append(HookRun{At: time.Now().UTC(), Event: "e", Output: string(rune('a' + i%26))}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	runs, err := log.Runs()
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(runs) != runLogCap {
		t.Fatalf("Runs returned %d entries, want the cap %d", len(runs), runLogCap)
	}
}
