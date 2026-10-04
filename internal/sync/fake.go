package sync

import (
	"context"
)

// Fake is a SyncClient that records the order it was called in and serves
// scripted results, so a test can pin a call order with no remote in reach. The
// scripted fields are what the next call answers with; a zero Fake answers a
// successful call that changed nothing.
type Fake struct {
	// Calls is every method name in the order it was called.
	Calls []string
	// Applied is what Pull reports: whether remote changes were applied.
	Applied bool
	// Reported is what Stats returns.
	Reported Stats
	// PushErr, PullErr, StatsErr and CheckpointErr are what the matching call
	// fails with; nil is a success.
	PushErr       error
	PullErr       error
	StatsErr      error
	CheckpointErr error
}

var _ SyncClient = (*Fake)(nil)

// Push records the call and fails with PushErr.
func (f *Fake) Push(context.Context) error {
	f.Calls = append(f.Calls, "push")
	return f.PushErr
}

// Pull records the call, fails with PullErr, and otherwise reports whether it
// applied remote changes.
func (f *Fake) Pull(context.Context) (bool, error) {
	f.Calls = append(f.Calls, "pull")
	if f.PullErr != nil {
		return false, f.PullErr
	}
	return f.Applied, nil
}

// Stats records the call and answers with Reported, or fails with StatsErr.
func (f *Fake) Stats(context.Context) (Stats, error) {
	f.Calls = append(f.Calls, "stats")
	if f.StatsErr != nil {
		return Stats{}, f.StatsErr
	}
	return f.Reported, nil
}

// Checkpoint records the call and fails with CheckpointErr.
func (f *Fake) Checkpoint(context.Context) error {
	f.Calls = append(f.Calls, "checkpoint")
	return f.CheckpointErr
}
