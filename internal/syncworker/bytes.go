package syncworker

// What this worker has moved, counted twice over: the replica's network traffic
// and the bodies in the bucket. Both are what a month of sync costs, and both
// are cumulative for this worker's life rather than for the replica's.
//
// They live apart from the log's own SQL because they are measured apart from
// it. The log's size is read in a query over the replica; these come from the
// engine's counters either side of a transfer, and from the byte counts the blob
// verbs already return.

import (
	"context"

	turso "turso.tech/database/tursogo"
)

// transfer runs one push or pull and folds what it moved into the cumulative
// counters, from the engine's own readings taken either side of it.
//
// A stats call that fails is not a reason to refuse the transfer: the bytes are
// being moved either way, and losing a measurement is better than losing the
// append or the pull that carried a user's work. The counters then stay where
// they were, and the next transfer's difference spans the gap -- which is
// right, because the engine's counter moved across both.
func (b *TursoBackend) transfer(ctx context.Context, move func(context.Context) error) error {
	before, haveBefore := b.engineBytes(ctx)
	if err := move(ctx); err != nil {
		return err
	}
	after, haveAfter := b.engineBytes(ctx)
	if haveBefore {
		b.lastEngine, b.haveLast = before, true
	}
	if !b.haveLast || !haveAfter {
		// With one end of the difference missing there is no transfer to measure,
		// and guessing would put a number in the month's total that no counter
		// supports. The next reading that does succeed spans the gap instead.
		return nil
	}
	sent, recv := bytesDelta(b.lastEngine, after)
	b.lastEngine = after
	b.bytes.TursoSent += sent
	b.bytes.TursoReceived += recv
	return nil
}

// engineBytes reads the engine's network counters. ok is false when the call
// failed, which is not the same as a zero reading: one says the replica moved
// nothing, the other says nobody could ask.
func (b *TursoBackend) engineBytes(ctx context.Context) (turso.TursoSyncDbStats, bool) {
	stats, err := b.driver.Stats(ctx)
	if err != nil {
		return turso.TursoSyncDbStats{}, false
	}
	return stats, true
}
