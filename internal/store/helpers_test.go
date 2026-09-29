package store

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

func newBinding(name, cwd string) Binding {
	return Binding{
		Name:             name,
		CWD:              cwd,
		MasterMind:       Endpoint{PaneID: "w2:p3", SessionID: "abc", Kind: "claude"},
		Builder:          Endpoint{AgentName: name + "-builder", PaneID: "w2:p4", Kind: "opencode"},
		BuilderCandidate: "builder",
		Round:            1,
		State:            StateActive,
		RoundCap:         20,
		RoundTimeoutMS:   1800000,
	}
}

// bindingRecordJSON returns the record_json a saved binding is stored as.
func bindingRecordJSON(t *testing.T, s *Store, name string) []byte {
	t.Helper()
	d, err := s.dbForWrite()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	rec, ok, err := d.RecordGet(s.owner, name)
	if err != nil || !ok {
		t.Fatalf("RecordGet(%q): ok=%v err=%v", name, ok, err)
	}
	return []byte(rec.JSON)
}

// holdStateLock takes the store's state lock and keeps it held until the
// returned release runs, so a caller can prove a read does not wait on it.
func holdStateLock(t *testing.T, s *Store) (release func()) {
	t.Helper()
	holding := make(chan struct{})
	unblock := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- s.WithLock(func(tx *Tx) error {
			close(holding)
			<-unblock
			return nil
		})
	}()
	<-holding
	return func() {
		close(unblock)
		if err := <-holderDone; err != nil {
			t.Errorf("WithLock holder: %v", err)
		}
	}
}

// bindingEvents returns the stored binding_event rows for name, so a test can
// assert on entry_json.
func bindingEvents(t *testing.T, s *Store, name string) []db.RecordEvent {
	t.Helper()
	d, err := s.dbForWrite()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	rec, ok, err := d.RecordGet(s.owner, name)
	if err != nil || !ok {
		t.Fatalf("RecordGet(%q): ok=%v err=%v", name, ok, err)
	}
	evs, err := d.EventsOf(rec.ID, 0)
	if err != nil {
		t.Fatalf("EventsOf: %v", err)
	}
	return evs
}

func seedBinding(t *testing.T) (*Store, string) {
	t.Helper()
	s := New(t.TempDir())
	if err := s.Save(newBinding("webshop", "/repo")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return s, "webshop"
}

func withBinding(b Binding, mutate func(*Binding)) Binding {
	mutate(&b)
	return b
}

// putRecordJSON puts name's live record with raw record JSON, so a test can
// seed a record shape the store's own writers would not produce.
func putRecordJSON(t *testing.T, s *Store, name, recordJSON string) {
	t.Helper()
	d, err := s.dbForWrite()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	if _, err := d.RecordPut(db.Record{
		Owner: s.owner,
		Name:  name,
		Round: 1,
		JSON:  recordJSON,
	}); err != nil {
		t.Fatalf("RecordPut(%q): %v", name, err)
	}
}

// putEventJSON replaces name's events with raw entry JSON, so a test can seed a
// log the store's own writers would not produce.
func putEventJSON(t *testing.T, s *Store, name string, lines []string) {
	t.Helper()
	d, err := s.dbForWrite()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	rec, ok, err := d.RecordGet(s.owner, name)
	if err != nil || !ok {
		t.Fatalf("RecordGet(%q): ok=%v err=%v", name, ok, err)
	}
	evs := make([]db.RecordEvent, 0, len(lines))
	for i, line := range lines {
		evs = append(evs, db.RecordEvent{Seq: i + 1, JSON: line})
	}
	if err := d.EventReplaceAll(rec.ID, evs); err != nil {
		t.Fatalf("EventReplaceAll(%q): %v", name, err)
	}
}

func containsAll(haystack []string, names map[string][]byte) bool {
	have := map[string]bool{}
	for _, h := range haystack {
		have[h] = true
	}
	for name := range names {
		if !have[name] {
			return false
		}
	}
	return true
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
