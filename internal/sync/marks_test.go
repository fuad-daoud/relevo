package sync

// The steady loop's publish of this machine's import marks: when it runs, what
// it sends, and what it costs. It runs against the in-memory log, so a case
// reaches no network and starts no worker.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// markSpy is the in-memory log with a count of the publishes that reached it,
// so a case reads how often the loop published rather than inferring it from
// what the log holds afterwards -- which is the same after a second publish of
// the same marks.
type markSpy struct {
	*synclog.MemTransport
	publishes int
	at        []time.Time
	marks     []map[string]int
}

func newMarkSpy(origin string) *markSpy {
	return &markSpy{MemTransport: synclog.NewMemTransport(origin)}
}

func (m *markSpy) PublishMarks(marks map[string]int, at time.Time) error {
	m.publishes++
	m.at = append(m.at, at)
	m.marks = append(m.marks, marks)
	return m.MemTransport.PublishMarks(marks, at)
}

// seed is seedFar against the spy, which stands where the log does for the loop
// while still recording what reached it.
func seed(t *testing.T, log *markSpy, origin, id string, schema int) {
	t.Helper()
	seedFar(t, log.MemTransport, origin, id, schema)
}

// mustSchemaVersion is this file's schema version, so a seeded entry imports as
// a row this machine can act on rather than being held as a newer writer's.
func mustSchemaVersion(t *testing.T, shared *db.DB) int {
	t.Helper()
	_, versions := shared.SchemaVersions()
	return versions
}

// markPublished is the marker as r.put writes it: the publish time as the
// marker's whole value, which is all the throttle reads.
type markPublished = time.Time

// TestSteadyPublishesMarksAfterAnImport pins the loop's job: an attempt that
// applied another origin's entries publishes how far it got, so the log knows a
// reader exists and where it stands.
//
// The marks that go out are this machine's own import marks for every origin it
// has applied, read from the file rather than from what the loop happened to
// move, so a publish reports the whole position and not the last page's.
func TestSteadyPublishesMarksAfterAnImport(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	log := newMarkSpy("m1")
	seed(t, log, "m2", "b2", mustSchemaVersion(t, shared))

	runner := &Runner{Client: log, Local: local}
	res := runner.SyncOnce(context.Background(), shared)
	if res.Err != nil {
		t.Fatalf("SyncOnce: %v", res.Err)
	}
	if log.publishes != 1 {
		t.Fatalf("the loop published %d times, want once after an import that moved a mark", log.publishes)
	}
	if got := log.marks[0]["m2"]; got == 0 {
		t.Errorf("the published marks = %v, want a sequence for m2", log.marks[0])
	}
	if _, ok := log.marks[0]["m1"]; ok {
		t.Errorf("the published marks = %v, want no mark of this machine's own origin", log.marks[0])
	}

	// The marker is what the throttle reads, so a case can see the publish
	// happened from the file rather than only from the transport.
	var at markPublished
	if err := marker(local, KeyMarksPublished, &at); err != nil {
		t.Fatalf("read the marks marker: %v", err)
	}
	if at.IsZero() {
		t.Error("the marks marker was left unset after a publish")
	}
}

// TestSteadyPublishesMarksAtMostEveryFifteenMinutes pins the throttle. Every
// publish is a Turso write and a push, and a tick that has just run a transfer
// should not spend another on a report of where it stands.
//
// The throttle is safe because a mark only grows: a published mark that fell
// behind is a lower one, which only makes another machine's floor more
// cautious, never less.
func TestSteadyPublishesMarksAtMostEveryFifteenMinutes(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	log := newMarkSpy("m1")
	seed(t, log, "m2", "b2", mustSchemaVersion(t, shared))

	runner := &Runner{Client: log, Local: local}
	for i := 0; i < 3; i++ {
		seed(t, log, "m2", "b"+string(rune('a'+i)), mustSchemaVersion(t, shared))
		if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
			t.Fatalf("attempt %d: %v", i, res.Err)
		}
	}
	if log.publishes != 1 {
		t.Errorf("three attempts that each moved a mark published %d times, want one: "+
			"every publish is a write and a push", log.publishes)
	}

	// Once the window has passed the next publish is due again, so the throttle
	// bounds how often rather than switching the report off.
	if err := local.KVPut(KeyMarksPublished, mustMarshal(t,
		time.Now().UTC().Add(-2*marksEvery))); err != nil {
		t.Fatalf("age the marks marker: %v", err)
	}
	seed(t, log, "m2", "b-late", mustSchemaVersion(t, shared))
	if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
		t.Fatalf("the attempt after the window: %v", res.Err)
	}
	if log.publishes != 2 {
		t.Errorf("after the window the loop published %d times, want a second publish", log.publishes)
	}
}

// TestSteadyPublishesNothingWhenNoMarkMoved pins that a publish follows an
// import that advanced a mark and not an attempt that merely ran. A tick with
// nothing to apply has nothing to report, and publishing the same marks on
// every idle window would spend a push to say nothing.
func TestSteadyPublishesNothingWhenNoMarkMoved(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	log := newMarkSpy("m1")
	runner := &Runner{Client: log, Local: local}

	for i := 0; i < 3; i++ {
		if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
			t.Fatalf("attempt %d: %v", i, res.Err)
		}
	}
	if log.publishes != 0 {
		t.Errorf("three attempts that moved no mark published %d times, want none", log.publishes)
	}
	if _, ok := markerIsSet(local, KeyMarksPublished); ok {
		t.Error("the marks marker was written by an attempt that published nothing")
	}
}

// TestMarksMarkerThatWillNotParsePublishesAnyway pins which way a damaged marker
// is read. A machine whose marker cannot be read must not be held off by it
// forever: the worst a bad row costs is a publish the throttle was going to
// skip, and the alternative is a machine whose marks are never published again.
func TestMarksMarkerThatWillNotParsePublishesAnyway(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	log := newMarkSpy("m1")
	seed(t, log, "m2", "b2", mustSchemaVersion(t, shared))
	if err := local.KVPut(KeyMarksPublished, []byte(`{"at":"not-a-time"}`)); err != nil {
		t.Fatalf("corrupt the marks marker: %v", err)
	}

	runner := &Runner{Client: log, Local: local}
	if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
		t.Fatalf("SyncOnce: %v", res.Err)
	}
	if log.publishes != 1 {
		t.Errorf("a damaged marker held %d publishes back, want the publish to go out", log.publishes)
	}
}

// mustMarshal encodes a marker body, which a case writes by hand.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode the marker: %v", err)
	}
	return body
}

// markerIsSet reports whether a marker exists, so a case asserts a key was left
// alone rather than only that it was read.
func markerIsSet(kv db.KV, key string) (any, bool) {
	_, ok, err := kv.KVGet(key)
	return nil, ok && err == nil
}
