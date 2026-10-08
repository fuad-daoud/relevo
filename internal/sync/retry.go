package sync

// Retry is the operator's way out of a latched machine. The breaker stops a
// machine until a human acts, and this is that act: the latch goes, the death
// count starts over, and the worker is dropped so the next attempt starts a
// fresh process rather than driving the one that stopped answering.
func (r *Runner) Retry() error {
	if r == nil || r.Local == nil {
		return errNoSync
	}
	if err := NewBreaker(r.Local).Clear(); err != nil {
		return err
	}
	cancelWorker(r.Client)
	return nil
}
