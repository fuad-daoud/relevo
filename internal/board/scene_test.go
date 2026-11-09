package board

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingIsNew(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.excalidraw")
	data, etag, isNew, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !isNew || etag != "" || data != nil {
		t.Errorf("Load(missing) = (%q, %q, %v), want (nil, \"\", true)", data, etag, isNew)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("Load created the file: stat error = %v", statErr)
	}
}

// TestWriteAtomicRenameFailureLeavesOldFile: a crash at the rename leaves
// the old board in place and no temp file behind.
func TestWriteAtomicRenameFailureLeavesOldFile(t *testing.T) {
	dir := t.TempDir()
	board := filepath.Join(dir, "board.html")
	before := []byte("<html><body>board</body></html>")
	if err := os.WriteFile(board, before, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	orig := renameFile
	renameFile = func(string, string) error { return errors.New("crash before rename") }
	t.Cleanup(func() { renameFile = orig })

	if err := writeAtomic(board, []byte("<html><body>new</body></html>")); err == nil {
		t.Fatal("writeAtomic with a failing rename succeeded")
	}
	got, _ := os.ReadFile(board)
	if !bytes.Equal(got, before) {
		t.Errorf("old board = %q, want it intact", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".board-") {
			t.Errorf("temp file %q left behind", e.Name())
		}
	}
}
