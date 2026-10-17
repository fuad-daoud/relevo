package store

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestPromptCopyPath(t *testing.T) {
	s := New(t.TempDir())
	got := s.PromptCopyPath("webshop", 1)
	want := filepath.Join(s.OutDir("webshop"), ".001-prompt.md")
	if got != want {
		t.Errorf("PromptCopyPath = %q, want %q", got, want)
	}
}

func assertPromptCopyNotListed(t *testing.T, s *Store, binding string) {
	t.Helper()
	rounds, err := s.RoundsOnDisk(binding)
	if err != nil {
		t.Fatalf("RoundsOnDisk: %v", err)
	}
	if len(rounds) != 0 {
		t.Errorf("RoundsOnDisk with only prompt copy = %v, want empty", rounds)
	}
	files, err := s.RoundFiles(binding)
	if err != nil {
		t.Fatalf("RoundFiles: %v", err)
	}
	for _, f := range files {
		if f == ".001-prompt.md" || filepath.Base(f) == ".001-prompt.md" {
			t.Errorf("RoundFiles contains prompt copy %q", f)
		}
	}
}

func assertPromptCopySealedState(t *testing.T, s *Store, binding string, copyPath, origPrompt string, copyBody []byte) {
	t.Helper()
	// Copy must be gone from disk.
	if _, err := os.Stat(copyPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("prompt copy %s still exists on disk after seal: %v", copyPath, err)
	}

	// Original prompt must be gone from disk, but readable through Store.ReadFile.
	if _, err := os.Stat(origPrompt); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("original prompt %s still exists on disk after seal", origPrompt)
	}
	if sealedPrompt, err := s.ReadFile(origPrompt); err != nil || !bytes.Equal(sealedPrompt, copyBody) {
		t.Errorf("ReadFile(origPrompt) = (%q, %v), want %q", sealedPrompt, err, copyBody)
	}

	// Prompt copy was NOT sealed into a row in the database.
	allFiles, err := s.RoundFiles(binding)
	if err != nil {
		t.Fatalf("RoundFiles: %v", err)
	}
	for _, f := range allFiles {
		if f == ".001-prompt.md" || filepath.Base(f) == ".001-prompt.md" {
			t.Errorf("RoundFiles contains prompt copy %q after seal", f)
		}
	}

	// out/ directory must be empty.
	entries, err := os.ReadDir(s.OutDir(binding))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadDir out/: %v", err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("out/ is not empty after seal: %v", names)
	}
}

func TestPromptCopyNotIngestedAsRunnerOutput(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/webshop")
	b.Round = 1
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.EnsureOutDir("webshop"); err != nil {
		t.Fatalf("EnsureOutDir: %v", err)
	}

	// 1. A prompt copy alone is not counted by RoundsOnDisk or RoundFiles.
	copyBody := []byte("staged prompt body\n")
	copyPath, err := s.StagePromptCopy("webshop", 1, copyBody)
	if err != nil {
		t.Fatalf("StagePromptCopy: %v", err)
	}
	if copyPath != s.PromptCopyPath("webshop", 1) {
		t.Errorf("copyPath = %q, want %q", copyPath, s.PromptCopyPath("webshop", 1))
	}
	data, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatalf("ReadFile copy: %v", err)
	}
	if !bytes.Equal(data, copyBody) {
		t.Errorf("copy content = %q, want %q", data, copyBody)
	}

	assertPromptCopyNotListed(t, s, "webshop")

	// 2. Add real round files: original prompt in binding dir, report in out/.
	origPrompt := s.PromptPath("webshop", 1)
	if err := os.WriteFile(origPrompt, copyBody, bindingFileMode); err != nil {
		t.Fatalf("write original prompt: %v", err)
	}
	reportPath := s.ReportPath("webshop", 1)
	if err := os.WriteFile(reportPath, []byte("report\n"), bindingFileMode); err != nil {
		t.Fatalf("write report: %v", err)
	}

	rounds, err := s.RoundsOnDisk("webshop")
	if err != nil {
		t.Fatalf("RoundsOnDisk: %v", err)
	}
	if len(rounds) != 1 || rounds[0] != 1 {
		t.Errorf("RoundsOnDisk = %v, want [1]", rounds)
	}

	// 3. Seal round: original prompt and report are sealed into rows and removed;
	// prompt copy is removed by SealRound (not sealed); out/ ends empty.
	if err := s.WithLock(func(tx *Tx) error {
		n, err := tx.SealRound("webshop", 1)
		if err != nil {
			return err
		}
		if n != 2 {
			t.Errorf("SealRound sealed %d files, want 2 (prompt and report)", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("SealRound: %v", err)
	}

	assertPromptCopySealedState(t, s, "webshop", copyPath, origPrompt, copyBody)
}

func TestStagePromptCopyCreatesOutDir(t *testing.T) {
	// A launch stages the copy while its argv is built, which is before
	// startProcess calls EnsureOutDir. Staging must create out/ itself: a real
	// round on a binding whose out/ does not exist yet failed with
	// "open out dir ...: no such file or directory" when it did not.
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/webshop")
	b.Round = 1
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if fi, err := os.Lstat(s.OutDir("webshop")); err == nil {
		t.Fatalf("out/ already exists (%v); the scenario needs it absent", fi.Mode())
	}

	body := []byte("staged on a fresh binding\n")
	copyPath, err := s.StagePromptCopy("webshop", 1, body)
	if err != nil {
		t.Fatalf("StagePromptCopy with no out/: %v", err)
	}
	if copyPath != s.PromptCopyPath("webshop", 1) {
		t.Errorf("copyPath = %q, want %q", copyPath, s.PromptCopyPath("webshop", 1))
	}
	got, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatalf("read copy %s: %v", copyPath, err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("copy content = %q, want %q", got, body)
	}
}

func TestStagePromptCopyRefusesNonDirectoryOut(t *testing.T) {
	// out/ must stay a real directory: a file planted at that path is refused,
	// so the round fails instead of staging outside its confined root.
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/webshop")
	b.Round = 1
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.OutDir("webshop")), 0o755); err != nil {
		t.Fatalf("mkdir binding dir: %v", err)
	}
	if err := os.WriteFile(s.OutDir("webshop"), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatalf("plant file at out/: %v", err)
	}

	if _, err := s.StagePromptCopy("webshop", 1, []byte("body\n")); err == nil {
		t.Error("StagePromptCopy with a file at out/ must fail, got nil error")
	}
}

func TestStagePromptCopyRefusesSymlink(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/webshop")
	b.Round = 1
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.EnsureOutDir("webshop"); err != nil {
		t.Fatalf("EnsureOutDir: %v", err)
	}

	// Plant a symlink at the copy path pointing to a target file.
	targetPath := filepath.Join(t.TempDir(), "target.txt")
	targetInitial := []byte("do not overwrite target")
	if err := os.WriteFile(targetPath, targetInitial, 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}

	copyPath := s.PromptCopyPath("webshop", 1)
	if err := os.Symlink(targetPath, copyPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// Staging must replace the symlink without modifying the target.
	newBody := []byte("new prompt body")
	if _, err := s.StagePromptCopy("webshop", 1, newBody); err != nil {
		t.Fatalf("StagePromptCopy: %v", err)
	}

	targetGot, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if !bytes.Equal(targetGot, targetInitial) {
		t.Errorf("target was modified through symlink: got %q, want %q", targetGot, targetInitial)
	}

	copyGot, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatalf("read copy: %v", err)
	}
	if !bytes.Equal(copyGot, newBody) {
		t.Errorf("copy content = %q, want %q", copyGot, newBody)
	}

	// Verify copy is now a regular file, not a symlink.
	fi, err := os.Lstat(copyPath)
	if err != nil {
		t.Fatalf("Lstat copy: %v", err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		t.Errorf("copy %s is still a symlink", copyPath)
	}
}
