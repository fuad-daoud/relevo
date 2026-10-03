package board

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testMMID = "mm_aaaaaaaaaaaa"

// liveHTMLRoot builds a live root with one MasterMind's directory inside it and
// returns the canonical root plus that MasterMind's live directory.
func liveHTMLRoot(t *testing.T) (root, liveDir string) {
	t.Helper()
	root = realDir(t, t.TempDir())
	liveDir = filepath.Join(root, testMMID)
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatalf("mkdir live dir: %v", err)
	}
	return root, liveDir
}

func TestResolveHTMLAcceptsSlugBoardShape(t *testing.T) {
	root := realDir(t, t.TempDir())
	got, err := ResolveHTML(root, root, "docs/boards/api/board.html")
	if err != nil {
		t.Fatalf("ResolveHTML: %v", err)
	}
	want := filepath.Join(root, "docs", "boards", "api", "board.html")
	if got.Path != want {
		t.Errorf("Path = %q, want %q", got.Path, want)
	}
	if got.Scope != ScopeRepo || got.Scene != "api" || got.LiveDir != "" {
		t.Errorf("Resolved = %+v, want repo scope, scene api, no live dir", got)
	}
}

func TestResolveHTMLRefusesEscape(t *testing.T) {
	root := realDir(t, t.TempDir())
	if _, err := ResolveHTML(root, root, "../outside/board.html"); !errors.Is(err, ErrUsage) {
		t.Fatalf("ResolveHTML escape = %v, want ErrUsage", err)
	}
}

func TestResolveHTMLRefusesSymlinkedParentOutside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks are not reliable on windows")
	}
	root := realDir(t, t.TempDir())
	outside := realDir(t, t.TempDir())
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := ResolveHTML(root, root, "link/api/board.html"); !errors.Is(err, ErrUsage) {
		t.Fatalf("ResolveHTML through an escaping symlink = %v, want ErrUsage", err)
	}
}

func TestResolveHTMLRefusesWrongFileName(t *testing.T) {
	root := realDir(t, t.TempDir())
	for _, arg := range []string{"docs/boards/api/notes.html", "docs/boards/api/board.excalidraw", "docs/boards/api"} {
		if _, err := ResolveHTML(root, root, arg); !errors.Is(err, ErrUsage) {
			t.Errorf("ResolveHTML(%q) = %v, want ErrUsage", arg, err)
		}
	}
}

func TestResolveHTMLRefusesBadSlug(t *testing.T) {
	root := realDir(t, t.TempDir())
	if _, err := ResolveHTML(root, root, "docs/boards/API/board.html"); !errors.Is(err, ErrUsage) {
		t.Fatalf("ResolveHTML bad slug = %v, want ErrUsage", err)
	}
	if _, err := ResolveHTML(root, root, ""); !errors.Is(err, ErrUsage) {
		t.Fatalf("ResolveHTML empty = %v, want ErrUsage", err)
	}
}

func TestResolveHTMLAcceptsMissingFile(t *testing.T) {
	root := realDir(t, t.TempDir())
	got, err := ResolveHTML(root, root, "docs/boards/new/board.html")
	if err != nil {
		t.Fatalf("ResolveHTML missing: %v", err)
	}
	if _, statErr := os.Stat(got.Path); !os.IsNotExist(statErr) {
		t.Errorf("ResolveHTML created %s: stat error = %v, want not-exist", got.Path, statErr)
	}
}

func TestResolveLiveHTMLArgShape(t *testing.T) {
	root, _ := liveHTMLRoot(t)
	got, ok, err := ResolveLiveHTMLArg(root, root, filepath.Join(testMMID, "api", "board.html"))
	if err != nil || !ok {
		t.Fatalf("ResolveLiveHTMLArg = %+v, %v, %v; want ok", got, ok, err)
	}
	want := filepath.Join(root, testMMID, "api", "board.html")
	if got.Path != want {
		t.Errorf("Path = %q, want %q", got.Path, want)
	}
	if got.Scope != ScopeLive || got.Scene != "api" {
		t.Errorf("Resolved = %+v, want live scope, scene api", got)
	}
	if got.LiveDir != filepath.Join(root, testMMID) {
		t.Errorf("LiveDir = %q, want %q", got.LiveDir, filepath.Join(root, testMMID))
	}
}

func TestResolveLiveHTMLArgOutsideRootIsNotLive(t *testing.T) {
	root, _ := liveHTMLRoot(t)
	elsewhere := realDir(t, t.TempDir())
	for _, arg := range []string{
		filepath.Join(elsewhere, testMMID, "api", "board.html"),
		filepath.Join(root, "..", "escape", testMMID, "api", "board.html"),
	} {
		if _, ok, err := ResolveLiveHTMLArg(root, root, arg); ok || err != nil {
			t.Errorf("ResolveLiveHTMLArg(%q) = ok %v, %v; want not-live", arg, ok, err)
		}
	}
}

// TestResolveLiveHTMLArgRefusesMalformedShape covers every wrong live shape: a
// wrong file name, a bare slug directory, one directory too deep, a bad slug
// and a malformed MasterMind id.
func TestResolveLiveHTMLArgRefusesMalformedShape(t *testing.T) {
	root, _ := liveHTMLRoot(t)
	cases := map[string]string{
		"wrong file":  filepath.Join(testMMID, "api", "notes.html"),
		"bare slug":   filepath.Join(testMMID, "api"),
		"too deep":    filepath.Join(testMMID, "api", "extra", "board.html"),
		"bad slug":    filepath.Join(testMMID, "API", "board.html"),
		"bad id":      filepath.Join("nope", "api", "board.html"),
		"live root":   "board.html",
		"empty parts": filepath.Join(testMMID, "board.html"),
	}
	for name, arg := range cases {
		if _, ok, err := ResolveLiveHTMLArg(root, root, arg); ok || !errors.Is(err, ErrUsage) {
			t.Errorf("%s: ResolveLiveHTMLArg(%q) = ok %v, %v; want ErrUsage", name, arg, ok, err)
		}
	}
}

// TestResolveLiveHTMLArgRefusesSymlinkedEscape: the symlink guard is the
// resolving check. A live-shaped path by name whose parent escapes must be
// refused, not treated as a repo path.
func TestResolveLiveHTMLArgRefusesSymlinkedEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks are not reliable on windows")
	}
	root, _ := liveHTMLRoot(t)
	outside := realDir(t, t.TempDir())
	if err := os.Symlink(outside, filepath.Join(root, testMMID, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	arg := filepath.Join(testMMID, "link", "board.html")
	_, ok, err := ResolveLiveHTMLArg(root, root, arg)
	if ok || !errors.Is(err, ErrUsage) {
		t.Fatalf("ResolveLiveHTMLArg(%q) = ok %v, %v; want ErrUsage", arg, ok, err)
	}
	if !strings.Contains(err.Error(), "outside the live directory") {
		t.Errorf("refusal %q does not name the live directory", err)
	}
}

func TestResolveLiveHTMLDir(t *testing.T) {
	repo := realDir(t, t.TempDir())
	_, liveDir := liveHTMLRoot(t)
	got, err := ResolveLiveHTMLDir(repo, liveDir, "api")
	if err != nil {
		t.Fatalf("ResolveLiveHTMLDir: %v", err)
	}
	want := filepath.Join(liveDir, "api", "board.html")
	if got.Path != want {
		t.Errorf("Path = %q, want %q", got.Path, want)
	}
	if got.Scope != ScopeLive || got.Scene != "api" {
		t.Errorf("Resolved = %+v, want live scope, scene api", got)
	}
}

func TestResolveLiveHTMLDirRefusesBadSlug(t *testing.T) {
	repo := realDir(t, t.TempDir())
	_, liveDir := liveHTMLRoot(t)
	if _, err := ResolveLiveHTMLDir(repo, liveDir, "API"); !errors.Is(err, ErrUsage) {
		t.Fatalf("ResolveLiveHTMLDir bad slug = %v, want ErrUsage", err)
	}
}

func TestResolveLiveHTMLDirRefusesNestedScopes(t *testing.T) {
	repo := realDir(t, t.TempDir())
	if _, err := ResolveLiveHTMLDir(repo, filepath.Join(repo, "state", "boards"), "api"); !errors.Is(err, ErrUsage) {
		t.Fatalf("ResolveLiveHTMLDir nested scopes = %v, want ErrUsage", err)
	}
}

func TestLoadHTMLMissingIsNew(t *testing.T) {
	dir := t.TempDir()
	data, etag, isNew, err := LoadHTML(filepath.Join(dir, "board.html"))
	if err != nil || !isNew || etag != "" || data != nil {
		t.Fatalf("LoadHTML missing = %q, %q, %v, %v; want new board", data, etag, isNew, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Errorf("LoadHTML created %d entries, err %v; want none", len(entries), err)
	}
}

func TestLoadHTMLReturnsBytesAndEtag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.html")
	body := []byte("<h1>hi</h1>")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, etag, isNew, err := LoadHTML(path)
	if err != nil || isNew {
		t.Fatalf("LoadHTML = %v, %v; want no error, not new", isNew, err)
	}
	if string(data) != string(body) || etag != Etag(body) {
		t.Errorf("LoadHTML = %q/%q, want %q/%q", data, etag, body, Etag(body))
	}
}

func TestPromoteCopiesBoardFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "board.html")
	body := []byte("<p>board</p>")
	if err := os.WriteFile(src, body, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "docs", "boards", "api", "board.html")
	if err := Promote(src, dst, false); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("promoted %q, want %q", got, body)
	}
	entries, err := os.ReadDir(filepath.Dir(dst))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("promote left %d entries beside the board, want only board.html", len(entries)-1)
	}
}

func TestPromoteRefusesMissingSource(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "board.html")
	err := Promote(filepath.Join(t.TempDir(), "gone.html"), dst, false)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Promote missing source = %v, want ErrNotFound", err)
	}
}

func TestPromoteRefusesExistingTargetWithoutForce(t *testing.T) {
	src := filepath.Join(t.TempDir(), "board.html")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "board.html")
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatalf("write dst: %v", err)
	}

	err := Promote(src, dst, false)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Promote over an existing target = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), dst) || !strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal %q names neither the target nor --force", err)
	}
	got, readErr := os.ReadFile(dst)
	if readErr != nil || string(got) != "old" {
		t.Errorf("refused promote left the target = %q, %v; want the old bytes", got, readErr)
	}
}

func TestPromoteOverwritesWithForce(t *testing.T) {
	src := filepath.Join(t.TempDir(), "board.html")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "board.html")
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatalf("write dst: %v", err)
	}
	if err := Promote(src, dst, true); err != nil {
		t.Fatalf("Promote --force: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "new" {
		t.Errorf("forced promote left the target = %q, %v; want the new bytes", got, err)
	}
}