package relevo

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// syncBlackhole answers nothing until its context expires, which is what a
// network that accepts a connection and then goes silent looks like from here.
// block is set past any timeout under test, so the context always ends the
// call.
type syncBlackhole struct {
	calls atomic.Int64
	block time.Duration
}

func (b *syncBlackhole) wait(ctx context.Context) error {
	b.calls.Add(1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(b.block):
		return nil
	}
}

func (b *syncBlackhole) Push(ctx context.Context) error { return b.wait(ctx) }
func (b *syncBlackhole) Pull(ctx context.Context) (bool, error) {
	return false, b.wait(ctx)
}
func (b *syncBlackhole) Stats(ctx context.Context) (relevosync.Stats, error) {
	return relevosync.Stats{}, b.wait(ctx)
}
func (b *syncBlackhole) Checkpoint(ctx context.Context) error { return b.wait(ctx) }

// syncLocal opens a machine-local file for a runner to write its markers into,
// already carrying the marker that says sync is on.
func syncLocal(t *testing.T) *db.DB {
	t.Helper()
	shared, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local, err := relevosync.LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	if err := local.KVPut(relevosync.KeyEnabled, []byte(`true`)); err != nil {
		t.Fatalf("KVPut(enabled): %v", err)
	}
	return local
}

// waitSyncIdle waits for a queued sync to finish, so an assertion about what it
// wrote is made after it wrote it rather than racing it.
func waitSyncIdle(t *testing.T, d *Daemon) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		d.syncMu.Lock()
		busy := d.syncInFlight
		d.syncMu.Unlock()
		if !busy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("a queued sync never finished")
}

// sealableReader brings a bound reader round to the point where the daemon tick
// seals it: the round is two behind and its stream is drained. It returns the
// runtime with the sync seam already wired.
func sealableReader(t *testing.T, client relevosync.SyncClient) Runtime {
	t.Helper()
	repo := readerRepo(t)
	rt, b := bindReader(t, repo)

	writeReaderStream(t, rt, "reader-bind", 1, readerCloseFinal)
	touch(t, rt.Store.DonePath("reader-bind", 1))
	exitReaderRunner(t, rt, b)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got.Round = 3
	got.Builder.StreamRound = 0
	if err := rt.Store.Save(got); err != nil {
		t.Fatalf("Save: %v", err)
	}

	local := syncLocal(t)
	rt.Sync = &relevosync.Runner{Client: client, Local: local}
	return rt
}

// TestSyncNeverBlocksSeal is the load-bearing property of the whole seam: a
// seal commits its files and returns while the remote it is syncing to accepts
// the connection and goes silent. The round's outcome cannot depend on a
// network, so the tick is timed against a bound rather than merely checked for
// correctness.
func TestSyncNeverBlocksSeal(t *testing.T) {
	t.Parallel()

	b := &syncBlackhole{block: time.Minute}
	rt := sealableReader(t, b)

	start := time.Now()
	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick with a blackholed remote: %v", err)
	}
	took := time.Since(start)

	// Generous next to what a seal costs, and far below the runner's own
	// timeout: a seal that waited on the network would spend that whole
	// timeout here and blow straight through this.
	if took > 10*time.Second {
		t.Errorf("the tick took %v, so the seal waited on the network", took)
	}

	// The seal itself is unaffected: the files are in the database and gone
	// from disk, which is the whole of what sealing means.
	names, err := rt.Store.RoundFiles("reader-bind")
	if err != nil {
		t.Fatalf("RoundFiles: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("the seal wrote no round files")
	}
	if _, err := os.Stat(rt.Store.RunnerStreamPath("reader-bind", 1)); !os.IsNotExist(err) {
		t.Errorf("the sealed stream is still on disk: %v", err)
	}

	waitSyncIdle(t, NewDaemon(rt, time.Second))
}

// TestSyncSealQueuesOnePushThenPull pins the seal trigger itself: a seal that
// moved bytes drives exactly one bounded push-then-pull, in that order.
func TestSyncSealQueuesOnePushThenPull(t *testing.T) {
	t.Parallel()

	f := &relevosync.Fake{}
	rt := sealableReader(t, f)
	d := NewDaemon(rt, time.Second)

	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitSyncIdle(t, d)

	want := []string{"push", "pull", "stats"}
	if len(f.Calls) < len(want) {
		t.Fatalf("calls = %v, want at least %v", f.Calls, want)
	}
	for i, name := range want {
		if f.Calls[i] != name {
			t.Fatalf("calls = %v, want them to start with %v", f.Calls, want)
		}
	}
}

// TestSyncSealIsQuietWhenItSealedNothing pins the other half of the trigger: a
// tick that sealed no bytes has nothing new to hand another machine, so it does
// not drive the network at all.
func TestSyncSealIsQuietWhenItSealedNothing(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, _ := bindReader(t, repo)
	f := &relevosync.Fake{}
	local := syncLocal(t)
	rt.Sync = &relevosync.Runner{Client: f, Local: local}

	d := NewDaemon(rt, time.Second)
	// Sync is off as far as the idle window is concerned, so the only thing
	// that could drive the fake is the seal.
	if err := rt.Sync.Local.KVPut(relevosync.KeyEnabled, []byte(`false`)); err != nil {
		t.Fatalf("KVPut(enabled): %v", err)
	}
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitSyncIdle(t, d)

	if len(f.Calls) != 0 {
		t.Errorf("a tick that sealed nothing called the remote: %v", f.Calls)
	}
}

// TestSyncIdleTickRunsOncePerWindow pins the idle window: the first tick syncs,
// a tick inside the window does not, and a tick past it does again. The clock
// is injected so the window is tested rather than waited out.
func TestSyncIdleTickRunsOncePerWindow(t *testing.T) {
	t.Parallel()

	f := &relevosync.Fake{}
	local := syncLocal(t)
	now := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	d := NewDaemon(Runtime{Sync: &relevosync.Runner{Client: f, Local: local}}, time.Second)
	d.syncNow = func() time.Time { return now }

	d.idleSync(context.Background())
	waitSyncIdle(t, d)
	if got := len(f.Calls); got != 3 {
		t.Fatalf("calls after the first window = %d, want 3 (push, pull, stats)", got)
	}

	// A tick two seconds later is inside the window and does nothing at all.
	now = now.Add(2 * time.Second)
	d.idleSync(context.Background())
	waitSyncIdle(t, d)
	if got := len(f.Calls); got != 3 {
		t.Errorf("calls after a tick inside the window = %d, want 3", got)
	}

	// A tick past the window drives it again.
	now = now.Add(syncWindow)
	d.idleSync(context.Background())
	waitSyncIdle(t, d)
	if got := len(f.Calls); got != 6 {
		t.Errorf("calls after the window reopened = %d, want 6", got)
	}
}

// TestSyncIdleTickSkipsWhenThereIsNothingToDrive pins the skip paths: a machine
// with no seam wired and a machine whose marker says sync is off both cost the
// window nothing.
func TestSyncIdleTickSkipsWhenThereIsNothingToDrive(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// build returns the runtime the daemon runs with, so a row can wire no
		// seam at all as easily as a half-wired one.
		build func(t *testing.T) Runtime
	}{
		{
			name:  "no seam wired at all",
			build: func(*testing.T) Runtime { return Runtime{} },
		},
		{
			name: "no client behind the seam",
			build: func(t *testing.T) Runtime {
				return Runtime{Sync: &relevosync.Runner{Local: syncLocal(t)}}
			},
		},
		{
			name: "sync turned off by its marker",
			build: func(t *testing.T) Runtime {
				local := syncLocal(t)
				if err := local.KVPut(relevosync.KeyEnabled, []byte(`false`)); err != nil {
					t.Fatalf("KVPut(enabled): %v", err)
				}
				return Runtime{Sync: &relevosync.Runner{
					Client: &relevosync.Fake{},
					Local:  local,
				}}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rt := tc.build(t)
			var f *relevosync.Fake
			if rt.Sync != nil {
				f, _ = rt.Sync.Client.(*relevosync.Fake)
			}
			d := NewDaemon(rt, time.Second)

			d.idleSync(context.Background())
			waitSyncIdle(t, d)

			if f != nil && len(f.Calls) != 0 {
				t.Errorf("a skipped window called the remote: %v", f.Calls)
			}
			// Nothing ran, so no marker was written over the ones the fixture
			// set up.
			if rt.Sync != nil && rt.Sync.Local != nil {
				if _, ok, err := rt.Sync.Local.KVGet(relevosync.KeyLastTick); err != nil {
					t.Fatalf("KVGet(last tick): %v", err)
				} else if ok {
					t.Error("a skipped window wrote a tick marker")
				}
			}
		})
	}
}

// TestSyncWritesTheMarkersATickLeavesBehind pins the join of the two halves: a
// trigger runs the shared body, and the runner's markers are what the statusline
// will read back.
func TestSyncWritesTheMarkersATickLeavesBehind(t *testing.T) {
	t.Parallel()

	f := &relevosync.Fake{Reported: relevosync.Stats{CdcOperations: 3, Revision: "rev-1"}}
	local := syncLocal(t)
	d := NewDaemon(Runtime{Sync: &relevosync.Runner{Client: f, Local: local}}, time.Second)

	d.idleSync(context.Background())
	waitSyncIdle(t, d)

	token, err := relevosync.StatusToken(local)
	if err != nil {
		t.Fatalf("StatusToken: %v", err)
	}
	if token != relevosync.TokenOK {
		t.Errorf("token = %q, want %q", token, relevosync.TokenOK)
	}
}

// TestSyncDoesNotChangeTheSealStore pins that wiring the seam in changed
// nothing about sealing itself: the same rounds are considered and the same
// rows are written with no sync handle at all.
func TestSyncDoesNotChangeTheSealStore(t *testing.T) {
	t.Parallel()

	sealedWithout := sealAndCount(t, nil)
	sealedWith := sealAndCount(t, &relevosync.Fake{})

	if sealedWithout != sealedWith {
		t.Errorf("files sealed without a sync seam = %d, with one = %d", sealedWithout, sealedWith)
	}
	if sealedWith == 0 {
		t.Error("the fixture sealed nothing, so the comparison is vacuous")
	}
}

// sealAndCount runs one tick over a sealable round and returns how many files
// the seal moved into the database.
func sealAndCount(t *testing.T, client relevosync.SyncClient) int {
	t.Helper()
	repo := readerRepo(t)
	rt, b := bindReader(t, repo)

	writeReaderStream(t, rt, "reader-bind", 1, readerCloseFinal)
	touch(t, rt.Store.DonePath("reader-bind", 1))
	exitReaderRunner(t, rt, b)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got.Round = 3
	got.Builder.StreamRound = 0
	if err := rt.Store.Save(got); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if client != nil {
		rt.Sync = &relevosync.Runner{Client: client, Local: syncLocal(t)}
	}

	sealed := 0
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		loaded, err := tx.Load("reader-bind")
		if err != nil {
			return err
		}
		sealed = sealRounds(rt.Store, tx, loaded, rt.Policy.ArtifactMaxBytes())
		return nil
	}); err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	return sealed
}
