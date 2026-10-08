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
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The tests here pin what the daemon's sync triggers do now that no pipeline
// stands behind them: they are still called from the tick and from the seal,
// and they open nothing. A trigger that stopped being called would be a hole
// the next pipeline is wired into without anyone noticing, and a trigger that
// opened a handle would put a network back on a path the round exists to keep
// off it.

// recordTransport is the log transport a trigger test hands the runner, so a
// test can pin that a trigger drove nothing: it names every call it was driven
// through and answers each with a zero.
type recordTransport struct {
	Calls []string
}

func (r *recordTransport) Append([]synclog.Entry) ([]synclog.Entry, error) {
	r.Calls = append(r.Calls, "append")
	return nil, nil
}

func (r *recordTransport) Pull(map[string]int) ([]synclog.Entry, error) {
	r.Calls = append(r.Calls, "pull")
	return nil, nil
}

func (r *recordTransport) Head(string) ([]synclog.HeadRow, error) {
	r.Calls = append(r.Calls, "head")
	return nil, nil
}

func (r *recordTransport) Stats() (synclog.Stats, error) {
	r.Calls = append(r.Calls, "stats")
	return synclog.Stats{}, nil
}

// syncBlackhole accepts a call and then goes silent, which is what a network
// that accepts a connection and then stops answering looks like from here. It
// counts what was driven through it, so a trigger that reached it is visible
// rather than only slow. block is set past any deadline under test.
type syncBlackhole struct {
	calls atomic.Int64
	block time.Duration
}

func (b *syncBlackhole) wait() error {
	b.calls.Add(1)
	time.Sleep(b.block)
	return nil
}

func (b *syncBlackhole) Append([]synclog.Entry) ([]synclog.Entry, error) { return nil, b.wait() }
func (b *syncBlackhole) Pull(map[string]int) ([]synclog.Entry, error)    { return nil, b.wait() }
func (b *syncBlackhole) Head(string) ([]synclog.HeadRow, error)          { return nil, b.wait() }
func (b *syncBlackhole) Stats() (synclog.Stats, error)                   { return synclog.Stats{}, b.wait() }

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
func sealableReader(t *testing.T, client synclog.LogTransport) Runtime {
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
// seal commits its files and returns whether or not any remote is answering.
// The round's outcome cannot depend on a network, so the tick is timed against
// a bound rather than merely checked for correctness.
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
	if got := b.calls.Load(); got != 0 {
		t.Errorf("the tick called a blackholed remote %d times, want none", got)
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

// TestSyncSealOpensNoHandle pins the seal trigger itself: a seal that moved
// bytes drives nothing at all. The trigger is still on the path -- this runs it
// through the real tick -- and what it does now is open no handle.
//
// The mutation is putting the drive back: a trigger that handed the round to a
// client would record calls on the fake, and this is what would fail.
func TestSyncSealOpensNoHandle(t *testing.T) {
	t.Parallel()

	f := &recordTransport{}
	rt := sealableReader(t, f)
	d := NewDaemon(rt, time.Second)

	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitSyncIdle(t, d)

	if len(f.Calls) != 0 {
		t.Errorf("a seal that moved bytes drove %v, want nothing", f.Calls)
	}
	if _, ok, err := rt.Sync.Local.KVGet(relevosync.KeyLastTick); err != nil {
		t.Fatalf("KVGet(last tick): %v", err)
	} else if ok {
		t.Error("a seal wrote a tick marker")
	}
}

// TestSyncSealIsQuietWhenItSealedNothing pins the other half of the trigger: a
// tick that sealed no bytes has nothing new to hand another machine either, so
// it drives nothing as well.
func TestSyncSealIsQuietWhenItSealedNothing(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, _ := bindReader(t, repo)
	f := &recordTransport{}
	local := syncLocal(t)
	rt.Sync = &relevosync.Runner{Client: f, Local: local}

	d := NewDaemon(rt, time.Second)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitSyncIdle(t, d)

	if len(f.Calls) != 0 {
		t.Errorf("a tick that sealed nothing called the remote: %v", f.Calls)
	}
}

// TestSyncIdleTickIsAReachableNoOp pins the idle window from the tick's own
// side: the window is still on the tick, it is still callable, and it opens
// nothing. It is called directly here as well as through Tick so that a trigger
// removed from the tick would fail the reachability half and a handle added to
// it would fail the no-open half.
func TestSyncIdleTickIsAReachableNoOp(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		build func(t *testing.T) (*Daemon, *recordTransport, relevosync.Local)
	}{
		{
			name: "a runtime with a client behind the seam",
			build: func(t *testing.T) (*Daemon, *recordTransport, relevosync.Local) {
				f := &recordTransport{}
				local := syncLocal(t)
				return NewDaemon(Runtime{Sync: &relevosync.Runner{Client: f, Local: local}}, time.Second), f, local
			},
		},
		{
			name: "a runtime with no seam at all",
			build: func(*testing.T) (*Daemon, *recordTransport, relevosync.Local) {
				return NewDaemon(Runtime{}, time.Second), nil, nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, f, local := tc.build(t)

			d.idleSync(context.Background())
			waitSyncIdle(t, d)
			if f != nil && len(f.Calls) != 0 {
				t.Errorf("the idle window drove %v, want nothing", f.Calls)
			}
			if local != nil {
				if _, ok, err := local.KVGet(relevosync.KeyLastTick); err != nil {
					t.Fatalf("KVGet(last tick): %v", err)
				} else if ok {
					t.Error("the idle window wrote a tick marker")
				}
			}
		})
	}
}

// TestSyncTickWithVerbsOpensNoHandle pins the shared-runner half: a daemon whose
// owner serves the verbs still runs both triggers through Tick, and neither
// opens a handle on the runner the verbs share.
func TestSyncTickWithVerbsOpensNoHandle(t *testing.T) {
	t.Parallel()

	f := &recordTransport{}
	local := syncLocal(t)
	opens := 0
	runner := &VerbRunner{
		Local: local,
		Path:  filepath.Join(t.TempDir(), "relevo.db"),
		Open: func(context.Context) (synclog.LogTransport, error) {
			opens++
			return f, nil
		},
	}
	d := NewDaemon(Runtime{Sync: &relevosync.Runner{Client: f, Local: local}}, time.Second)
	d.SetSyncVerbs(runner)

	d.idleSync(context.Background())
	d.queueSync(context.Background())
	waitSyncIdle(t, d)

	if opens != 0 {
		t.Errorf("the tick opened %d handles, want none", opens)
	}
	if len(f.Calls) != 0 {
		t.Errorf("the tick drove %v, want nothing", f.Calls)
	}
}

// TestSyncDoesNotChangeTheSealStore pins that wiring the seam in changed
// nothing about sealing itself: the same rounds are considered and the same
// rows are written with no sync handle at all.
func TestSyncDoesNotChangeTheSealStore(t *testing.T) {
	t.Parallel()

	sealedWithout := sealAndCount(t, nil)
	sealedWith := sealAndCount(t, &recordTransport{})

	if sealedWithout != sealedWith {
		t.Errorf("files sealed without a sync seam = %d, with one = %d", sealedWithout, sealedWith)
	}
	if sealedWith == 0 {
		t.Error("the fixture sealed nothing, so the comparison is vacuous")
	}
}

// sealAndCount runs one tick over a sealable round and returns how many files
// the seal moved into the database.
func sealAndCount(t *testing.T, client synclog.LogTransport) int {
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
