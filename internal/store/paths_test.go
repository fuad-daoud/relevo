package store

import (
	"os"
	"testing"
)

// TestEnsureScratchDirChownsThroughCallback pins that EnsureScratchDir creates
// .worktrees/.scratch and hands exactly that directory to the tenant chown
// callback, and that it does not chown again when the directory already exists.
// The callback is the seam a user-mode server uses; a none-mode store has none.
func TestEnsureScratchDirChownsThroughCallback(t *testing.T) {
	s := New(t.TempDir())
	var got []string
	s.SetTenantChown(func(path string) error {
		got = append(got, path)
		return nil
	})

	if err := s.EnsureScratchDir(); err != nil {
		t.Fatalf("EnsureScratchDir: %v", err)
	}
	if fi, err := os.Stat(s.ScratchWorktreeDir()); err != nil || !fi.IsDir() {
		t.Fatalf("scratch dir not created: %v", err)
	}
	if len(got) != 1 || got[0] != s.ScratchWorktreeDir() {
		t.Fatalf("chown callback got %v, want [%s]", got, s.ScratchWorktreeDir())
	}

	if err := s.EnsureScratchDir(); err != nil {
		t.Fatalf("EnsureScratchDir again: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("second EnsureScratchDir chowned again: %v", got)
	}
}

// TestEnsureOutDirChownsThroughCallback pins the same rule for out/: the
// created directory is handed to the callback, and a pre-existing one is not.
func TestEnsureOutDirChownsThroughCallback(t *testing.T) {
	s := New(t.TempDir())
	var got []string
	s.SetTenantChown(func(path string) error {
		got = append(got, path)
		return nil
	})

	if err := s.EnsureOutDir("api"); err != nil {
		t.Fatalf("EnsureOutDir: %v", err)
	}
	if len(got) != 1 || got[0] != s.OutDir("api") {
		t.Fatalf("chown callback got %v, want [%s]", got, s.OutDir("api"))
	}
	if err := s.EnsureOutDir("api"); err != nil {
		t.Fatalf("EnsureOutDir again: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("second EnsureOutDir chowned again: %v", got)
	}
}
