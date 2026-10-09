package relevo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// TestDisableStopsASteadyAttemptHoldingTheSlot pins the disable against the
// daemon's own guard while a steady attempt holds it: the attempt's pull hangs
// until its worker is cancelled and the step bound is a minute away, so a
// disable that waited for the slot instead of stopping the attempt would sit
// out that minute.
func TestDisableStopsASteadyAttemptHoldingTheSlot(t *testing.T) {
	f := newVerbFixture(t)
	d := NewDaemon(Runtime{Sync: f.runner.Runner}, time.Hour)
	f.runner.Serialize = d.WaitSyncSlot

	hung := newHangingJoin(relevosync.NewBreaker(f.local))
	f.runner.Runner.Client = hung
	f.runner.Runner.Timeout = time.Minute

	attempt := make(chan relevosync.SteadyResult, 1)
	go d.WaitSyncSlot(func() { attempt <- f.runner.Runner.SyncOnce(context.Background(), f.shared) })
	select {
	case <-hung.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the steady attempt never reached its pull")
	}

	done := make(chan *wire.SyncResult, 1)
	go func() {
		done <- f.runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbDisable}, nil)
	}()
	select {
	case res := <-done:
		if !res.OK {
			t.Fatalf("disable refused: %s", res.Message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the disable queued behind the steady attempt instead of stopping it")
	}
	if res := <-attempt; res.Err == nil {
		t.Error("the stopped attempt reported success")
	}
	if !hung.wasCancelled() {
		t.Error("the attempt's worker was never cancelled")
	}
}

// hungAppend is a log whose appends hang until the worker is cancelled, and
// whose next worker hangs the same way: a remote too slow to take the final
// export inside a disable's bound, behind a supervisor that restarts a
// cancelled worker for the next call.
type hungAppend struct {
	mu   sync.Mutex
	dead chan struct{}
}

func (h *hungAppend) worker() chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dead
}

func (h *hungAppend) Append([]synclog.Entry) ([]synclog.Entry, error) {
	<-h.worker()
	return nil, errors.New("test: the append was cancelled")
}
func (h *hungAppend) Pull(map[string]int) ([]synclog.Entry, error) { return nil, nil }
func (h *hungAppend) Head(string) ([]synclog.HeadRow, error)       { return nil, nil }
func (h *hungAppend) Stats() (synclog.Stats, error)                { return synclog.Stats{}, nil }

func (h *hungAppend) Cancel() {
	h.mu.Lock()
	close(h.dead)
	h.dead = make(chan struct{})
	h.mu.Unlock()
}

var _ synclog.LogTransport = (*hungAppend)(nil)

// TestDisableBoundsTheFinalExport pins the final export to the disable's own
// timeout: an export the remote never takes is stopped when the bound passes,
// reported as a warning, and the turn-off still completes, instead of the
// disable waiting out a data call's bound.
func TestDisableBoundsTheFinalExport(t *testing.T) {
	f := newVerbFixture(t)
	if _, err := f.shared.RecordPut(db.Record{
		Owner: "m1", Name: "webshop", State: "open", JSON: "{}",
		CreatedAt: time.Unix(0, 0).UTC(), UpdatedAt: time.Unix(0, 0).UTC(),
	}); err != nil {
		t.Fatalf("seed an owned row: %v", err)
	}
	if err := relevosync.MarkEnabled(f.local, true, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	f.runner.Runner.Client = &hungAppend{dead: make(chan struct{})}

	done := make(chan *wire.SyncResult, 1)
	go func() {
		done <- f.runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbDisable, TimeoutMS: 200}, nil)
	}()
	select {
	case res := <-done:
		if !res.OK {
			t.Fatalf("disable refused: %s", res.Message)
		}
		if res.Warning == "" {
			t.Error("a final export the bound stopped left no warning")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the disable waited on the final export past its own bound")
	}
	if on, err := relevosync.Enabled(f.local); err != nil || on {
		t.Errorf("Enabled = %v, %v; want the turn-off to finish", on, err)
	}
}

// heldJoin is a log whose first pull waits for the test to release it and
// answers normally once released, whatever a cancel did meanwhile: a join that
// sits between two calls when a disable lands. It counts the head reads that
// reach it, which is where the join goes next.
type heldJoin struct {
	entered   chan struct{}
	release   chan struct{}
	cancelled chan struct{}
	once      sync.Once
	stopOnce  sync.Once
	mu        sync.Mutex
	heads     int
}

func (h *heldJoin) Append(entries []synclog.Entry) ([]synclog.Entry, error) { return entries, nil }
func (h *heldJoin) Stats() (synclog.Stats, error)                           { return synclog.Stats{}, nil }
func (h *heldJoin) Cancel()                                                 { h.stopOnce.Do(func() { close(h.cancelled) }) }

func (h *heldJoin) Pull(map[string]int) ([]synclog.Entry, error) {
	h.once.Do(func() { close(h.entered) })
	<-h.release
	return nil, nil
}

func (h *heldJoin) Head(string) ([]synclog.HeadRow, error) {
	h.mu.Lock()
	h.heads++
	h.mu.Unlock()
	return nil, nil
}

func (h *heldJoin) headReads() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.heads
}

var _ synclog.LogTransport = (*heldJoin)(nil)

// TestDisableEndsAJoinBetweenItsCalls pins the join's stop as sticky: the
// disable lands while the join's pull is held, the pull then answers as a
// restarted worker would, and the join must end at its next call rather than
// go on to reconcile through the transport the disable stopped.
func TestDisableEndsAJoinBetweenItsCalls(t *testing.T) {
	f := newVerbFixture(t)
	if _, _, err := db.CompressHistoryOnce(f.shared, t.TempDir(), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("CompressHistoryOnce: %v", err)
	}
	d := NewDaemon(Runtime{Sync: f.runner.Runner}, time.Hour)
	f.runner.Serialize = d.WaitSyncSlot
	join := &heldJoin{entered: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan struct{})}
	f.runner.Open = func(context.Context) (synclog.LogTransport, error) { return join, nil }

	enabled := make(chan *wire.SyncResult, 1)
	go func() {
		enabled <- f.runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbEnable}, []byte(verbFixtureToken))
	}()
	select {
	case <-join.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the join never reached its pull")
	}

	disabled := make(chan *wire.SyncResult, 1)
	go func() {
		disabled <- f.runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbDisable}, nil)
	}()
	// The disable preempts before it waits for the slot. The join's stop is in
	// place before its worker is cancelled, so the cancel is the moment the
	// held pull may answer.
	select {
	case <-join.cancelled:
	case <-time.After(5 * time.Second):
		t.Error("the disable never cancelled the join's worker")
	}
	close(join.release)

	if res := <-enabled; res.OK {
		t.Error("the stopped enable reported success")
	}
	if res := <-disabled; !res.OK {
		t.Fatalf("disable refused: %s", res.Message)
	}
	if got := join.headReads(); got != 0 {
		t.Errorf("the join read head %d times after the stop, want it ended at its next call", got)
	}
	if on, err := relevosync.Enabled(f.local); err != nil || on {
		t.Errorf("Enabled = %v, %v; want the machine off", on, err)
	}
}

// heldPush is a log whose first append waits for the test to release it and
// whose later appends answer at once, whatever a cancel did meanwhile: a push
// of several batches that sits between two of them when a disable lands.
type heldPush struct {
	entered chan struct{}
	release chan struct{}
	stopped chan struct{}
	once    sync.Once
	mu      sync.Mutex
	appends int
	cancels int
}

func (h *heldPush) Append(entries []synclog.Entry) ([]synclog.Entry, error) {
	h.mu.Lock()
	h.appends++
	h.mu.Unlock()
	h.once.Do(func() { close(h.entered) })
	<-h.release
	return entries, nil
}
func (h *heldPush) Pull(map[string]int) ([]synclog.Entry, error) { return nil, nil }
func (h *heldPush) Head(string) ([]synclog.HeadRow, error)       { return nil, nil }
func (h *heldPush) Stats() (synclog.Stats, error)                { return synclog.Stats{}, nil }

// Cancel counts the worker cancels. The preempt cancels the runner's worker
// and then stops the push, whose stop cancels it again once the stop is in
// place, so the second cancel is the moment the held append may answer.
func (h *heldPush) Cancel() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cancels++
	if h.cancels == 2 {
		close(h.stopped)
	}
}

func (h *heldPush) appendCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.appends
}

var _ synclog.LogTransport = (*heldPush)(nil)

// TestDisableEndsAPushBetweenItsBatches pins the push verb to the same stop a
// steady attempt takes: a disable that lands while a push of several batches
// is held mid-way ends the push at its next batch instead of letting it drain
// the outbox through a fresh worker while the disable waits for the slot.
func TestDisableEndsAPushBetweenItsBatches(t *testing.T) {
	f := newVerbFixture(t)
	for i := range 300 {
		if _, err := f.shared.RecordPut(db.Record{
			Owner: "m1", Name: fmt.Sprintf("r%03d", i), State: "open", JSON: "{}",
			CreatedAt: time.Unix(0, 0).UTC(), UpdatedAt: time.Unix(0, 0).UTC(),
		}); err != nil {
			t.Fatalf("seed an owned row: %v", err)
		}
	}
	d := NewDaemon(Runtime{Sync: f.runner.Runner}, time.Hour)
	f.runner.Serialize = d.WaitSyncSlot
	log := &heldPush{entered: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{})}
	f.runner.Runner.Client = log

	pushed := make(chan *wire.SyncResult, 1)
	go func() {
		pushed <- f.runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbPush}, nil)
	}()
	select {
	case <-log.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the push never reached its first append")
	}

	disabled := make(chan *wire.SyncResult, 1)
	go func() {
		disabled <- f.runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbDisable}, nil)
	}()
	select {
	case <-log.stopped:
	case <-time.After(5 * time.Second):
		t.Error("the disable never stopped the push")
	}
	close(log.release)

	if res := <-pushed; res.OK {
		t.Error("the stopped push reported success")
	}
	if res := <-disabled; !res.OK {
		t.Fatalf("disable refused: %s", res.Message)
	}
	if got := log.appendCalls(); got != 1 {
		t.Errorf("the push made %d appends, want it ended after the one in flight", got)
	}
}
