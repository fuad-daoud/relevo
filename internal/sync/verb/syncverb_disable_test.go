package verb

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// errHungPullReleased is what the hung pull returns once it is cancelled.
var errHungPullReleased = errors.New("test: the hung pull was released")

// hungEnablePull is a log whose pulls hang until it is cancelled, so a test
// can hold an enable's import open and watch a disable reach it. Every other
// call is recorded by the embedded fake.
type hungEnablePull struct {
	*recordTransport
	entered    chan struct{}
	cancel     chan struct{}
	enterOnce  sync.Once
	cancelOnce sync.Once
}

func (h *hungEnablePull) Pull(map[string]int) ([]synclog.Entry, error) {
	h.enterOnce.Do(func() { close(h.entered) })
	<-h.cancel
	return nil, errHungPullReleased
}

func (h *hungEnablePull) Cancel() {
	h.cancelOnce.Do(func() { close(h.cancel) })
}

// TestDisableTransportReachesTheEnableInFlight pins the disable's transport
// beside the runner: a disable issued while an enable runs reaches the
// enable's in-flight transport while the machine is still off, and an off
// machine with no enable opens none.
//
// The enable runs while the enabled mark is still off, so the disable has to
// reach the enable's own transport rather than the enabled-gated one. The
// daemon-guard half of the same contract -- the disable stopping the worker
// promptly and leaving the machine off -- lives in internal/relevo's daemon
// tests, which the verb package must not import.
func TestDisableTransportReachesTheEnableInFlight(t *testing.T) {
	f := newVerbFixture(t)
	if _, _, err := db.CompressHistoryOnce(f.shared, t.TempDir(), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("CompressHistoryOnce: %v", err)
	}
	hung := &hungEnablePull{
		recordTransport: &recordTransport{},
		entered:         make(chan struct{}),
		cancel:          make(chan struct{}),
	}
	f.runner.Open = func(context.Context) (synclog.LogTransport, error) { return hung, nil }

	ctx := context.Background()
	enableDone := make(chan *wire.SyncResult, 1)
	go func() {
		enableDone <- f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbEnable}, []byte(verbFixtureToken))
	}()
	select {
	case <-hung.entered:
	case res := <-enableDone:
		t.Fatalf("the enable settled before its pull: %+v", res)
	case <-time.After(10 * time.Second):
		t.Fatal("the enable did not reach its pull")
	}

	transport := f.runner.disableTransport(ctx)
	if transport == nil {
		t.Fatal("a disable during an enable reaches no transport")
	}
	if c := cancelTransport(transport); c == nil {
		t.Error("a disable during an enable reaches no cancel for the enable's transport")
	}

	hung.Cancel()
	<-enableDone

	// Off and no enable: nothing to reach, so the disable opens no transport.
	if f.runner.disableTransport(ctx) != nil {
		t.Error("an off machine with no enable still opened a transport")
	}
}
