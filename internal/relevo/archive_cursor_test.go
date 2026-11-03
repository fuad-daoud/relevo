package relevo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/store"
)

// countingArchiveBackend wraps the live backend, counting every call and
// optionally failing ingest a set number of times per record, so the cost and
// retry tests drive the production Tick path with an observable seam.
type countingArchiveBackend struct {
	archiveBackend
	listAllCalls   int
	listSinceCalls int
	markedCalls    int
	ingestCalls    int
	confirmCalls   int
	failIngest     map[string]int
}

func (b *countingArchiveBackend) listAll() ([]store.ArchivedBinding, error) {
	b.listAllCalls++
	return b.archiveBackend.listAll()
}

func (b *countingArchiveBackend) listSince(since time.Time) ([]store.ArchivedRef, error) {
	b.listSinceCalls++
	return b.archiveBackend.listSince(since)
}

func (b *countingArchiveBackend) marked(recordID string) (bool, error) {
	b.markedCalls++
	return b.archiveBackend.marked(recordID)
}

func (b *countingArchiveBackend) ingest(ctx context.Context, recordID, name string) (ingest.Stats, error) {
	b.ingestCalls++
	if b.failIngest[recordID] > 0 {
		b.failIngest[recordID]--
		return ingest.Stats{}, errors.New("test ingest failure")
	}
	return b.archiveBackend.ingest(ctx, recordID, name)
}

func (b *countingArchiveBackend) confirm(recordID string) error {
	b.confirmCalls++
	return b.archiveBackend.confirm(recordID)
}

// TestMirrorIdleTickSkipsSeenRecords pins the per-tick cost: after a first
// pass over three archived records, a later tick with no new archive performs
// no ingest and no per-record mark read. Without the cursor the second pass
// would list every archived record and read every mark.
func TestMirrorIdleTickSkipsSeenRecords(t *testing.T) {
	rt, d, daemon := archiveTickRuntime(t)
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if err := rt.Store.Save(store.Binding{Name: name, CWD: "/repo", State: store.StateActive, Round: 1}); err != nil {
			t.Fatalf("Save %s: %v", name, err)
		}
		if err := rt.Store.AppendLog(name, store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt}); err != nil {
			t.Fatalf("AppendLog %s: %v", name, err)
		}
		if _, err := rt.Store.Archive(name); err != nil {
			t.Fatalf("Archive %s: %v", name, err)
		}
	}

	backend := &countingArchiveBackend{archiveBackend: liveBackend(rt)}
	daemon.archiveBackend = backend

	if err := daemon.Tick(context.Background()); err != nil {
		t.Fatalf("first Tick: %v", err)
	}
	if backend.ingestCalls != 3 {
		t.Fatalf("first tick ingests = %d, want one per archived record", backend.ingestCalls)
	}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if _, found, err := d.Binding(name); err != nil || !found {
			t.Fatalf("mirror Binding(%s): found=%v err=%v", name, found, err)
		}
	}

	*backend = countingArchiveBackend{archiveBackend: backend.archiveBackend}
	if err := daemon.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	if backend.listAllCalls != 0 {
		t.Errorf("idle tick listed all archived records %d times, want none", backend.listAllCalls)
	}
	if backend.markedCalls != 0 || backend.confirmCalls != 0 {
		t.Errorf("idle tick mark reads = %d, writes = %d; want none", backend.markedCalls, backend.confirmCalls)
	}
	if backend.ingestCalls != 0 {
		t.Errorf("idle tick ingests = %d, want none", backend.ingestCalls)
	}
	if backend.listSinceCalls != 1 {
		t.Errorf("idle tick narrow scans = %d, want one", backend.listSinceCalls)
	}
}

// TestMirrorRetriesFailedIngest pins the failure path: a record whose ingest
// fails once is mirrored on a later tick, not dropped. Without the retry set
// the second tick would never re-attempt it.
func TestMirrorRetriesFailedIngest(t *testing.T) {
	rt, d, daemon := archiveTickRuntime(t)
	seedLiveWebshop(t, rt)
	if _, err := rt.Store.Archive("webshop"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	archived, err := rt.Store.ListArchived()
	if err != nil || len(archived) != 1 {
		t.Fatalf("ListArchived = %+v, %v; want one record", archived, err)
	}

	backend := &countingArchiveBackend{
		archiveBackend: liveBackend(rt),
		failIngest:     map[string]int{archived[0].RecordID: 1},
	}
	daemon.archiveBackend = backend

	if err := daemon.Tick(context.Background()); err != nil {
		t.Fatalf("first Tick: %v", err)
	}
	if _, found, _ := d.Binding("webshop"); found {
		t.Fatalf("mirror holds webshop after a failed ingest, want nothing yet")
	}
	if _, ok, _ := d.LocalOrSelf().KVGet("ingested.archive." + archived[0].RecordID); ok {
		t.Fatalf("the mark is set after a failed ingest, want it unset")
	}

	if err := daemon.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	row, found, err := d.Binding("webshop")
	if err != nil || !found {
		t.Fatalf("mirror Binding after the retry: found=%v err=%v", found, err)
	}
	if row.ArchivedAt == nil {
		t.Error("the retried row has no ArchivedAt, want the archive stamp")
	}
	if backend.ingestCalls != 2 {
		t.Errorf("ingests = %d, want one attempt per tick", backend.ingestCalls)
	}
}
