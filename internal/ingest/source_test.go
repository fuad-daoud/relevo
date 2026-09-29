package ingest

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

func TestDirSourceListsAndOpens(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bind.json"), `{"name":"fixture"}`)
	writeFile(t, filepath.Join(dir, "log.jsonl"), `{"round":1}`+"\n")
	writeFile(t, filepath.Join(dir, "001-plan.md"), "plan text")

	src := DirSource(dir)

	if got := src.Name(); got != filepath.Base(dir) {
		t.Errorf("Name() = %q, want %q", got, filepath.Base(dir))
	}

	b, err := src.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Name != "fixture" {
		t.Errorf("Bind().Name = %q, want fixture", b.Name)
	}

	names, err := src.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"001-plan.md", "bind.json", "log.jsonl"}
	if !equalStrings(names, want) {
		t.Errorf("List() = %v, want %v", names, want)
	}

	rc, size, err := src.Open("001-plan.md")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "plan text" {
		t.Errorf("Open(001-plan.md) = %q, want %q", data, "plan text")
	}
	if size != int64(len("plan text")) {
		t.Errorf("size = %d, want %d", size, len("plan text"))
	}

	kind, path := src.Origin()
	if kind != "live" || path != dir {
		t.Errorf("Origin() = %q, %q, want live, %q", kind, path, dir)
	}

	if _, _, err := src.Open("does-not-exist"); !os.IsNotExist(err) {
		t.Errorf("Open(missing) err = %v, want os.ErrNotExist", err)
	}
}

func TestDirSourceMissingBindIsErrSource(t *testing.T) {
	src := DirSource(t.TempDir())

	if _, err := src.Bind(); err == nil {
		t.Fatal("Bind() over a directory with no bind.json must fail")
	} else if !errors.Is(err, ErrSource) {
		t.Errorf("Bind() err = %v, want ErrSource", err)
	}
}

// buildArchivedSource saves a binding, adds a round file, then archives it and
// returns the store and the record's id.
func buildArchivedSource(t *testing.T, root, name string) (*store.Store, string) {
	t.Helper()
	s := store.New(root)
	if err := s.Save(store.Binding{Name: name, CWD: "/work/" + name, State: store.StateDone}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	writeFile(t, filepath.Join(s.Dir(name), "001-plan.md"), "plan text")
	writeFile(t, filepath.Join(s.Dir(name), "log.jsonl"), `{"round":1}`+"\n")

	if _, err := s.Archive(name); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	archived, err := s.ListArchived()
	if err != nil || len(archived) != 1 {
		t.Fatalf("ListArchived = %+v, %v, want exactly one record", archived, err)
	}
	return s, archived[0].RecordID
}

func TestArchivedSourceReadsMembersWithoutExtracting(t *testing.T) {
	root := t.TempDir()
	s, recordID := buildArchivedSource(t, root, "fixture")

	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	src := ArchivedSource(s, recordID)

	if got := src.Name(); got != "fixture" {
		t.Errorf("Name() = %q, want fixture", got)
	}

	names, err := src.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"001-plan.md", "bind.json", "log.jsonl"}
	if !equalStrings(names, want) {
		t.Errorf("List() = %v, want %v", names, want)
	}

	b, err := src.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Name != "fixture" {
		t.Errorf("Bind().Name = %q, want fixture", b.Name)
	}

	rc, size, err := src.Open("001-plan.md")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "plan text" {
		t.Errorf("Open(001-plan.md) = %q, want %q", data, "plan text")
	}
	if size != int64(len("plan text")) {
		t.Errorf("size = %d, want %d", size, len("plan text"))
	}

	if _, _, err := src.Open("does-not-exist"); !os.IsNotExist(err) {
		t.Errorf("Open(missing) err = %v, want os.ErrNotExist", err)
	}

	// Reading an archived record extracts nothing: no round file reappears.
	after, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("root directory gained entries reading the archived record: before %v, after %v", before, after)
	}
}

func TestArchivedSourceOriginAndStamp(t *testing.T) {
	root := t.TempDir()
	s, recordID := buildArchivedSource(t, root, "fixture")

	src := ArchivedSource(s, recordID)

	kind, gotPath := src.Origin()
	if kind != "archive" || gotPath != "" {
		t.Errorf("Origin() = %q, %q, want archive, \"\"", kind, gotPath)
	}

	ts, ok := src.(archivedAtter)
	if !ok {
		t.Fatalf("ArchivedSource() did not return an archivedAtter, got %T", src)
	}
	at, found := ts.ArchivedAt()
	if !found {
		t.Fatal("ArchivedAt() found = false, want true")
	}
	if at.IsZero() {
		t.Error("ArchivedAt() returned the zero time")
	}
}
