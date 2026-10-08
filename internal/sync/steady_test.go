package sync

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The steady pipeline's own cases: what one attempt moves, which markers it
// leaves, and what it does to a worker whose step ran past its bound. They run
// against the in-memory log, so no case reaches a network or spawns a worker.

// farBody is one binding row as a body, so an entry seeded for another origin
// carries the columns the table's insert needs and imports like a real write.
func farBody(t *testing.T, id string) json.RawMessage {
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

// farEntry is one other origin's entry, numbered by the log it is appended to.
func seedFar(t *testing.T, log *synclog.MemTransport, origin, id string, schema int) {
	t.Helper()
	entry, err := synclog.NewUpsert(origin, "binding", `["`+id+`"]`, schema, farBody(t, id), time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("build the far entry: %v", err)
	}
	if _, err := log.OnLog(origin).Append([]synclog.Entry{entry}); err != nil {
		t.Fatalf("append the far entry: %v", err)
	}
}

// TestSteadyExportsAndImports pins the pipeline's two halves in order: this
// machine's outbox entry reaches the log, and the other origin's entry lands in
// this machine's file.
func TestSteadyExportsAndImports(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	seedJoin(t, shared, joinBinding("b1", "m1"))

	log := synclog.NewMemTransport("m1")
	_, known := shared.SchemaVersions()
	seedFar(t, log, "m2", "b2", known)

	runner := &Runner{Client: log, Local: local, Timeout: time.Second}
	res := runner.SyncOnce(context.Background(), shared)
	if res.Err != nil {
		t.Fatalf("SyncOnce: %v", res.Err)
	}
	if res.Exported != 1 {
		t.Errorf("Exported = %d, want the one owned entry", res.Exported)
	}
	if res.Applied != 1 {
		t.Errorf("Applied = %d, want the other origin's entry", res.Applied)
	}
	if got, ok, err := shared.ImportMark("m2"); err != nil || !ok || got == 0 {
		t.Errorf("import mark for m2 = (%d, %v, %v), want the entry applied", got, ok, err)
	}

	// A successful attempt leaves the tick marker OK and the backlog at zero,
	// because the export ran the outbox to empty.
	var tickMark struct {
		OK bool `json:"ok"`
	}
	body, ok, err := local.KVGet(KeyLastTick)
	if err != nil || !ok {
		t.Fatalf("read the tick marker: (ok %v, err %v)", ok, err)
	}
	if err := json.Unmarshal(body, &tickMark); err != nil {
		t.Fatalf("decode the tick marker: %v", err)
	}
	if !tickMark.OK {
		t.Error("the tick marker says the attempt failed")
	}
	if got, ok, err := local.KVGet(KeyBacklog); err != nil || !ok || string(got) != "0" {
		t.Errorf("backlog = (%q, %v, %v), want zero after a drained export", got, ok, err)
	}
}

// TestSteadyRecordsANoClientAttempt pins the degenerate case: a runner with
// nothing to drive reports it and writes no marker, because there is no local
// file to write one through.
func TestSteadyRecordsANoClientAttempt(t *testing.T) {
	t.Parallel()
	shared, _ := joinPair(t, "m1")
	if res := (&Runner{}).SyncOnce(context.Background(), shared); !errors.Is(res.Err, errNoSync) {
		t.Errorf("SyncOnce with no client = %v, want errNoSync", res.Err)
	}
	if res := (&Runner{}).SyncOnce(context.Background(), nil); !errors.Is(res.Err, errNoSync) {
		t.Errorf("SyncOnce with no shared file = %v, want errNoSync", res.Err)
	}
}

// TestRunnerOnReadsTheEnabledMark pins the idle trigger's gate: a machine
// marked on is driven, one marked off is not, and a marker that will not parse
// reads as on rather than quietly stopping a machine meant to be syncing.
func TestRunnerOnReadsTheEnabledMark(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	runner := &Runner{Client: synclog.NewMemTransport("m1"), Local: local}

	if runner.On() {
		t.Error("a machine never marked on reads as on")
	}
	// A marker the file cannot decode reads as on: the machine has a client and
	// a local file, and stopping on a read error would quietly end syncing.
	if err := MarkEnabled(local, true, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	raw, err := db.OpenRaw(shared.Path())
	if err != nil {
		t.Fatalf("db.OpenRaw: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`UPDATE kv SET value_json = 'not-json' WHERE key = ?`, KeyEnabled); err != nil {
		t.Fatalf("corrupt the enabled marker: %v", err)
	}
	if !runner.On() {
		t.Error("an unreadable enabled marker did not read as on")
	}
	if (&Runner{Client: synclog.NewMemTransport("m1")}).On() {
		t.Error("a runner with no local file reads as on")
	}
}

// cancelLog is a transport whose worker has to be cancelled before its call
// comes back, which is what a step that ran past its bound is meant to reach.
// It records that the cancel happened.
type cancelLog struct {
	dead chan struct{}
	once sync.Once
	mu   sync.Mutex
	gone bool
}

func newCancelLog() *cancelLog { return &cancelLog{dead: make(chan struct{})} }

func (c *cancelLog) wait() error {
	<-c.dead
	return errors.New("the worker was cancelled")
}

func (c *cancelLog) Append([]synclog.Entry) ([]synclog.Entry, error) { return nil, c.wait() }
func (c *cancelLog) Pull(map[string]int) ([]synclog.Entry, error)    { return nil, c.wait() }
func (c *cancelLog) Head(string) ([]synclog.HeadRow, error)          { return nil, c.wait() }
func (c *cancelLog) Stats() (synclog.Stats, error)                   { return synclog.Stats{}, c.wait() }

func (c *cancelLog) Cancel() {
	c.mu.Lock()
	c.gone = true
	c.mu.Unlock()
	c.once.Do(func() { close(c.dead) })
}

func (c *cancelLog) cancelled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gone
}

// TestSteadyCancelsAStepThatRanPastItsBound pins the bound and what it costs:
// a worker whose call does not come back is cancelled, the step is reported as
// timed out, and the attempt records a failed tick rather than looking idle.
func TestSteadyCancelsAStepThatRanPastItsBound(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	seedJoin(t, shared, joinBinding("b1", "m1"))

	client := newCancelLog()
	runner := &Runner{Client: client, Local: local, Timeout: 30 * time.Millisecond}
	res := runner.SyncOnce(context.Background(), shared)
	if !errors.Is(res.Err, errStepTimedOut) {
		t.Fatalf("SyncOnce = %v, want a timed-out step", res.Err)
	}
	if !client.cancelled() {
		t.Error("the overrun worker was not cancelled")
	}

	var tickMark struct {
		OK bool `json:"ok"`
	}
	body, ok, err := local.KVGet(KeyLastTick)
	if err != nil || !ok {
		t.Fatalf("read the tick marker: (ok %v, err %v)", ok, err)
	}
	if err := json.Unmarshal(body, &tickMark); err != nil {
		t.Fatalf("decode the tick marker: %v", err)
	}
	if tickMark.OK {
		t.Error("the tick marker says a timed-out attempt succeeded")
	}
}

// TestSteadyStopsWhenTheCallerCancels pins the other release: a context the
// caller cancels ends the step it is waiting on and is reported as the
// cancellation it is rather than as the bound elapsing.
func TestSteadyStopsWhenTheCallerCancels(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	seedJoin(t, shared, joinBinding("b1", "m1"))

	ctx, cancel := context.WithCancel(context.Background())
	client := newCancelLog()
	runner := &Runner{Client: client, Local: local, Timeout: time.Minute}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	res := runner.SyncOnce(ctx, shared)
	if !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("SyncOnce = %v, want the caller's cancellation", res.Err)
	}
	if !client.cancelled() {
		t.Error("a cancelled step left its worker running")
	}
}
