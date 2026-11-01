package syncworker

// What this worker has moved, counted twice over: the replica's network traffic
// and the bodies in the bucket. Both are what a month of sync costs, and both
// are cumulative for this worker's life rather than for the replica's.

import (
	"context"

	turso "turso.tech/database/tursogo"
)

// transfer runs one push or pull and folds what it moved into the cumulative
// counters, from the engine's own readings taken either side of it. A stats
// call that fails never refuses the transfer: losing a measurement is better
// than losing the append or pull that carried a user's work, and the next
// transfer's difference spans the gap.
func (b *TursoBackend) transfer(ctx context.Context, move func(context.Context) error) error {
	before, haveBefore := b.engineBytes(ctx)
	if err := move(ctx); err != nil {
		return err
	}
	after, haveAfter := b.engineBytes(ctx)
	if haveAfter {
		b.boundWAL(ctx, after)
	}
	if haveBefore {
		b.lastEngine, b.haveLast = before, true
	}
	if !b.haveLast || !haveAfter {
		// With one end of the difference missing there is no transfer to
		// measure, and the next reading that succeeds spans the gap instead.
		return nil
	}
	sent, recv := bytesDelta(b.lastEngine, after)
	b.lastEngine = after
	b.bytes.TursoSent += sent
	b.bytes.TursoReceived += recv
	return nil
}

// engineBytes reads the engine's network counters. ok is false when the call
// failed, which is not a zero reading: one says nobody could ask, the other
// says the replica moved nothing.
func (b *TursoBackend) engineBytes(ctx context.Context) (turso.TursoSyncDbStats, bool) {
	stats, err := b.driver.Stats(ctx)
	if err != nil {
		return turso.TursoSyncDbStats{}, false
	}
	return stats, true
}
