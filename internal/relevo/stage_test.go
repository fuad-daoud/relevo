package relevo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStagePlanOnlyTouchesARegularFile pins stagePlan's cases: an absent path
// is created with the bytes, an existing regular file is replaced in place with
// its mode kept, and a symlink or a directory is refused with nothing written
// through it.
func TestStagePlanOnlyTouchesARegularFile(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"absent is created with the bytes", func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "001-prompt.md")
			if err := stagePlan(path, []byte("plan body")); err != nil {
				t.Fatalf("stagePlan: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if string(got) != "plan body" {
				t.Errorf("bytes = %q, want %q", got, "plan body")
			}
		}},
		{"a regular file is replaced in place with its mode kept", func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "001-prompt.md")
			if err := os.WriteFile(path, []byte("old body"), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}

			if err := stagePlan(path, []byte("new body")); err != nil {
				t.Fatalf("stagePlan: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if string(got) != "new body" {
				t.Errorf("bytes = %q, want %q", got, "new body")
			}
			after, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !after.Mode().IsRegular() {
				t.Errorf("path is no longer a regular file: mode = %v", after.Mode())
			}
			if after.Mode() != before.Mode() {
				t.Errorf("mode = %v, want the existing %v kept", after.Mode(), before.Mode())
			}
		}},
		{"a symlink to a sentinel is refused", func(t *testing.T) {
			dir := t.TempDir()
			sentinel := filepath.Join(dir, "sentinel")
			if err := os.WriteFile(sentinel, []byte("sentinel"), 0o644); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "001-prompt.md")
			if err := os.Symlink(sentinel, path); err != nil {
				t.Fatal(err)
			}

			err := stagePlan(path, []byte("plan body"))
			if err == nil {
				t.Fatal("stagePlan: got nil error, want a refusal")
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error = %q, want it to name %s", err.Error(), path)
			}
			got, rerr := os.ReadFile(sentinel)
			if rerr != nil {
				t.Fatalf("ReadFile(sentinel): %v", rerr)
			}
			if string(got) != "sentinel" {
				t.Errorf("sentinel = %q, want it byte-identical", got)
			}
			fi, lerr := os.Lstat(path)
			if lerr != nil {
				t.Fatal(lerr)
			}
			if fi.Mode()&os.ModeSymlink == 0 {
				t.Errorf("path is no longer a symlink: mode = %v", fi.Mode())
			}
		}},
		{"a directory is refused", func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "001-prompt.md")
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}

			err := stagePlan(path, []byte("plan body"))
			if err == nil {
				t.Fatal("stagePlan: got nil error, want a refusal")
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error = %q, want it to name %s", err.Error(), path)
			}
		}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t)
		})
	}
}

// TestOpenAppendAppendsOnlyARegularFile pins openAppend's cases: an absent path
// is created, an existing regular file is appended to, and a symlink to a
// sentinel is refused with the sentinel untouched.
func TestOpenAppendAppendsOnlyARegularFile(t *testing.T) {
	t.Parallel()

	t.Run("absent is created", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "001-builder.log")
		f, err := openAppend(path)
		if err != nil {
			t.Fatalf("openAppend: %v", err)
		}
		if _, err := f.WriteString("first\n"); err != nil {
			t.Fatalf("WriteString: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) != "first\n" {
			t.Errorf("bytes = %q, want %q", got, "first\n")
		}
	})

	t.Run("a regular file is appended to", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "001-builder.log")
		if err := os.WriteFile(path, []byte("first\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		f, err := openAppend(path)
		if err != nil {
			t.Fatalf("openAppend: %v", err)
		}
		if _, err := f.WriteString("second\n"); err != nil {
			t.Fatalf("WriteString: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) != "first\nsecond\n" {
			t.Errorf("bytes = %q, want %q", got, "first\nsecond\n")
		}
	})

	t.Run("a symlink to a sentinel is refused", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		sentinel := filepath.Join(dir, "sentinel")
		if err := os.WriteFile(sentinel, []byte("sentinel"), 0o644); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "001-builder.log")
		if err := os.Symlink(sentinel, path); err != nil {
			t.Fatal(err)
		}

		f, err := openAppend(path)
		if err == nil {
			_ = f.Close()
			t.Fatal("openAppend: got nil error, want a refusal")
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("error = %q, want it to name %s", err.Error(), path)
		}
		got, rerr := os.ReadFile(sentinel)
		if rerr != nil {
			t.Fatalf("ReadFile(sentinel): %v", rerr)
		}
		if string(got) != "sentinel" {
			t.Errorf("sentinel = %q, want it unchanged", got)
		}
	})
}
