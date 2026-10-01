package store

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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

// reservedKeyCase names one reserved shape and the path helper it resolves
// through.
type reservedKeyCase struct {
	name string
	path func(s *Store, binding string) string
}

// reservedKeyCases names the seven reserved shapes and the path helper each one
// resolves through.
func reservedKeyCases() []reservedKeyCase {
	return []reservedKeyCase{
		{"001-diff.patch", func(s *Store, b string) string { return s.DiffPath(b, 1) }},
		{"001-plan-diff.patch", func(s *Store, b string) string { return s.PlanDiffPath(b, 1) }},
		{"001-chain-diff.patch", func(s *Store, b string) string { return s.ChainDiffPath(b, 1) }},
		{"001-drift.patch", func(s *Store, b string) string { return s.DriftPath(b, 1) }},
		{"001-builder-segments.json", func(s *Store, b string) string { return s.BuilderSegmentsPath(b, 1) }},
		{"001-7f2a3c1d-findings.md", func(s *Store, b string) string { return s.FindingsPath(b, 1, "7f2a3c1d") }},
		{"001-7f2a3c1d-ask.md", func(s *Store, b string) string { return s.AskPath(b, 1, "7f2a3c1d") }},
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

// sealRound runs SealRound under the state lock and returns how many files it
// sealed.
func sealRound(t *testing.T, s *Store, binding string, round int) int {
	t.Helper()
	var n int
	if err := s.WithLock(func(tx *Tx) error {
		var err error
		n, err = tx.SealRound(binding, round)
		return err
	}); err != nil {
		t.Fatalf("SealRound(%s, %d): %v", binding, round, err)
	}
	return n
}

// TestSealRoundLeavesAReservedRoundFileAlone pins the disk side: the round-file
// walk never treats a reserved name as a round file. A plant is not read, not
// sealed and not removed, the row the name belongs to keeps its bytes, and a
// plant alone neither becomes a row nor makes its round appear on disk. It
// walks reservedKeyCases, so every reserved key is pinned once.
func TestSealRoundLeavesAReservedRoundFileAlone(t *testing.T) {
	for _, tc := range reservedKeyCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("a row plus a plant", func(t *testing.T) { assertSealKeepsTheRow(t, tc) })
			t.Run("a plant with no row", func(t *testing.T) { assertSealLeavesThePlant(t, tc) })
		})
	}
}

// assertSealKeepsTheRow pins the row-plus-plant case for one reserved key: the
// walk seals nothing, the row answers a read, and the plant stays on disk.
func assertSealKeepsTheRow(t *testing.T, tc reservedKeyCase) {
	t.Helper()

	s, binding := seedReservedStore(t)
	const row = "the row's bytes\n"
	const plant = "the plant's bytes\n"

	path := tc.path(s, binding)
	putRow(t, s, binding, 1, path, []byte(row))
	writePlant(t, path, plant)

	if n := sealRound(t, s, binding, 1); n != 0 {
		t.Errorf("SealRound sealed %d files, want 0", n)
	}

	body, err := s.ReadFile(path)
	if err != nil || string(body) != row {
		t.Errorf("ReadFile(%s) = %q (err %v), want the row's %q", path, body, err, row)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the plant must stay on disk: %v", err)
	}
	if string(onDisk) != plant {
		t.Errorf("plant bytes = %q, want them byte-identical %q", onDisk, plant)
	}
	names, err := s.RoundFiles(binding)
	if err != nil {
		t.Fatalf("RoundFiles: %v", err)
	}
	if !slices.Contains(names, tc.name) {
		t.Errorf("RoundFiles = %v, want %s from the row", names, tc.name)
	}
}

// assertSealLeavesThePlant pins the plant-only case for one reserved key: no
// row is created, the plant is unlisted, and its round never appears.
func assertSealLeavesThePlant(t *testing.T, tc reservedKeyCase) {
	t.Helper()

	s, binding := seedReservedStore(t)
	path := tc.path(s, binding)
	writePlant(t, path, "the plant's bytes\n")

	if n := sealRound(t, s, binding, 1); n != 0 {
		t.Errorf("SealRound sealed %d files, want 0", n)
	}

	if body, err := s.ReadFile(path); !errors.Is(err, fs.ErrNotExist) || body != nil {
		t.Errorf("ReadFile(%s) = %q (err %v), want a miss: no row was created", path, body, err)
	}
	names, err := s.RoundFiles(binding)
	if err != nil {
		t.Fatalf("RoundFiles: %v", err)
	}
	if slices.Contains(names, tc.name) {
		t.Errorf("RoundFiles = %v, want the plant unlisted", names)
	}
	rounds, err := s.RoundsOnDisk(binding)
	if err != nil {
		t.Fatalf("RoundsOnDisk: %v", err)
	}
	if len(rounds) != 0 {
		t.Errorf("RoundsOnDisk = %v, want empty: a plant is not a round", rounds)
	}
}
