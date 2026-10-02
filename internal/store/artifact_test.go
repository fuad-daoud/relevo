package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestSealRoundSealsAnArtifactDirAndRemovesIt pins the nested seal: a round's
// flat file and the files of its artifact directory, in a subdirectory too,
// become round_file rows named NNN-<actor>/<rel>, the files and both
// directories leave disk, and ReadFile answers on each original path.
func TestSealRoundSealsAnArtifactDirAndRemovesIt(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/webshop")
	b.Round = 5
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	files := map[string]string{
		s.ReportPath("webshop", 4): "flat report\n",
		filepath.Join(s.ArtifactDir("webshop", 4, "reviewer"), "summary.md"):         "summary\n",
		filepath.Join(s.ArtifactDir("webshop", 4, "reviewer"), "site", "index.html"): "<html>\n",
	}
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), bindingDirMode); err != nil {
			t.Fatalf("MkdirAll %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(body), bindingFileMode); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	var n int
	if err := s.WithLock(func(tx *Tx) error {
		var err error
		n, err = tx.SealRound("webshop", 4)
		return err
	}); err != nil {
		t.Fatalf("SealRound: %v", err)
	}
	if n != len(files) {
		t.Errorf("SealRound sealed %d files, want %d", n, len(files))
	}

	names, err := s.RoundFiles("webshop")
	if err != nil {
		t.Fatalf("RoundFiles: %v", err)
	}
	for _, want := range []string{
		"004-report.md",
		"004-reviewer/summary.md",
		"004-reviewer/site/index.html",
	} {
		if !slices.Contains(names, want) {
			t.Errorf("RoundFiles = %v, want %q", names, want)
		}
	}

	for path, body := range files {
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is still on disk: %v", path, err)
		}
		got, err := s.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", path, err)
		}
		if string(got) != body {
			t.Errorf("ReadFile(%s) = %q, want %q", path, got, body)
		}
	}

	for _, dir := range []string{
		s.ArtifactDir("webshop", 4, "reviewer"),
		filepath.Join(s.ArtifactDir("webshop", 4, "reviewer"), "site"),
	} {
		if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is still on disk: %v", dir, err)
		}
	}
}

// TestSealRoundSkipsSymlinksAndDotfiles pins the walk's security: a symlink and
// a dot-file in the artifact directory are neither sealed nor followed, so
// they stay on disk and the directory is not removed.
func TestSealRoundSkipsSymlinksAndDotfiles(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/webshop")
	b.Round = 5
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dir := s.ArtifactDir("webshop", 4, "reviewer")
	if err := os.MkdirAll(dir, bindingDirMode); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte("summary\n"), bindingFileMode); err != nil {
		t.Fatalf("write summary: %v", err)
	}
	hidden := filepath.Join(dir, ".hidden")
	if err := os.WriteFile(hidden, []byte("hidden\n"), bindingFileMode); err != nil {
		t.Fatalf("write hidden: %v", err)
	}
	target := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(target, []byte("outside\n"), bindingFileMode); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	var n int
	if err := s.WithLock(func(tx *Tx) error {
		var err error
		n, err = tx.SealRound("webshop", 4)
		return err
	}); err != nil {
		t.Fatalf("SealRound: %v", err)
	}
	if n != 1 {
		t.Errorf("SealRound sealed %d files, want only the regular summary.md", n)
	}

	names, err := s.RoundFiles("webshop")
	if err != nil {
		t.Fatalf("RoundFiles: %v", err)
	}
	if !slices.Equal(names, []string{"004-reviewer/summary.md"}) {
		t.Fatalf("RoundFiles = %v, want only the sealed summary", names)
	}

	if info, err := os.Lstat(link); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("the symlink must stay on disk, got info %v err %v", info, err)
	}
	if _, err := os.Stat(hidden); err != nil {
		t.Errorf(".hidden must stay on disk: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the non-empty artifact dir must stay on disk: %v", err)
	}
	if body, err := os.ReadFile(target); err != nil || string(body) != "outside\n" {
		t.Errorf("the symlink target = %q, %v; want it untouched", body, err)
	}
}

// TestFlatRoundFilesUnchanged pins that today's flat round files behave exactly
// as they did before artifact directories: sealed under their own basenames,
// with no directory prefix, and readable back on their original paths.
func TestFlatRoundFilesUnchanged(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/webshop")
	b.Round = 4
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	flat := map[string]string{
		"003-plan.md":   "plan\n",
		"003-report.md": "report\n",
		"003-done":      "",
	}
	for base, body := range flat {
		if err := os.WriteFile(filepath.Join(s.Dir("webshop"), base), []byte(body), bindingFileMode); err != nil {
			t.Fatalf("write %s: %v", base, err)
		}
	}

	if err := s.WithLock(func(tx *Tx) error {
		n, err := tx.SealRound("webshop", 3)
		if err != nil {
			return err
		}
		if n != len(flat) {
			t.Errorf("SealRound sealed %d files, want %d", n, len(flat))
		}
		return nil
	}); err != nil {
		t.Fatalf("SealRound: %v", err)
	}

	names, err := s.RoundFiles("webshop")
	if err != nil {
		t.Fatalf("RoundFiles: %v", err)
	}
	if want := []string{"003-done", "003-plan.md", "003-report.md"}; !slices.Equal(names, want) {
		t.Fatalf("RoundFiles = %v, want %v", names, want)
	}
	for base, body := range flat {
		got, err := s.ReadFile(filepath.Join(s.Dir("webshop"), base))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", base, err)
		}
		if string(got) != body {
			t.Errorf("ReadFile(%s) = %q, want %q", base, got, body)
		}
	}
}

// TestRoundsOnDiskCountsAnArtifactDir pins that a NNN-* directory holding at
// least one file counts as round NNN on disk, and an empty one does not.
func TestRoundsOnDiskCountsAnArtifactDir(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/webshop")
	b.Round = 6
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dir := s.ArtifactDir("webshop", 5, "reviewer")
	if err := os.MkdirAll(dir, bindingDirMode); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(s.ArtifactDir("webshop", 5, "reviewer"), "summary.md"), []byte("summary\n"), bindingFileMode); err != nil {
		t.Fatalf("write summary: %v", err)
	}
	if err := os.MkdirAll(s.ArtifactDir("webshop", 6, "reviewer"), bindingDirMode); err != nil {
		t.Fatalf("MkdirAll empty: %v", err)
	}

	rounds, err := s.RoundsOnDisk("webshop")
	if err != nil {
		t.Fatalf("RoundsOnDisk: %v", err)
	}
	if !slices.Equal(rounds, []int{5}) {
		t.Fatalf("RoundsOnDisk = %v, want [5]", rounds)
	}
}

// TestPutRoundFileNestedPath pins the put's nested rule: a path inside an
// NNN-<actor>/ directory is accepted under its nested name, and a mismatched
// NNN or a path with ".." is refused with nothing written.
func TestPutRoundFileNestedPath(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/webshop")
	b.Round = 5
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body := []byte("<html>\n")
	path := filepath.Join(s.ArtifactDir("webshop", 4, "reviewer"), "site", "index.html")
	if err := s.WithLock(func(tx *Tx) error {
		return tx.PutRoundFile("webshop", 4, path, body)
	}); err != nil {
		t.Fatalf("PutRoundFile nested: %v", err)
	}

	got, err := s.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("ReadFile = %q, want %q", got, body)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("os.Stat(%s) = %v, want ErrNotExist (no file on disk)", path, err)
	}

	refused := []struct {
		name  string
		round int
		path  string
	}{
		{"a mismatched NNN", 4, filepath.Join(s.ArtifactDir("webshop", 3, "reviewer"), "summary.md")},
		{"a path with ..", 4, s.Dir("webshop") + "/004-reviewer/../004-secret.md"},
		{"a first segment that is not NNN-", 4, filepath.Join(s.Dir("webshop"), "notes", "x.md")},
	}
	for _, tc := range refused {
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
	if !slices.Equal(names, []string{"004-reviewer/site/index.html"}) {
		t.Fatalf("RoundFiles = %v, want only the accepted nested row", names)
	}
}

// TestForkCopiesNestedFiles pins that a fork copies the nested files of the
// rounds at or below the cut, under their same nested names, and writes no file
// in dst's directory.
func TestForkCopiesNestedFiles(t *testing.T) {
	s := New(t.TempDir())
	srcName, dstName := "webshop", "webshop-fork"
	b := newBinding(srcName, "/repo")
	b.Round = 4
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	files := map[string]string{
		s.PromptPath(srcName, 1): "round 1 plan",
		s.PromptPath(srcName, 2): "round 2 plan",
		s.PromptPath(srcName, 3): "round 3 plan",
		filepath.Join(s.ArtifactDir(srcName, 2, "reviewer"), "summary.md"):         "round 2 summary",
		filepath.Join(s.ArtifactDir(srcName, 2, "reviewer"), "site", "index.html"): "round 2 html",
		filepath.Join(s.ArtifactDir(srcName, 3, "reviewer"), "summary.md"):         "round 3 summary",
	}
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), bindingDirMode); err != nil {
			t.Fatalf("MkdirAll %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(body), bindingFileMode); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	dst := newBinding(dstName, "/repo-fork")
	if err := s.WithLock(func(tx *Tx) error {
		return tx.ForkState(srcName, dst, 2)
	}); err != nil {
		t.Fatalf("ForkState: %v", err)
	}

	checkDirEmpty(t, s.Dir(dstName))

	names, err := s.RoundFiles(dstName)
	if err != nil {
		t.Fatalf("RoundFiles dst: %v", err)
	}
	want := []string{
		"001-prompt.md",
		"002-prompt.md",
		"002-reviewer/site/index.html",
		"002-reviewer/summary.md",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("RoundFiles(dst) = %v, want %v", names, want)
	}
	for _, name := range want {
		got, err := s.ReadFile(filepath.Join(s.Dir(dstName), name))
		if err != nil {
			t.Fatalf("ReadFile dst %s: %v", name, err)
		}
		srcKey := filepath.Join(s.Dir(srcName), name)
		if _, ok := files[srcKey]; !ok {
			srcKey = filepath.Join(s.OutDir(srcName), filepath.FromSlash(name))
		}
		if string(got) != files[srcKey] {
			t.Errorf("dst %s = %q, want %q", name, got, files[srcKey])
		}
	}
}
