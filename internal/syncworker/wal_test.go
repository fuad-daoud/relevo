package syncworker

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	turso "turso.tech/database/tursogo"
)

// TestTransferCheckpointsOnlyAWALPastTheBound pins when a transfer folds the
// replica's WAL into its file: once the reading after it shows the WAL at the
// bound, and never below it, so a busy worker does not checkpoint every tick.
func TestTransferCheckpointsOnlyAWALPastTheBound(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		wal  int64
		want int
	}{
		{"empty", 0, 0},
		{"just under the bound", walCheckpointBytes - 1, 0},
		{"at the bound", walCheckpointBytes, 1},
		{"far past it", 4 << 30, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeSyncDb{}
			driver := newTursoDriver("")
			driver.sdb = fake
			backend := &TursoBackend{driver: driver}
			if err := backend.transfer(t.Context(), func(context.Context) error {
				fake.reading = turso.TursoSyncDbStats{MainWalSize: tc.wal}
				return nil
			}); err != nil {
				t.Fatalf("transfer: %v", err)
			}
			if fake.checkpoints != tc.want {
				t.Errorf("checkpoints = %d after a %d-byte WAL, want %d", fake.checkpoints, tc.wal, tc.want)
			}
		})
	}
}

// TestAFailedCheckpointKeepsTheTransfer pins that a checkpoint failing does not
// turn the push or pull before it into a refusal: the daemon would keep
// re-sending an append the remote already holds. The failure goes to the
// daemon's log instead.
func TestAFailedCheckpointKeepsTheTransfer(t *testing.T) {
	t.Parallel()
	fake := &fakeSyncDb{checkpointErr: errors.New("database is locked")}
	driver := newTursoDriver("")
	driver.sdb = fake
	var log bytes.Buffer
	backend := &TursoBackend{driver: driver, warn: &log}
	if err := backend.transfer(t.Context(), func(context.Context) error {
		fake.reading = turso.TursoSyncDbStats{MainWalSize: walCheckpointBytes}
		return nil
	}); err != nil {
		t.Fatalf("a transfer whose checkpoint failed was refused: %v", err)
	}
	if fake.checkpoints != 1 {
		t.Fatalf("checkpoints = %d, want 1", fake.checkpoints)
	}
	if !strings.Contains(log.String(), "database is locked") {
		t.Errorf("the failed checkpoint was not reported: %q", log.String())
	}
}

// TestAppendAndPullBoundTheReplicaWAL pins the wiring: both verbs that move the
// replica over the wire check its WAL afterwards, since an append grows it with
// this machine's entries and a pull with every other machine's.
func TestAppendAndPullBoundTheReplicaWAL(t *testing.T) {
	driver := &localDriver{path: tempReplica(t)}
	b := backendFor(t, driver, "origin-a")
	driver.engineBytes = turso.TursoSyncDbStats{MainWalSize: 1 << 30}

	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["b"]`, `{"title":"one"}`)}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if driver.checkpoints != 1 {
		t.Errorf("checkpoints after an append = %d, want 1", driver.checkpoints)
	}
	if _, err := b.Pull(map[string]int{}); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if driver.checkpoints != 2 {
		t.Errorf("checkpoints after a pull = %d, want 2", driver.checkpoints)
	}
}
