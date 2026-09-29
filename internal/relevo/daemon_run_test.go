package relevo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestRunReturnsErrReexecAfterOneTick pins §4.5: a hook that returns true ends
// Run with ErrReexec, and the hook is consulted exactly once -- there is no
// second tick after the decision.
func TestRunReturnsErrReexecAfterOneTick(t *testing.T) {
	t.Parallel()

	rt := Runtime{Store: store.New(t.TempDir())}

	calls := 0
	d := NewDaemon(rt, 10*time.Millisecond).WithUpgrade(func(context.Context) bool {
		calls++
		return true
	})

	if err := d.Run(context.Background()); !errors.Is(err, ErrReexec) {
		t.Fatalf("Run = %v, want ErrReexec", err)
	}
	if calls != 1 {
		t.Errorf("upgrade hook calls = %d, want 1", calls)
	}
}

// TestRunDrainsInFlightTickOnCancel pins §4.5's drain. A tick already running
// when ctx is cancelled finishes under a context that is not itself cancelled,
// Run then returns nil, and the upgrade hook is never consulted after
// cancellation.
func TestRunDrainsInFlightTickOnCancel(t *testing.T) {
	rt, _ := sentBinding(t)
	releaseStateRoot(t) // the release check composes its own path from the root
	fetch := &ctxRecordingFetcher{}
	rt.Fetcher = fetch

	entered := make(chan struct{})
	release := make(chan struct{})
	hookCalls := 0

	d := NewDaemon(rt, 10*time.Millisecond).
		WithRefresh(func(in Runtime) Runtime {
			close(entered)
			<-release
			return in
		}).
		WithUpgrade(func(context.Context) bool {
			hookCalls++
			return false
		})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	<-entered
	cancel()
	close(release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil after a drained tick", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the in-flight tick was released")
	}

	if !fetch.sawLiveCtx {
		t.Error("the tick's context was cancelled: Tick must run under context.WithoutCancel")
	}
	if hookCalls != 0 {
		t.Errorf("upgrade hook calls = %d, want 0 after cancellation", hookCalls)
	}
}

// ctxRecordingFetcher records whether the release check saw a live context,
// which is how the drain test observes context.WithoutCancel.
type ctxRecordingFetcher struct {
	sawLiveCtx bool
}

func (f *ctxRecordingFetcher) Latest(ctx context.Context) (string, error) {
	f.sawLiveCtx = ctx.Err() == nil
	return "", errors.New("no release in test")
}
