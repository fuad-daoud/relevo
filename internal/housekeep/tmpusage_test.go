package housekeep

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// baseTime is the fixed clock the planted entries carry, so mtimes stay
// deterministic across the walk tests.
var baseTime = time.Unix(1757000000, 0).UTC()

// tmpMkFile plants one file with its own mtime, the way a leftover looks to
// the walk: the name and the clock are all it reads.
func tmpMkFile(t *testing.T, path string, mod time.Time) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
	return path
}

// The usage walk splits one root's regular-file bytes by the same three
// groups the sweep decides on. Any budget that runs out flags the numbers as
// floors rather than reporting a smaller total as the truth.

func TestTmpUsageSplitsRoundsChecksLoose(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// Every planted file carries one byte, so each count below is the number
	// of files its group owns.
	tmpMkFile(t, filepath.Join(root, "rounds", "shop-001", "runner.jsonl"), baseTime)
	tmpMkFile(t, filepath.Join(root, "rounds", "shop-001", "deep", "out.log"), baseTime)
	tmpMkFile(t, filepath.Join(root, "checks", "unit-a", "gate.txt"), baseTime)
	tmpMkFile(t, filepath.Join(root, "req-body-7"), baseTime)
	tmpMkFile(t, filepath.Join(root, "bundle", "leaf"), baseTime)

	got := TmpUsageOf(root, time.Minute, 0)
	if got.Rounds != 2 || got.Checks != 1 || got.Loose != 2 {
		t.Errorf("TmpUsageOf = %+v, want rounds 2, checks 1, loose 2", got)
	}
	if got.Truncated {
		t.Errorf("a walk inside both budgets reports Truncated: %+v", got)
	}
}

func TestTmpUsageStopsAtBudget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for i := range 10 {
		tmpMkFile(t, filepath.Join(root, fmt.Sprintf("f-%d", i)), baseTime)
	}

	capped := TmpUsageOf(root, time.Minute, 4)
	if !capped.Truncated || capped.Loose != 4 {
		t.Errorf("entry budget = %+v, want the walk stopped at 4 entries and truncated", capped)
	}
	full := TmpUsageOf(root, time.Minute, 0)
	if full.Truncated || full.Loose != 10 {
		t.Errorf("no entry budget = %+v, want all 10 entries and no truncation", full)
	}

	// A spent time budget truncates the same way with no entry bound set,
	// while over an empty root the walk finished with nothing to spend.
	expired := TmpUsageOf(root, 0, 0)
	if !expired.Truncated {
		t.Errorf("time budget of zero over a full root = %+v, want truncated", expired)
	}
	empty := TmpUsageOf(t.TempDir(), 0, 0)
	if empty.Truncated || empty.Loose != 0 {
		t.Errorf("time budget of zero over an empty root = %+v, want a complete zero reading", empty)
	}
}

func TestTmpUsageDoesNotFollowSymlinks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()

	target := filepath.Join(outside, "payload")
	if err := os.WriteFile(target, make([]byte, 5000), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	tree := filepath.Join(outside, "tree")
	if err := os.MkdirAll(tree, 0o700); err != nil {
		t.Fatalf("mkdir target tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tree, "deep"), make([]byte, 5000), 0o600); err != nil {
		t.Fatalf("write target tree file: %v", err)
	}

	if err := os.Symlink(target, filepath.Join(root, "link.bin")); err != nil {
		t.Fatalf("symlink file: %v", err)
	}
	if err := os.Symlink(tree, filepath.Join(root, "linkdir")); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}
	// A link even wearing one of the two rule names is not walked either:
	// the sweep confines itself to the root, and so does the reading.
	if err := os.Symlink(tree, filepath.Join(root, "rounds")); err != nil {
		t.Fatalf("symlink rounds: %v", err)
	}
	tmpMkFile(t, filepath.Join(root, "real.txt"), baseTime)

	got := TmpUsageOf(root, time.Minute, 0)
	if got.Rounds != 0 || got.Loose != 1 {
		t.Errorf("TmpUsageOf = %+v, want the links sized as nothing: rounds 0, loose 1", got)
	}
	if got.Truncated {
		t.Errorf("symlinks neither descend nor fail the walk, want no truncation: %+v", got)
	}
}
