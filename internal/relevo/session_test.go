package relevo

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHomeSessionLocatorGlobsAnySlug(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	slugA := filepath.Join(home, ".claude", "projects", "-a-slug")
	slugB := filepath.Join(home, ".claude", "projects", "-b-slug")
	if err := os.MkdirAll(slugA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(slugB, 0o755); err != nil {
		t.Fatal(err)
	}

	older := filepath.Join(slugB, "S.jsonl")
	newer := filepath.Join(slugA, "S.jsonl")
	if err := os.WriteFile(older, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	locate := HomeSessionLocator(home)

	if path, ok := locate("claude", "S"); !ok || path != newer {
		t.Errorf("locate(claude, S) = %q, %v; want the newer match %q, true", path, ok, newer)
	}
	if _, ok := locate("claude", "missing"); ok {
		t.Error("a missing session must not locate")
	}
	if _, ok := locate("opencode", "S"); ok {
		t.Error("a non-claude kind must not locate")
	}
	if _, ok := locate("claude", "../x"); ok {
		t.Error("a path separator in the id must be refused")
	}
	if _, ok := locate("claude", "*"); ok {
		t.Error("a glob metacharacter in the id must be refused")
	}
}
