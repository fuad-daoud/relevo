package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The tests here drive the whole tick rather than the phase: a trigger removed
// from it fails them. What they pin is that the seal and the idle window each
// hand the runner to the steady pipeline, that the seal path never waits on the
// network, and that a worker which ran past its step is cancelled rather than
// driven again.

// recordTransport is the log transport a fixture hands a runner: it answers
// every call with a zero, so a test can look at what was driven through it.
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

// syncBlackhole accepts a call and then goes silent until it is cancelled,
// which is what a network that accepts a connection and then stops answering
// looks like from here. It counts what was driven through it, so a trigger that
// reached it is visible rather than only slow, and it can be cancelled the way
// a worker is when its step runs past the bound.
type syncBlackhole struct {
	calls atomic.Int64
	dead  chan struct{}
	once  sync.Once
}

// newSyncBlackhole is a blackhole whose calls block until it is cancelled.
func newSyncBlackhole() *syncBlackhole {
	return &syncBlackhole{dead: make(chan struct{})}
}

func (b *syncBlackhole) wait() error {
	b.calls.Add(1)
	<-b.dead
	return errors.New("the worker was cancelled")
}

func (b *syncBlackhole) Append([]synclog.Entry) ([]synclog.Entry, error) { return nil, b.wait() }
func (b *syncBlackhole) Pull(map[string]int) ([]synclog.Entry, error)    { return nil, b.wait() }
func (b *syncBlackhole) Head(string) ([]synclog.HeadRow, error)          { return nil, b.wait() }
func (b *syncBlackhole) Stats() (synclog.Stats, error)                   { return synclog.Stats{}, b.wait() }

// Cancel releases every call waiting on the blackhole, which is what killing
// the worker behind a call does.
func (b *syncBlackhole) Cancel() { b.once.Do(func() { close(b.dead) }) }

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

// TestSealTriggersASyncWithoutBlockingTheSeal is the load-bearing property of
// the whole seam: a seal commits its files and returns whether or not any
// remote is answering, and the pipeline it starts runs off the seal path. The
// tick is timed against a bound rather than merely checked for correctness, and
// the blackhole is reached after the seal committed rather than during it.
func TestSealTriggersASyncWithoutBlockingTheSeal(t *testing.T) {
	t.Parallel()

	b := newSyncBlackhole()
	rt := sealableReader(t, b)
	// The step bound is longer than the bound the tick is timed against, so a
	// tick that waited on the network would spend it here and be seen waiting.
	rt.Sync.Timeout = time.Minute

	d := NewDaemon(rt, time.Second)
	start := time.Now()
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick with a blackholed remote: %v", err)
	}
	took := time.Since(start)
	if took > 5*time.Second {
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

	// The trigger handed the round's bytes to the pipeline: the remote was
	// reached, and it was reached after the seal committed.
	deadline := time.Now().Add(5 * time.Second)
	for b.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := b.calls.Load(); got == 0 {
		t.Error("the seal trigger never reached the pipeline")
	}
	// Release the worker the blackhole is holding, so the attempt ends rather
	// than waiting its own bound out.
	b.Cancel()
	waitSyncIdle(t, d)
}

// farBody is one binding row as a body, so an entry seeded for another origin
// carries the columns the table's insert needs and imports like a real write.
func farBody(t *testing.T, id string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id": id, "name": id, "cwd": "/far", "builder_mode": "local",
		"created_at": "t", "ingest_source": "manual",
	})
	if err != nil {
		t.Fatalf("encode the far row: %v", err)
	}
	return body
}

// seedFar appends one other origin's entry to the log, so an import has
// something to apply.
func seedFar(t *testing.T, log *synclog.MemTransport, origin, id string, schema int) {
	t.Helper()
	entry, err := synclog.NewUpsert(origin, "binding", `["`+id+`"]`, schema, farBody(t, id), baseTime)
	if err != nil {
		t.Fatalf("build the far entry: %v", err)
	}
	if _, err := log.OnLog(origin).Append([]synclog.Entry{entry}); err != nil {
		t.Fatalf("append the far entry: %v", err)
	}
}

// tickTransport is the log a tick drives: it forwards to the in-memory log and
// records which calls the pipeline made through it.
type tickTransport struct {
	synclog.LogTransport
	mu    sync.Mutex
	calls []string
}

func (t *tickTransport) note(name string) {
	t.mu.Lock()
	t.calls = append(t.calls, name)
	t.mu.Unlock()
}

func (t *tickTransport) Append(entries []synclog.Entry) ([]synclog.Entry, error) {
	t.note("append")
	return t.LogTransport.Append(entries)
}

func (t *tickTransport) Pull(marks map[string]int) ([]synclog.Entry, error) {
	t.note("pull")
	return t.LogTransport.Pull(marks)
}

func (t *tickTransport) called(name string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range t.calls {
		if c == name {
			return true
		}
	}
	return false
}

// TestIdleWindowReachesThePipelineThroughTheRealTick pins the idle path from
// the tick's own side: the window is on the tick, and when it opens it drains
// the outbox into the log and imports what the other origins wrote. Removing
// the window from the tick, or wiring it to a no-op, leaves the remote
// untouched and this failing.
func TestIdleWindowReachesThePipelineThroughTheRealTick(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, _ := bindReader(t, repo)
	mdb, err := rt.Store.DB()
	if err != nil {
		t.Fatalf("store db: %v", err)
	}
	local, err := relevosync.LocalHandle(mdb)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	if err := relevosync.MarkEnabled(local, true, baseTime); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}

	// The other machine's entry waits in the same log the tick reads, so the
	// pipeline's import has something to apply.
	log := synclog.NewMemTransport("m2")
	_, known := mdb.SchemaVersions()
	seedFar(t, log, "m2", "far-1", known)

	spy := &tickTransport{LogTransport: log.OnLog(mdb.Origin())}
	rt.Sync = &relevosync.Runner{Client: spy, Local: local}

	d := NewDaemon(rt, time.Second)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitSyncIdle(t, d)

	if !spy.called("append") {
		t.Error("the idle window exported nothing")
	}
	if !spy.called("pull") {
		t.Error("the idle window imported nothing")
	}
	if got, ok, err := mdb.ImportMark("m2"); err != nil || !ok || got == 0 {
		t.Errorf("import mark for m2 = (%d, %v, %v), want the far entry applied", got, ok, err)
	}
}

// restartTransport models a worker a cancelled call killed: the first append
// waits on the worker until it is cancelled, and every call after that runs on
// a fresh worker, so a reused worker shows up as a repeated id.
type restartTransport struct {
	synclog.LogTransport
	dead    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	workers int
	kill    bool
	calls   []int
}

func newRestartTransport(inner synclog.LogTransport) *restartTransport {
	return &restartTransport{LogTransport: inner, dead: make(chan struct{})}
}

func (r *restartTransport) Append(entries []synclog.Entry) ([]synclog.Entry, error) {
	r.mu.Lock()
	if r.kill || r.workers == 0 {
		r.workers++
		r.kill = false
	}
	id := r.workers
	r.calls = append(r.calls, id)
	r.mu.Unlock()

	if id == 1 {
		<-r.dead
		return nil, errors.New("the worker was cancelled")
	}
	return r.LogTransport.Append(entries)
}

func (r *restartTransport) Cancel() {
	r.mu.Lock()
	r.kill = true
	first := r.workers <= 1
	r.mu.Unlock()
	if first {
		r.once.Do(func() { close(r.dead) })
	}
}

func (r *restartTransport) firstWorker() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return 0
	}
	return r.calls[0]
}

func (r *restartTransport) lastWorker() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return 0
	}
	return r.calls[len(r.calls)-1]
}

func (r *restartTransport) wasCancelled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.kill
}

// TestTickRestartsACancelledWorker pins the steady path's half of the worker's
// life: a step that ran past its bound cancels the worker behind it, and the
// next tick starts a fresh process rather than driving the cancelled one. The
// window is advanced between the two ticks so both reach the idle path.
func TestTickRestartsACancelledWorker(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, _ := bindReader(t, repo)
	mdb, err := rt.Store.DB()
	if err != nil {
		t.Fatalf("store db: %v", err)
	}
	local, err := relevosync.LocalHandle(mdb)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	if err := relevosync.MarkEnabled(local, true, baseTime); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}

	client := newRestartTransport(synclog.NewMemTransport("m2").OnLog(mdb.Origin()))
	rt.Sync = &relevosync.Runner{Client: client, Local: local}
	// The bound has to outlast the export's way to its first append, or the
	// cancel lands before any worker exists and the call it was meant for
	// starts after it: the first append waits for the cancel either way, so a
	// longer bound costs only this much test time.
	rt.Sync.Timeout = 500 * time.Millisecond

	now := baseTime
	d := NewDaemon(rt, time.Second)
	d.syncNow = func() time.Time { return now }

	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("first Tick: %v", err)
	}
	waitSyncIdle(t, d)
	first := client.firstWorker()
	if first == 0 {
		t.Fatal("the first tick drove no worker")
	}
	if !client.wasCancelled() {
		t.Fatal("the overrun worker was not cancelled")
	}

	// The second tick is about which worker the next call reaches, not about
	// the bound, so its bound is one no runner can overrun.
	rt.Sync.Timeout = time.Minute
	now = now.Add(syncWindow + time.Minute)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	waitSyncIdle(t, d)
	if second := client.lastWorker(); second == first {
		t.Errorf("the call after the cancel reused worker %d, want a fresh one", first)
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
