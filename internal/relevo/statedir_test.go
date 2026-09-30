package relevo

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureRealDirRefusesALinkedComponent pins the refusal: a symlink planted
// anywhere between the state root and the destination is never descended, even
// when it points at a real directory.
func TestEnsureRealDirRefusesALinkedComponent(t *testing.T) {
	t.Run("dir itself is a link", func(t *testing.T) {
		root := t.TempDir()
		target := t.TempDir()
		dir := filepath.Join(root, "001-reviewer")
		if err := os.Symlink(target, dir); err != nil {
			t.Fatal(err)
		}
		if err := ensureRealDir(root, dir); err == nil {
			t.Fatal("ensureRealDir accepted a linked destination directory")
		}
		if entries, _ := os.ReadDir(target); len(entries) != 0 {
			t.Errorf("the link target holds %d entries, want none", len(entries))
		}
	})

	t.Run("a parent component is a link while the immediate dir is absent", func(t *testing.T) {
		root := t.TempDir()
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(root, "001-reviewer")); err != nil {
			t.Fatal(err)
		}
		if err := ensureRealDir(root, filepath.Join(root, "001-reviewer", "site")); err == nil {
			t.Fatal("ensureRealDir descended a linked parent component")
		}
		if entries, _ := os.ReadDir(target); len(entries) != 0 {
			t.Errorf("the link target holds %d entries, want none", len(entries))
		}
	})

	t.Run("a component is a plain file", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "001-reviewer"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := ensureRealDir(root, filepath.Join(root, "001-reviewer", "site")); err == nil {
			t.Fatal("ensureRealDir descended a plain file")
		}
	})

	t.Run("a real nested directory is created", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "001-reviewer", "site")
		if err := ensureRealDir(root, dir); err != nil {
			t.Fatalf("ensureRealDir: %v", err)
		}
		path := filepath.Join(dir, "index.html")
		if err := os.WriteFile(path, []byte("hi"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != "hi" {
			t.Fatalf("read %s = %q, %v", path, got, err)
		}
	})
}

// TestOpenAppendRegularRefusesANonRegularFile pins the append helper: a link or
// a directory at the log path is refused, and a regular file still appends.
func TestOpenAppendRegularRefusesANonRegularFile(t *testing.T) {
	t.Run("link refused", func(t *testing.T) {
		dir := t.TempDir()
		victim := filepath.Join(dir, "victim")
		if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "001-builder.log")
		if err := os.Symlink(victim, path); err != nil {
			t.Fatal(err)
		}
		if _, err := openAppendRegular(path); err == nil {
			t.Fatal("openAppendRegular followed a planted link")
		}
		if got, _ := os.ReadFile(victim); string(got) != "keep" {
			t.Errorf("victim = %q, want it untouched", got)
		}
	})

	t.Run("directory refused", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "001-builder.log")
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := openAppendRegular(path); err == nil {
			t.Fatal("openAppendRegular accepted a directory")
		}
	})

	t.Run("regular file appends", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "001-builder.log")
		if err := os.WriteFile(path, []byte("a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := openAppendRegular(path)
		if err != nil {
			t.Fatalf("openAppendRegular: %v", err)
		}
		if _, err := f.WriteString("b\n"); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); string(got) != "a\nb\n" {
			t.Errorf("file = %q, want %q", got, "a\nb\n")
		}
	})
}
