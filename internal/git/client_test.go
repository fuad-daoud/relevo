package git

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStatusDoesNotTakeIndexLock pins the index.lock hazard. First, a status read
// over an index a plain `git status` would refresh leaves .git/index
// byte-for-byte alone: writing it means taking index.lock, and it is
// GIT_OPTIONAL_LOCKS=0 that makes git skip that write. Second, with index.lock
// held by hand the way a concurrent git write holds it, the read still succeeds.
func TestStatusDoesNotTakeIndexLock(t *testing.T) {
	ctx := context.Background()
	repoDir, _ := initRepoWithCommit(t, "a.txt", "one\n")

	// Backdate the tracked file: the index's recorded stat data is then stale,
	// so a plain `git status` re-hashes the file, finds its content unchanged,
	// and rewrites the index to record the new mtime.
	stale := time.Now().Add(-2 * time.Second)
	if err := os.Chtimes(filepath.Join(repoDir, "a.txt"), stale, stale); err != nil {
		t.Fatalf("backdate a.txt: %v", err)
	}

	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	indexPath := filepath.Join(repoDir, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read .git/index: %v", err)
	}

	dirty, err := client.Dirty(ctx, repoDir)
	if err != nil {
		t.Fatalf("Dirty with a stale index: %v", err)
	}
	if dirty {
		t.Error("Dirty reported a change, but a.txt's content is unchanged")
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read .git/index after the status read: %v", err)
	}
	if !bytes.Equal(indexBefore, indexAfter) {
		t.Error("the status read rewrote .git/index, so it took index.lock; a builder's commit in this worktree would fail against it")
	}

	lockPath := filepath.Join(repoDir, ".git", "index.lock")
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("hold .git/index.lock: %v", err)
	}
	defer func() { _ = os.Remove(lockPath) }()

	if _, err := client.Dirty(ctx, repoDir); err != nil {
		t.Fatalf("Dirty while .git/index.lock is held: %v", err)
	}
}
