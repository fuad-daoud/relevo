package store

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seedReservedStore builds a live binding named webshop with no files, so a
// reserved-key test starts from a directory that holds only what it plants.
func seedReservedStore(t *testing.T) (*Store, string) {
	t.Helper()
	s := New(t.TempDir())
	const binding = "webshop"
	if err := s.Save(newBinding(binding, "/repo")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return s, binding
}

// putRow authors one round file as a round_file row, the way relevo produces a
// reserved key: in the database, with no file on disk.
func putRow(t *testing.T, s *Store, binding string, round int, path string, body []byte) {
	t.Helper()
	if err := s.WithLock(func(tx *Tx) error {
		return tx.PutRoundFile(binding, round, path, body)
	}); err != nil {
		t.Fatalf("PutRoundFile(%s): %v", path, err)
	}
}

// writePlant writes body to path, creating the parent directory: a file that
// pretends to be an artifact relevo authored.
func writePlant(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), bindingDirMode); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), bindingFileMode); err != nil {
		t.Fatalf("write plant %s: %v", path, err)
	}
}

// reservedKeyCases names the four reserved shapes and the path helper each one
// resolves through.
func reservedKeyCases() []struct {
	name string
	path func(s *Store, binding string) string
} {
	return []struct {
		name string
		path func(s *Store, binding string) string
	}{
		{"001-diff.patch", func(s *Store, b string) string { return s.DiffPath(b, 1) }},
		{"001-drift.patch", func(s *Store, b string) string { return s.DriftPath(b, 1) }},
		{"001-builder-segments.json", func(s *Store, b string) string { return s.BuilderSegmentsPath(b, 1) }},
		{"001-7f2a3c1d-findings.md", func(s *Store, b string) string { return s.FindingsPath(b, 1, "7f2a3c1d") }},
	}
}

// TestReadFileRefusesAReservedRoundFileOnDisk pins the read policy for a
// reserved round-file name: the disk is never the record. A plant with no row
// is a miss, a row plus a plant answers with the row's bytes, and the negative
// shapes -- a non-reserved flat key and a nested artifact name -- still read
// the disk.
func TestReadFileRefusesAReservedRoundFileOnDisk(t *testing.T) {
	const row = "the row's bytes\n"
	const plant = "the plant's bytes\n"

	for _, tc := range reservedKeyCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("a plant with no row is a miss", func(t *testing.T) {
				s, binding := seedReservedStore(t)
				path := tc.path(s, binding)
				writePlant(t, path, plant)

				body, err := s.ReadFile(path)
				if !errors.Is(err, fs.ErrNotExist) || !os.IsNotExist(err) {
					t.Fatalf("ReadFile(%s) err = %v, want a not-exist error", tc.name, err)
				}
				if body != nil {
					t.Errorf("ReadFile(%s) = %q, want no bytes: the plant must never be returned", tc.name, body)
				}
			})

			t.Run("a row plus a plant answers with the row", func(t *testing.T) {
				s, binding := seedReservedStore(t)
				path := tc.path(s, binding)
				putRow(t, s, binding, 1, path, []byte(row))
				writePlant(t, path, plant)

				body, err := s.ReadFile(path)
				if err != nil {
					t.Fatalf("ReadFile(%s): %v", tc.name, err)
				}
				if string(body) != row {
					t.Errorf("ReadFile(%s) = %q, want the row's %q", tc.name, body, row)
				}
			})
		})
	}

	t.Run("non-reserved keys keep reading the disk", func(t *testing.T) {
		s, binding := seedReservedStore(t)
		for _, path := range []string{
			s.ReportPath(binding, 1),
			filepath.Join(s.Dir(binding), "004-reviewer", "findings.md"),
		} {
			writePlant(t, path, plant)
			body, err := s.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile(%s): %v", path, err)
			}
			if string(body) != plant {
				t.Errorf("ReadFile(%s) = %q, want the plant's %q", path, body, plant)
			}
		}
	})
}

// TestStatFileRefusesAReservedRoundFileOnDisk pins the stat policy: a plant
// must not set a reserved key's size or mtime; the row's answer instead.
func TestStatFileRefusesAReservedRoundFileOnDisk(t *testing.T) {
	s, binding := seedReservedStore(t)
	const row = "the row's bytes\n"
	plantTime := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)

	path := s.DiffPath(binding, 1)
	before := time.Now().UTC().Add(-time.Second)
	putRow(t, s, binding, 1, path, []byte(row))

	writePlant(t, path, "a much longer planted body than the row's\n")
	if err := os.Chtimes(path, plantTime, plantTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	size, mtime, ok, err := s.StatFile(path)
	if err != nil || !ok {
		t.Fatalf("StatFile(%s) = (ok %v, err %v), want the row", path, ok, err)
	}
	if size != int64(len(row)) {
		t.Errorf("StatFile size = %d, want the row's %d: the plant must not set it", size, len(row))
	}
	if !mtime.After(before) {
		t.Errorf("StatFile mtime = %v, want the row's stamp, not the plant's %v", mtime, plantTime)
	}
}

// TestReadFilePrefersDiskForPromptAndStream pins the load-bearing cases this
// round must not change: prompts, streams and builder logs stay disk-first,
// because a resend re-stages a prompt for a round whose earlier one is already
// sealed and the fresh file must win.
func TestReadFilePrefersDiskForPromptAndStream(t *testing.T) {
	const row = "the row's bytes\n"
	const fresh = "the fresh file's bytes\n"

	cases := []struct {
		name string
		path func(s *Store, binding string) string
	}{
		{"prompt", func(s *Store, b string) string { return s.PromptPath(b, 1) }},
		{"stream", func(s *Store, b string) string { return s.StreamPath(b, 1) }},
		{"builder log", func(s *Store, b string) string { return s.BuilderLogPath(b, 1) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, binding := seedReservedStore(t)
			path := tc.path(s, binding)
			putRow(t, s, binding, 1, path, []byte(row))

			writePlant(t, path, fresh)
			body, err := s.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile(%s): %v", path, err)
			}
			if string(body) != fresh {
				t.Errorf("ReadFile(%s) = %q, want the fresh file's %q", path, body, fresh)
			}

			if err := os.Remove(path); err != nil {
				t.Fatalf("remove %s: %v", path, err)
			}
			body, err = s.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile(%s) after the file is gone: %v", path, err)
			}
			if !bytes.Equal(body, []byte(row)) {
				t.Errorf("ReadFile(%s) = %q, want the sealed row's %q", path, body, row)
			}
		})
	}
}
