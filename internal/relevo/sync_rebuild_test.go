package relevo

// The daemon's corrupt-replica branch, driven through the real tick with no
// network, no harness and no Turso file: the replica is three seeded files in
// their own directory, the log is the in-memory one, and the transport's first
// append returns the corrupt read the contabo log showed.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// syncReplicaFiles is the replica and the driver files named after it, the
// set a machine that has opened a worker holds beside the shared file.
var syncReplicaFiles = []string{"relevo-sync.db", "relevo-sync.db-wal", "relevo-sync.db-shm"}

// seedSyncReplica writes the replica files into dir.
func seedSyncReplica(t *testing.T, dir string) {
	t.Helper()
	for _, n := range syncReplicaFiles {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("replica"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", n, err)
		}
	}
}

// corruptReplicaRuntime is a bound reader with sync on and a seeded replica,
// ready for a tick: the caller installs its transport through make, which is
// handed this machine's origin, and its clock on the daemon it builds.
func corruptReplicaRuntime(t *testing.T, make func(origin string) synclog.LogTransport) (rt Runtime, local *db.DB, state, replica string) {
	t.Helper()
	repo := readerRepo(t)
	rt, _ = bindReader(t, repo)
	mdb, err := rt.Store.DB()
	if err != nil {
		t.Fatalf("store db: %v", err)
	}
	local, err = relevosync.LocalHandle(mdb)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	if err := relevosync.MarkEnabled(local, true, baseTime); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	state = t.TempDir()
	replica = filepath.Join(state, "relevo-sync.db")
	seedSyncReplica(t, state)
	rt.Sync = &relevosync.Runner{Client: make(mdb.Origin()), Local: local, ReplicaPath: replica}
	return rt, local, state, replica
}

// corruptReplicaTransport is the log a tick drives while the replica is
// corrupt: the first append returns the corrupt read the contabo log showed,
// carried under the worker's refusal marker, and every later call runs on the
// in-memory log underneath. The count is what says the retry reached the
// transport.
type corruptReplicaTransport struct {
	synclog.LogTransport
	mu      sync.Mutex
	appends int
}

func (t *corruptReplicaTransport) Append(entries []synclog.Entry) ([]synclog.Entry, error) {
	t.mu.Lock()
	t.appends++
	n := t.appends
	t.mu.Unlock()
	if n == 1 {
		return nil, errors.New("the sync worker refused: turso: database is corrupt: " +
			"range 25445.. out of bounds for buffer size 4096")
	}
	return t.LogTransport.Append(entries)
}

func (t *corruptReplicaTransport) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.appends
}

// remoteRefusedTransport is the log an ordinary remote refusal drives: its
// first append carries the worker's marker and the remote's sentinel, the pair
// every classified refusal arrives under, and no needle a corrupt read would
// add.
type remoteRefusedTransport struct {
	synclog.LogTransport
}

func (t *remoteRefusedTransport) Append([]synclog.Entry) ([]synclog.Entry, error) {
	return nil, fmt.Errorf("synclog: export: append: %s: %w",
		relevosync.WorkerRefusedText, relevosync.ErrRemoteRefused)
}

// TestCorruptReplicaIsMovedAsideAndRetried pins the whole branch: an attempt
// whose error is the replica's own unreadability moves the replica and its
// driver files into the aside folder, records the rebuild in the machine-local
// kv, and queues the fresh bootstrap in the same slot -- the second attempt
// reaches the transport, which is what a fresh replica is bootstrapped from.
//
// Mutation: deleting the IsReplicaCorrupt branch in runSync leaves the files
// where they were and this fails.
func TestCorruptReplicaIsMovedAsideAndRetried(t *testing.T) {
	t.Parallel()

	log := synclog.NewMemTransport("m2")
	spy := &corruptReplicaTransport{}
	rt, local, state, _ := corruptReplicaRuntime(t, func(origin string) synclog.LogTransport {
		spy.LogTransport = log.OnLog(origin)
		return spy
	})
	d := NewDaemon(rt, time.Second)

	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitSyncIdle(t, d)

	aside := filepath.Join(state, "relevo-sync-aside")
	for _, n := range syncReplicaFiles {
		if _, err := os.Stat(filepath.Join(state, n)); !os.IsNotExist(err) {
			t.Errorf("%s is still in the state directory: %v", n, err)
		}
		if _, err := os.Stat(filepath.Join(aside, n)); err != nil {
			t.Errorf("%s is not in the aside folder: %v", n, err)
		}
	}
	got, err := relevosync.ReadRebuild(local)
	if err != nil {
		t.Fatalf("ReadRebuild: %v", err)
	}
	if got.At.IsZero() || got.Aside != aside {
		t.Errorf("the rebuild record = %+v, want the aside folder %s named", got, aside)
	}
	if spy.count() < 2 {
		t.Errorf("the retry never reached the transport: %d appends", spy.count())
	}
}

// TestSecondCorruptionInTheWindowLatchesInsteadOfRebuilding pins the cap at
// the tick: with a fresh rebuild record already in the kv, a corrupt read
// moves nothing -- the machine latches instead, and the cause names the manual
// rebuild the retry verb carries.
//
// Mutation: deleting the cap makes this move the files and fail.
func TestSecondCorruptionInTheWindowLatchesInsteadOfRebuilding(t *testing.T) {
	t.Parallel()

	log := synclog.NewMemTransport("m2")
	spy := &corruptReplicaTransport{}
	rt, local, state, _ := corruptReplicaRuntime(t, func(origin string) synclog.LogTransport {
		spy.LogTransport = log.OnLog(origin)
		return spy
	})
	now := baseTime
	d := NewDaemon(rt, time.Second)
	d.syncNow = func() time.Time { return now }
	if err := relevosync.WriteRebuild(local, now, ""); err != nil {
		t.Fatalf("WriteRebuild: %v", err)
	}

	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitSyncIdle(t, d)

	for _, n := range syncReplicaFiles {
		if _, err := os.Stat(filepath.Join(state, n)); err != nil {
			t.Errorf("%s did not stay in the state directory: %v", n, err)
		}
	}
	if _, err := os.Stat(filepath.Join(state, "relevo-sync-aside")); !os.IsNotExist(err) {
		t.Errorf("an aside folder was created: %v", err)
	}
	b := relevosync.NewBreaker(local)
	if latched, err := b.Latched(); err != nil || !latched {
		t.Errorf("Latched = %t, %v; want the second corruption to have latched the machine", latched, err)
	}
	cause, err := b.LatchCause()
	if err != nil {
		t.Fatalf("LatchCause: %v", err)
	}
	if !strings.Contains(cause, "retry --rebuild") {
		t.Errorf("the latch cause %q does not name the manual rebuild", cause)
	}
}

// TestOrdinarySyncFailureMovesNothingAside pins the classification from the
// tick's other side: a remote refusal -- even one that arrived under the
// worker's marker, as they all do -- is not the replica's own unreadability,
// so the replica files are untouched and no record was written.
//
// Mutation: widening IsReplicaCorrupt past the needle table makes this move
// the files and fail.
func TestOrdinarySyncFailureMovesNothingAside(t *testing.T) {
	t.Parallel()

	log := synclog.NewMemTransport("m2")
	rt, local, state, _ := corruptReplicaRuntime(t, func(origin string) synclog.LogTransport {
		return &remoteRefusedTransport{LogTransport: log.OnLog(origin)}
	})
	d := NewDaemon(rt, time.Second)

	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitSyncIdle(t, d)

	for _, n := range syncReplicaFiles {
		if _, err := os.Stat(filepath.Join(state, n)); err != nil {
			t.Errorf("%s did not survive the ordinary failure: %v", n, err)
		}
	}
	if _, err := os.Stat(filepath.Join(state, "relevo-sync-aside")); !os.IsNotExist(err) {
		t.Errorf("an aside folder was created: %v", err)
	}
	got, err := relevosync.ReadRebuild(local)
	if err != nil {
		t.Fatalf("ReadRebuild: %v", err)
	}
	if !got.At.IsZero() || got.Aside != "" {
		t.Errorf("an ordinary failure wrote a rebuild record: %+v", got)
	}
}
