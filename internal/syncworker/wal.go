package syncworker

import (
	"context"
	"fmt"

	turso "turso.tech/database/tursogo"
)

// walCheckpointBytes is how large the replica's WAL may grow before a transfer
// folds it into the database file. The engine never checkpoints a sync replica
// by itself: left alone, a laptop's WAL reached 3.9 GB beside a 121 MB database
// in five days.
const walCheckpointBytes = 64 << 20

// boundWAL checkpoints the replica once the reading taken after a transfer
// shows its WAL past walCheckpointBytes. It runs after the transfer, never
// instead of it, and a failure is reported for the next transfer to retry:
// refusing the push or pull over a file that is merely large would lose the
// work it carried.
func (b *TursoBackend) boundWAL(ctx context.Context, reading turso.TursoSyncDbStats) {
	if reading.MainWalSize < walCheckpointBytes {
		return
	}
	if err := b.driver.Checkpoint(ctx); err != nil && b.warn != nil {
		_, _ = fmt.Fprintf(b.warn, "syncworker: checkpoint a %d-byte replica WAL: %v\n", reading.MainWalSize, err)
	}
}
