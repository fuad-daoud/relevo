//go:build unix

package diskfree

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFreeFollowsSymlink pins that Free measures the link's target filesystem:
// the served tmp link lives on /tmp while its bytes live under the serve
// root, and the disk preflight must read the latter.
func TestFreeFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	want, err := Free(target)
	if err != nil {
		t.Fatalf("Free(target): %v", err)
	}
	got, err := Free(link)
	if err != nil {
		t.Fatalf("Free(link): %v", err)
	}
	if got != want {
		t.Errorf("Free through the link = %d, want the target's %d", got, want)
	}
}
