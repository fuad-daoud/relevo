package sync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// retryTransport is the log a retry test drives: it forwards to the in-memory
// log and counts the times the worker behind it was dropped, so a retry that
// reused the old process is visible rather than inferred.
type retryTransport struct {
	inner   synclog.LogTransport
	cancels int
}

func (r *retryTransport) Append(e []synclog.Entry) ([]synclog.Entry, error) {
	return r.inner.Append(e)
}

func (r *retryTransport) Pull(m map[string]int) ([]synclog.Entry, error) { return r.inner.Pull(m) }

func (r *retryTransport) Head(o string) ([]synclog.HeadRow, error) { return r.inner.Head(o) }

func (r *retryTransport) Stats() (synclog.Stats, error) { return r.inner.Stats() }

func (r *retryTransport) Cancel() { r.cancels++ }

// TestRetryClearsALatchedBreaker pins the operator's way out of a latched
// machine: the latch goes, the death count starts over, and the worker is
// dropped so the next tick starts a fresh process. A retry that left the latch
// or the worker in place would leave the machine refusing calls exactly as
// before.
func TestRetryClearsALatchedBreaker(t *testing.T) {
	t.Parallel()

	_, shared, local := openSplit(t)
	putMarker(t, local, KeyEnabled, `true`)
	b := NewBreaker(local)
	b.now = func() time.Time { return breakerNow }
	for i := 0; i < LatchAfter; i++ {
		missCall(t, b, local)
	}
	if latched, _ := b.Latched(); !latched {
		t.Fatal("the fixture did not latch the machine")
	}
	if err := NewBreaker(local).Begin("export"); !errors.Is(err, ErrLatched) {
		t.Fatalf("a call while latched = %v, want ErrLatched", err)
	}

	transport := &retryTransport{inner: synclog.NewMemTransport(shared.Origin())}
	runner := &Runner{Client: transport, Local: local}
	if err := runner.Retry(); err != nil {
		t.Fatalf("Retry: %v", err)
	}

	if latched, _ := b.Latched(); latched {
		t.Error("the latch survived the retry")
	}
	if got, _ := b.Deaths(); got != 0 {
		t.Errorf("deaths after the retry = %d, want the count started over", got)
	}
	if transport.cancels == 0 {
		t.Error("the retry reused the old worker instead of dropping it")
	}

	// The next tick runs: the breaker no longer refuses the call, and the
	// pipeline reaches the log.
	if err := NewBreaker(local).Begin("export"); err != nil {
		t.Fatalf("a call after the retry = %v, want the latch gone", err)
	}
	if out := runner.SyncOnce(context.Background(), shared); out.Err != nil {
		t.Fatalf("the tick after the retry failed: %v", out.Err)
	}
}
