package syncworker

import (
	"context"
	"fmt"

	turso "turso.tech/database/tursogo"
)

// walCheckpointBytes is how large the replica's WAL may grow before a transfer
// folds it into the database file. The engine never checkpoints a sync replica
// by itself: left alone, a laptop's WAL reached 3.9 GB beside a 121 MB
// database in five days. At 64 MiB a checkpoint runs once every many transfers,
// and the file stays a small multiple of one transfer.
const walCheckpointBytes = 64 << 20

// boundWAL checkpoints the replica once the reading taken after a transfer
// shows its WAL past walCheckpointBytes. It runs after the transfer, never
// instead of it: the engine serialises push, pull and checkpoint on one lock,
// and the worker serves one verb at a time, so no transaction is open across
// it. A checkpoint that fails is reported and left for the next transfer to
// retry, because the push or pull before it already succeeded, and refusing
// that over a file that is merely large would lose the work it carried.
func (b *TursoBackend) boundWAL(ctx context.Context, reading turso.TursoSyncDbStats) {
	if reading.MainWalSize < walCheckpointBytes {
		return
	}
	if err := b.driver.Checkpoint(ctx); err != nil && b.warn != nil {
		_, _ = fmt.Fprintf(b.warn, "syncworker: checkpoint a %d-byte replica WAL: %v\n", reading.MainWalSize, err)
	}
}
