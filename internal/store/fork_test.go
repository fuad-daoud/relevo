package store

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestForkStateCopiesRoundFilesAndLog pins the copy: everything through the
// cut, paths rewritten into dst's directory, src untouched.
func TestForkStateCopiesRoundFilesAndLog(t *testing.T) {
	s := New(t.TempDir())
	srcName, dstName := "webshop", "webshop-fork"
	seedForkSource(t, s, srcName, 5)
	srcDir := s.Dir(srcName)

	srcSnap, srcEntriesBefore := snapshotDir(t, srcDir)
	srcLogBefore, err := s.ReadLog(srcName)
	if err != nil {
		t.Fatalf("ReadLog src: %v", err)
	}

	// ForkState saves dst itself: there is no Save after it.
	dst := newBinding(dstName, "/repo-fork")
	if err := s.WithLock(func(tx *Tx) error {
		return tx.ForkState(srcName, dst, 2)
	}); err != nil {
		t.Fatalf("ForkState: %v", err)
	}
	dstDir := s.Dir(dstName)

	checkDirEmpty(t, dstDir)
	checkCopiedLog(t, s, dstName, dstDir)

	wantFiles := []string{
		"001-custom.artifact", "001-diff.patch", "001-prompt.md", "001-question.md", "001-report.md",
		"002-custom.artifact", "002-diff.patch", "002-prompt.md", "002-question.md", "002-report.md",
	}
	dstFiles, err := s.RoundFiles(dstName)
	if err != nil {
		t.Fatalf("RoundFiles dst: %v", err)
	}
	if !slices.Equal(dstFiles, wantFiles) {
		t.Fatalf("RoundFiles(dst) = %v, want %v", dstFiles, wantFiles)
	}
	for _, name := range wantFiles {
		dstContent, err := s.ReadFile(filepath.Join(dstDir, name))
		if err != nil {
			t.Fatalf("ReadFile dst %s: %v", name, err)
		}
		if !bytes.Equal(dstContent, srcSnap[name]) {
			t.Errorf("file %s content mismatch: dst=%q, src=%q", name, dstContent, srcSnap[name])
		}
	}

	checkSourceUntouched(t, s, srcName, srcDir, srcEntriesBefore, srcSnap, srcLogBefore)
}

// seedForkSource creates rounds 1..rounds of srcName, each with five round
// files and three log entries.
func seedForkSource(t *testing.T, s *Store, srcName string, rounds int) {
	t.Helper()
	b := newBinding(srcName, "/repo")
	b.Round = rounds
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	srcDir := s.Dir(srcName)
	for r := 1; r <= rounds; r++ {
		files := map[string]string{
			s.PromptPath(srcName, r):   fmt.Sprintf("plan content for round %d", r),
			s.ReportPath(srcName, r):   fmt.Sprintf("report content for round %d", r),
			s.QuestionPath(srcName, r): fmt.Sprintf("question content for round %d", r),
			s.DiffPath(srcName, r):     fmt.Sprintf("diff content for round %d", r),
			filepath.Join(srcDir, fmt.Sprintf("%03d-custom.artifact", r)): fmt.Sprintf("custom artifact %d", r),
		}
		for path, content := range files {
			if err := os.WriteFile(path, []byte(content), bindingFileMode); err != nil {
				t.Fatalf("WriteFile %s: %v", path, err)
			}
		}

		deliveryTime := time.Now().UTC().Add(-time.Hour)
		for _, e := range []LogEntry{
			{Round: r, Direction: DirToBuilder, Kind: KindPrompt, Path: s.PromptPath(srcName, r), Confirmed: true},
			{Round: r, Direction: DirToMasterMind, Kind: KindReport, Path: s.ReportPath(srcName, r), DeliveredAt: &deliveryTime},
			{Round: r, Direction: DirToMasterMind, Kind: KindQuestion},
		} {
			if err := s.AppendLog(srcName, e); err != nil {
				t.Fatalf("AppendLog: %v", err)
			}
		}
	}
}

func snapshotDir(t *testing.T, dir string) (map[string][]byte, []os.DirEntry) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	snap := make(map[string][]byte, len(entries))
	for _, e := range entries {
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", e.Name(), err)
		}
		snap[e.Name()] = content
	}
	return snap, entries
}

func checkDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	if len(entries) != 0 {
		t.Fatalf("%s must hold no files after ForkState, got %v", dir, entries)
	}
}

// checkCopiedLog asserts dst holds src's six entries (3 per round through 2),
// all confirmed, undelivered, with paths pointing into dst's directory.
func checkCopiedLog(t *testing.T, s *Store, dstName, dstDir string) {
	t.Helper()
	dstLog, err := s.ReadLog(dstName)
	if err != nil {
		t.Fatalf("ReadLog dst: %v", err)
	}
	if len(dstLog) != 6 {
		t.Fatalf("got %d log entries in dst, want 6", len(dstLog))
	}
	for i, e := range dstLog {
		if e.Round > 2 {
			t.Errorf("entry %d has Round %d > 2", i, e.Round)
		}
		if !e.Confirmed {
			t.Errorf("entry %d Confirmed = false, want true", i)
		}
		if e.DeliveredAt != nil {
			t.Errorf("entry %d DeliveredAt = %v, want nil", i, e.DeliveredAt)
		}
		if e.Path != "" && filepath.Dir(e.Path) != dstDir {
			t.Errorf("entry %d Path = %q, want a path in %s", i, e.Path, dstDir)
		}
	}
	if got, want := dstLog[0].Path, s.PromptPath(dstName, 1); got != want {
		t.Errorf("first copied entry Path = %q, want %q", got, want)
	}
}

func checkSourceUntouched(t *testing.T, s *Store, srcName, srcDir string, entriesBefore []os.DirEntry, snap map[string][]byte, logBefore []LogEntry) {
	t.Helper()
	entriesAfter, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatalf("ReadDir src after: %v", err)
	}
	if len(entriesAfter) != len(entriesBefore) {
		t.Fatalf("src file count changed: got %d, want %d", len(entriesAfter), len(entriesBefore))
	}
	for _, e := range entriesAfter {
		content, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			t.Fatalf("ReadFile src after %s: %v", e.Name(), err)
		}
		orig, ok := snap[e.Name()]
		if !ok {
			t.Errorf("new file %s in src after fork", e.Name())
		} else if !bytes.Equal(content, orig) {
			t.Errorf("file %s in src modified after fork", e.Name())
		}
	}

	logAfter, err := s.ReadLog(srcName)
	if err != nil {
		t.Fatalf("ReadLog src after: %v", err)
	}
	if len(logAfter) != len(logBefore) {
		t.Fatalf("src log count changed: got %d, want %d", len(logAfter), len(logBefore))
	}
	for i := range logAfter {
		if logAfter[i].Confirmed != logBefore[i].Confirmed {
			t.Errorf("src log entry %d Confirmed changed", i)
		}
		if (logAfter[i].DeliveredAt == nil) != (logBefore[i].DeliveredAt == nil) {
			t.Errorf("src log entry %d DeliveredAt changed", i)
		}
	}
}

func TestForkStateValidationAndErrors(t *testing.T) {
	s := New(t.TempDir())
	srcName := "webshop"

	if err := s.Save(newBinding(srcName, "/repo")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := os.Mkdir(s.Dir("existing"), bindingDirMode); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	err := s.WithLock(func(tx *Tx) error {
		return tx.ForkState(srcName, newBinding("existing", "/repo-existing"), 1)
	})
	if err == nil {
		t.Fatal("ForkState into existing directory must error, got nil")
	}

	err = s.WithLock(func(tx *Tx) error {
		return tx.ForkState("nosuch", newBinding("dst", "/repo-dst"), 1)
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ForkState from nonexistent src: got %v, want ErrNotFound", err)
	}

	for _, bad := range []string{"", "123bad", "UPPER", "bad name"} {
		err = s.WithLock(func(tx *Tx) error {
			return tx.ForkState(srcName, newBinding(bad, "/repo-bad"), 1)
		})
		if err == nil {
			t.Errorf("ForkState with invalid dst %q must error, got nil", bad)
		}
	}

	for _, badRound := range []int{0, -1, -5} {
		err = s.WithLock(func(tx *Tx) error {
			return tx.ForkState(srcName, newBinding("validfork", "/repo-validfork"), badRound)
		})
		if err == nil {
			t.Errorf("ForkState with round %d must error, got nil", badRound)
		}
	}

	// Onto an existing record with no directory: the record owns the name even
	// though nothing is on disk for it.
	if err := s.Save(newBinding("taken", "/repo-taken")); err != nil {
		t.Fatalf("Save taken: %v", err)
	}
	if err := os.RemoveAll(s.Dir("taken")); err != nil {
		t.Fatalf("RemoveAll taken dir: %v", err)
	}
	err = s.WithLock(func(tx *Tx) error {
		return tx.ForkState(srcName, newBinding("taken", "/repo-taken2"), 1)
	})
	if err == nil {
		t.Fatal("ForkState onto an existing record must error, got nil")
	}
}

// TestForkStateMidCopyFailureLeavesNoDst pins the cleanup: a fork that fails
// partway leaves neither dst's directory nor its record.
func TestForkStateMidCopyFailureLeavesNoDst(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(t *testing.T, s *Store, srcName string)
	}{
		{
			name: "unreadable file",
			corrupt: func(t *testing.T, s *Store, srcName string) {
				t.Helper()
				if err := os.WriteFile(s.PromptPath(srcName, 1), []byte("plan 1"), bindingFileMode); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				unreadable := s.ReportPath(srcName, 1)
				if err := os.WriteFile(unreadable, []byte("report 1"), bindingFileMode); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				if err := os.Chmod(unreadable, 0o000); err != nil {
					t.Fatalf("Chmod 0000: %v", err)
				}
				t.Cleanup(func() { _ = os.Chmod(unreadable, bindingFileMode) })
			},
		},
		{
			name: "corrupted log",
			corrupt: func(t *testing.T, s *Store, srcName string) {
				t.Helper()
				putEventJSON(t, s, srcName, []string{"invalid-json"})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(t.TempDir())
			srcName := "webshop"
			if err := s.Save(newBinding(srcName, "/repo")); err != nil {
				t.Fatalf("Save: %v", err)
			}
			tc.corrupt(t, s, srcName)

			dstName := "dstfailed"
			err := s.WithLock(func(tx *Tx) error {
				return tx.ForkState(srcName, newBinding(dstName, "/repo-dstfailed"), 1)
			})
			if err == nil {
				t.Fatal("ForkState must error, got nil")
			}
			if _, err := os.Stat(s.Dir(dstName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("dst directory must be removed on failure, got err: %v", err)
			}
			if _, err := s.Load(dstName); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Load(%s) after a failed fork = %v, want ErrNotFound", dstName, err)
			}
		})
	}
}

// TestForkWritesNoFiles pins that a fork copies both on-disk and sealed files
// into round_file rows, and never puts a file in dst's directory.
func TestForkWritesNoFiles(t *testing.T) {
	s := New(t.TempDir())
	srcName, dstName := "webshop", "webshop-fork"
	onDisk, sealed := seedSealedForkSource(t, s, srcName)

	dst := newBinding(dstName, "/repo-fork")
	if err := s.WithLock(func(tx *Tx) error {
		return tx.ForkState(srcName, dst, 2)
	}); err != nil {
		t.Fatalf("ForkState: %v", err)
	}

	// Dir(dst) is kept, empty: Save created it and nothing puts a file in it.
	checkDirEmpty(t, s.Dir(dstName))

	names, err := s.RoundFiles(dstName)
	if err != nil {
		t.Fatalf("RoundFiles dst: %v", err)
	}
	want := []string{"001-prompt.md", "001-report.md", "002-prompt.md", "002-report.md"}
	if !slices.Equal(names, want) {
		t.Fatalf("RoundFiles(dst) = %v, want %v", names, want)
	}

	for _, files := range []map[string]string{onDisk, sealed} {
		for path, content := range files {
			base := filepath.Base(path)
			body, err := s.ReadFile(filepath.Join(s.Dir(dstName), base))
			if err != nil {
				t.Fatalf("ReadFile dst %s: %v", base, err)
			}
			if string(body) != content {
				t.Errorf("dst %s = %q, want %q", base, body, content)
			}
		}
	}
}

// seedSealedForkSource leaves round 1 on disk and seals round 2, returning both.
func seedSealedForkSource(t *testing.T, s *Store, srcName string) (onDisk, sealed map[string]string) {
	t.Helper()
	b := newBinding(srcName, "/repo")
	b.Round = 3
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	onDisk = map[string]string{
		s.PromptPath(srcName, 1): "round 1 plan",
		s.ReportPath(srcName, 1): "round 1 report",
	}
	sealed = map[string]string{
		s.PromptPath(srcName, 2): "round 2 plan",
		s.ReportPath(srcName, 2): "round 2 report",
	}
	for _, files := range []map[string]string{onDisk, sealed} {
		for path, content := range files {
			if err := os.WriteFile(path, []byte(content), bindingFileMode); err != nil {
				t.Fatalf("WriteFile %s: %v", path, err)
			}
		}
	}
	if err := s.WithLock(func(tx *Tx) error {
		_, err := tx.SealRound(srcName, 2)
		return err
	}); err != nil {
		t.Fatalf("SealRound: %v", err)
	}
	for path := range sealed {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s must be sealed off disk, got err: %v", path, err)
		}
	}
	return onDisk, sealed
}

func TestRoundOfFile(t *testing.T) {
	tests := []struct {
		name      string
		filename  string
		wantRound int
		wantOk    bool
	}{
		{"plan suffix", "001-plan.md", 1, true},
		{"report suffix", "002-report.md", 2, true},
		{"question suffix", "003-question.md", 3, true},
		{"diff suffix", "004-diff.patch", 4, true},
		{"custom text suffix", "005-custom.txt", 5, true},
		{"archive suffix", "010-backup.patch", 10, true},
		{"single digit round", "1-plan.md", 1, true},
		{"large round number", "999-report.md", 999, true},
		{"multiple hyphens in filename", "002-extra-long-name.md", 2, true},
		{"four digit round", "1000-future.artifact", 1000, true},

		{"bind.json", "bind.json", 0, false},
		{"log.jsonl", "log.jsonl", 0, false},
		{"hidden worktrees", ".worktrees", 0, false},
		{"hidden archive", ".archive", 0, false},
		{"lock file", ".lock", 0, false},
		{"empty string", "", 0, false},

		{"no number before hyphen", "-plan.md", 0, false},
		{"no suffix after hyphen", "001-", 0, false},
		{"no hyphen", "001", 0, false},
		{"alphabetic prefix", "abc-plan.md", 0, false},
		{"alphanumeric prefix", "01a-plan.md", 0, false},
		{"plus prefix", "+01-plan.md", 0, false},
		{"minus prefix", "-01-plan.md", 0, false},
		{"round zero", "000-plan.md", 0, false},
		{"round single zero", "0-plan.md", 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotRound, gotOk := roundOfFile(tc.filename)
			if gotOk != tc.wantOk || gotRound != tc.wantRound {
				t.Errorf("roundOfFile(%q) = (%d, %v), want (%d, %v)",
					tc.filename, gotRound, gotOk, tc.wantRound, tc.wantOk)
			}
		})
	}
}
