package store

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestPutRoundFileWritesARowWithNoFile pins the put's whole shape: a
// drift-shaped patch for a saved binding becomes a round_file row, reads back
// through ReadFile, is listed by RoundFiles, and never lands on disk.
func TestPutRoundFileWritesARowWithNoFile(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/webshop")
	b.Round = 2
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body := []byte("--- a/f\n+++ b/f\n@@ ...\n")
	path := s.DriftPath("webshop", 2)

	if err := s.WithLock(func(tx *Tx) error {
		return tx.PutRoundFile("webshop", 2, path, body)
	}); err != nil {
		t.Fatalf("PutRoundFile: %v", err)
	}

	got, err := s.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("ReadFile = %q, want %q", got, body)
	}

	names, err := s.RoundFiles("webshop")
	if err != nil {
		t.Fatalf("RoundFiles: %v", err)
	}
	if !slices.Contains(names, "002-drift.patch") {
		t.Errorf("RoundFiles = %v, want the put 002-drift.patch", names)
	}

	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("os.Stat(%s) = %v, want ErrNotExist (no file on disk)", path, err)
	}
}

// TestPutRoundFileUnknownBindingIsErrNotFound pins the lookup: a name with no
// live record is an error wrapping ErrNotFound, not a silent no-op.
func TestPutRoundFileUnknownBindingIsErrNotFound(t *testing.T) {
	s := New(t.TempDir())

	err := s.WithLock(func(tx *Tx) error {
		return tx.PutRoundFile("ghost", 2, s.DriftPath("ghost", 2), []byte("x"))
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("PutRoundFile unknown binding = %v, want ErrNotFound", err)
	}
}

// TestPutRoundFileRefusesBadInput pins the validation: a path outside the
// binding dir, a basename without the NNN- prefix, a round/base mismatch and a
// basename whose NNN- is round 0 are all errors, and none writes a row.
func TestPutRoundFileRefusesBadInput(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/webshop")
	b.Round = 2
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cases := []struct {
		name  string
		round int
		path  string
	}{
		{"path outside the binding dir", 2, filepath.Join(t.TempDir(), "002-drift.patch")},
		{"base without the NNN- prefix", 2, filepath.Join(s.Dir("webshop"), "drift.patch")},
		{"round/base mismatch", 2, s.DriftPath("webshop", 3)},
		{"base whose NNN- is round 0", 0, filepath.Join(s.Dir("webshop"), "000-drift.patch")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.WithLock(func(tx *Tx) error {
				return tx.PutRoundFile("webshop", tc.round, tc.path, []byte("x"))
			})
			if err == nil {
				t.Fatalf("PutRoundFile(%d, %s) = nil, want an error", tc.round, tc.path)
			}
		})
	}

	names, err := s.RoundFiles("webshop")
	if err != nil {
		t.Fatalf("RoundFiles: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("RoundFiles = %v, want nothing written for a refused put", names)
	}
}
