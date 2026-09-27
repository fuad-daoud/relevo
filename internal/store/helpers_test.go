package store

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
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

// writeTarGz writes a flat <name>/<member> tarball at dest, the layout a
// pre-database relevo archived bindings in.
func writeTarGz(t *testing.T, dest, name string, members map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for base, body := range members {
		hdr := &tar.Header{
			Name:     name + "/" + base,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []func() error{tw.Close, gz.Close, f.Close} {
		if err := c(); err != nil {
			t.Fatal(err)
		}
	}
}

// legacyFixture returns the ingest package's legacy bind.json/log.jsonl pair.
func legacyFixture(t *testing.T, base string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "ingest", "testdata", "binding-three-rounds", base))
	if err != nil {
		t.Fatalf("read fixture %s: %v", base, err)
	}
	return raw
}

// seedLegacyDir writes a legacy binding directory named name from the fixture,
// optionally patched by patchLog, and returns the log bytes it wrote.
func seedLegacyDir(t *testing.T, root, name string, patchLog func([]byte) []byte) []byte {
	t.Helper()

	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	var doc map[string]any
	if err := json.Unmarshal(legacyFixture(t, "bind.json"), &doc); err != nil {
		t.Fatalf("decode fixture bind.json: %v", err)
	}
	doc["name"] = name
	bindJSON, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bind.json"), bindJSON, 0o644); err != nil {
		t.Fatal(err)
	}

	logJSON := legacyFixture(t, "log.jsonl")
	if patchLog != nil {
		logJSON = patchLog(logJSON)
	}
	if err := os.WriteFile(filepath.Join(dir, "log.jsonl"), logJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	return logJSON
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
