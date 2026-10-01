package relevo

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
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

// TestAccountSessionLocatorSearchesEveryClaudeHome pins the account-aware
// locator: a session written under an account's own config dir is found even
// though it is absent from the default home, and an empty pool searches only
// the default home, exactly as HomeSessionLocator does.
func TestAccountSessionLocatorSearchesEveryClaudeHome(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	work := t.TempDir()
	slug := filepath.Join(work, "projects", "-slug")
	if err := os.MkdirAll(slug, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(slug, "S.jsonl")
	if err := os.WriteFile(want, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	accounts := account.Set{{Name: "work", Harness: account.Claude, Groups: []string{"test"}, ConfigDir: work}}
	locate := AccountSessionLocator(home, accounts)
	if path, ok := locate("claude", "S"); !ok || path != want {
		t.Errorf("locate(claude, S) = %q, %v; want the account-home record %q", path, ok, want)
	}
	if _, ok := locate("opencode", "S"); ok {
		t.Error("a non-claude kind must not locate")
	}
	if _, ok := AccountSessionLocator(home, nil)("claude", "S"); ok {
		t.Error("with no accounts the default home alone must be searched")
	}
}
