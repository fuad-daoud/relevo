package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/pathscope"
)

// TestChangedFilesParsesEveryChange is step 3's gate: against a real
// repository, an add, a modify, a delete, a mode change and a rename (which
// --no-renames flattens to a delete plus an add) all parse.
func TestChangedFilesParsesEveryChange(t *testing.T) {
	requireGit(t)
	t.Parallel()

	dir := initRepo(t)
	writeGitFile(t, dir, "modify.txt", "v1\n")
	writeGitFile(t, dir, "delete.txt", "gone\n")
	writeGitFile(t, dir, "mode.txt", "#!/bin/sh\necho hi\n")
	writeGitFile(t, dir, "rename-a.txt", "r\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "first")
	first := mustHead(t, dir)

	writeGitFile(t, dir, "add.txt", "new\n")
	writeGitFile(t, dir, "modify.txt", "v2\n")
	if err := os.Remove(filepath.Join(dir, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "mode.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "mv", "rename-a.txt", "rename-b.txt")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "second")
	second := mustHead(t, dir)

	client := NewClient("", 0, 0)
	changes, err := client.ChangedFiles(context.Background(), dir, first, second)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	byPath := make(map[string]pathscope.Change, len(changes))
	for _, c := range changes {
		byPath[c.Path] = c
	}

	if c, ok := byPath["add.txt"]; !ok || c.Status != 'A' || c.OldMode != "000000" || c.NewMode != "100644" {
		t.Errorf("add.txt = %+v, want A 000000 100644", c)
	}
	if c, ok := byPath["modify.txt"]; !ok || c.Status != 'M' || c.OldMode != "100644" || c.NewMode != "100644" {
		t.Errorf("modify.txt = %+v, want M 100644 100644", c)
	}
	if c, ok := byPath["delete.txt"]; !ok || c.Status != 'D' || c.OldMode != "100644" || c.NewMode != "000000" {
		t.Errorf("delete.txt = %+v, want D 100644 000000", c)
	}
	if c, ok := byPath["mode.txt"]; !ok || c.Status != 'M' || c.OldMode != "100644" || c.NewMode != "100755" {
		t.Errorf("mode.txt = %+v, want M 100644 100755", c)
	}
	if c, ok := byPath["rename-a.txt"]; !ok || c.Status != 'D' {
		t.Errorf("rename-a.txt = %+v, want a D from the flattened rename", c)
	}
	if c, ok := byPath["rename-b.txt"]; !ok || c.Status != 'A' {
		t.Errorf("rename-b.txt = %+v, want an A from the flattened rename", c)
	}
}

// TestReadBlobReadsContents pins ReadBlob against a real object.
func TestReadBlobReadsContents(t *testing.T) {
	requireGit(t)
	t.Parallel()

	dir, first := initRepoWithCommit(t, "file.txt", "hello\n")
	_ = first
	client := NewClient("", 0, 0)
	oid := oidOf(t, dir, "file.txt")
	got, err := client.ReadBlob(context.Background(), dir, oid)
	if err != nil {
		t.Fatalf("ReadBlob: %v", err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("ReadBlob = %q, want %q", got, "hello\n")
	}
}

// oidOf returns the HEAD blob id of a path.
func oidOf(t *testing.T, dir, path string) string {
	t.Helper()
	out := runGit(t, dir, "rev-parse", "HEAD:"+path)
	return trimNewline(out)
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
