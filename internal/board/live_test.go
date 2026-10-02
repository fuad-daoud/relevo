package board

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// testLiveRoot makes a live root and one valid MasterMind id directory under it.
func testLiveRoot(t *testing.T) (string, string) {
	t.Helper()
	root := realDir(t, t.TempDir())
	liveRoot := filepath.Join(root, "boards")
	id := "mm_aaaaaaaaaaaa"
	if err := os.MkdirAll(filepath.Join(liveRoot, id), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return liveRoot, id
}

func TestValidSceneName(t *testing.T) {
	for _, ok := range []string{"board", "a", "abc-123", "0"} {
		if err := ValidSceneName(ok); err != nil {
			t.Errorf("ValidSceneName(%q) = %v, want nil", ok, err)
		}
	}
	long := strings.Repeat("a", 65)
	for _, bad := range []string{"", "-x", "Board", "a_b", "a/b", long} {
		if err := ValidSceneName(bad); !errors.Is(err, ErrUsage) {
			t.Errorf("ValidSceneName(%q) = %v, want ErrUsage", bad, err)
		}
	}
}

func TestSelectScenePrecedence(t *testing.T) {
	liveRoot, _ := testLiveRoot(t)
	liveDir := filepath.Join(liveRoot, "mm_aaaaaaaaaaaa")

	// No flag, no pointer: the default, not from the pointer.
	if name, fromPtr, err := SelectScene(liveDir, ""); err != nil || name != DefaultBoard || fromPtr {
		t.Errorf("SelectScene(no pointer) = %q, %v, %v; want %q, false, nil", name, fromPtr, err, DefaultBoard)
	}

	if err := WritePointer(liveDir, "alpha"); err != nil {
		t.Fatalf("WritePointer: %v", err)
	}
	// The pointer now wins, and reports itself as from the pointer.
	if name, fromPtr, err := SelectScene(liveDir, ""); err != nil || name != "alpha" || !fromPtr {
		t.Errorf("SelectScene(pointer) = %q, %v, %v; want alpha, true, nil", name, fromPtr, err)
	}
	// --board wins over the pointer, not from the pointer.
	if name, fromPtr, err := SelectScene(liveDir, "beta"); err != nil || name != "beta" || fromPtr {
		t.Errorf("SelectScene(--board) = %q, %v, %v; want beta, false, nil", name, fromPtr, err)
	}
}

func TestWritePointerOnlyWhenDifferent(t *testing.T) {
	liveRoot, _ := testLiveRoot(t)
	liveDir := filepath.Join(liveRoot, "mm_aaaaaaaaaaaa")

	if err := WritePointer(liveDir, "alpha"); err != nil {
		t.Fatalf("WritePointer: %v", err)
	}
	path := filepath.Join(liveDir, pointerName)
	past := time.Unix(1000000000, 0).UTC()
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	if err := WritePointer(liveDir, "alpha"); err != nil {
		t.Fatalf("WritePointer (same): %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.ModTime().Equal(past) {
		t.Errorf("WritePointer rewrote an unchanged pointer: mtime = %v, want %v", info.ModTime(), past)
	}

	if err := WritePointer(liveDir, "beta"); err != nil {
		t.Fatalf("WritePointer (different): %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "beta\n" {
		t.Errorf("pointer = %q, want %q", data, "beta\n")
	}
}

func TestWritePointerCreatesLiveDir0700(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not reliable on windows")
	}
	liveDir := filepath.Join(t.TempDir(), "boards", "mm_aaaaaaaaaaaa")
	if err := WritePointer(liveDir, "board"); err != nil {
		t.Fatalf("WritePointer: %v", err)
	}
	info, err := os.Stat(liveDir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("live dir mode = %o, want 0700", got)
	}
}

func TestPointerCorruptRefused(t *testing.T) {
	liveRoot, id := testLiveRoot(t)
	liveDir := filepath.Join(liveRoot, id)
	if err := os.WriteFile(filepath.Join(liveDir, pointerName), []byte("Not A Slug\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, _, err := Pointer(liveDir)
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("Pointer(corrupt) = %v, want ErrUsage", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(liveDir, pointerName)) {
		t.Errorf("refusal %q does not name the pointer file", err)
	}
}

func TestResolveLiveArgAcceptsLiveShape(t *testing.T) {
	liveRoot, id := testLiveRoot(t)
	path := filepath.Join(liveRoot, id, "board.excalidraw")
	res, ok, err := ResolveLiveArg(liveRoot, liveRoot, path)
	if err != nil || !ok {
		t.Fatalf("ResolveLiveArg = %+v, %v, %v; want a live resolution", res, ok, err)
	}
	if res.Scope != ScopeLive || res.Scene != "board" || res.Path != path || res.LiveDir != filepath.Join(liveRoot, id) {
		t.Errorf("ResolveLiveArg = %+v, want live board at %s", res, path)
	}
}

func TestResolveLiveArgNotUnderLiveRoot(t *testing.T) {
	liveRoot, _ := testLiveRoot(t)
	cwd := realDir(t, t.TempDir())
	if _, ok, err := ResolveLiveArg(liveRoot, cwd, "docs/boards/api.excalidraw"); ok || err != nil {
		t.Errorf("ResolveLiveArg(repo path) = ok %v, err %v; want false, nil", ok, err)
	}
}

func TestResolveLiveArgRefusals(t *testing.T) {
	liveRoot, id := testLiveRoot(t)
	cases := []struct{ name, arg string }{
		{"dotdot", filepath.Join(liveRoot, id, "..", "board.excalidraw")},
		{"wrong extension", filepath.Join(liveRoot, id, "board.svg")},
		{"nested dirs", filepath.Join(liveRoot, id, "sub", "board.excalidraw")},
		{"bad id segment", filepath.Join(liveRoot, "not-an-id", "board.excalidraw")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ResolveLiveArg(liveRoot, liveRoot, tc.arg)
			if !errors.Is(err, ErrUsage) {
				t.Fatalf("ResolveLiveArg(%q) = %v, want ErrUsage", tc.arg, err)
			}
		})
	}
}

func TestResolveLiveArgRefusesEscapingSymlinkParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks are not reliable on windows")
	}
	liveRoot, id := testLiveRoot(t)
	repo := realDir(t, t.TempDir())
	// The live directory is a symlink into the repository, so a live-shaped path
	// resolves under the repo root: the live scope must refuse it (S1's rule).
	liveDir := filepath.Join(liveRoot, id)
	if err := os.RemoveAll(liveDir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if err := os.Symlink(repo, liveDir); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	_, _, err := ResolveLiveArg(liveRoot, liveRoot, filepath.Join(liveDir, "board.excalidraw"))
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("ResolveLiveArg(symlinked parent into repo) = %v, want ErrUsage", err)
	}
}

func TestResolveLiveArgRefusesRepoPathThroughSymlinkIntoLiveDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks are not reliable on windows")
	}
	liveRoot, id := testLiveRoot(t)
	repoRoot := realDir(t, t.TempDir())
	// A repo path whose parent symlinks into the live directory must be refused
	// by the repo scope's own EvalSymlinks rule (S1).
	if err := os.Symlink(filepath.Join(liveRoot, id), filepath.Join(repoRoot, "link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	_, err := Resolve(repoRoot, repoRoot, "link/board.excalidraw")
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("Resolve(repo path through symlink into live dir) = %v, want ErrUsage", err)
	}
}

func TestResolveLiveDirRefusesNestedScopes(t *testing.T) {
	repoRoot := realDir(t, t.TempDir())
	liveDir := filepath.Join(repoRoot, "boards", "mm_aaaaaaaaaaaa")
	if _, err := ResolveLiveDir(repoRoot, liveDir, "board"); !errors.Is(err, ErrUsage) {
		t.Fatalf("ResolveLiveDir(live under repo) = %v, want ErrUsage", err)
	}
	// And the reverse: a repo root that sits under the live directory.
	if _, err := ResolveLiveDir(filepath.Join(liveDir, "repo"), liveDir, "board"); !errors.Is(err, ErrUsage) {
		t.Fatalf("ResolveLiveDir(repo under live) = %v, want ErrUsage", err)
	}
}

func TestResolveLiveDirJoins(t *testing.T) {
	repoRoot := realDir(t, t.TempDir())
	liveDir := filepath.Join(t.TempDir(), "boards", "mm_aaaaaaaaaaaa")
	res, err := ResolveLiveDir(repoRoot, liveDir, "notes")
	if err != nil {
		t.Fatalf("ResolveLiveDir: %v", err)
	}
	if res.Scope != ScopeLive || res.Scene != "notes" || res.Path != filepath.Join(liveDir, "notes.excalidraw") || res.LiveDir != liveDir {
		t.Errorf("ResolveLiveDir = %+v", res)
	}
}

func TestServerInfoRoundTrip(t *testing.T) {
	liveRoot, id := testLiveRoot(t)
	liveDir := filepath.Join(liveRoot, id)
	want := ServerInfo{Scene: "board", URL: "http://127.0.0.1:9/#t=abc", Port: 9, PID: 1234, StartedAt: 1700000000}
	if err := WriteServerInfo(liveDir, want); err != nil {
		t.Fatalf("WriteServerInfo: %v", err)
	}
	got, ok, err := ReadServerInfo(liveDir)
	if err != nil || !ok {
		t.Fatalf("ReadServerInfo = %+v, %v, %v", got, ok, err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestServerInfoMostRecentWins(t *testing.T) {
	liveRoot, id := testLiveRoot(t)
	liveDir := filepath.Join(liveRoot, id)
	a := ServerInfo{Scene: "board", URL: "http://127.0.0.1:1/#t=a", Port: 1, PID: 1, StartedAt: 1}
	b := ServerInfo{Scene: "board", URL: "http://127.0.0.1:2/#t=b", Port: 2, PID: 2, StartedAt: 2}
	if err := WriteServerInfo(liveDir, a); err != nil {
		t.Fatalf("WriteServerInfo A: %v", err)
	}
	if err := WriteServerInfo(liveDir, b); err != nil {
		t.Fatalf("WriteServerInfo B: %v", err)
	}
	if got, _, _ := ReadServerInfo(liveDir); got != b {
		t.Errorf("most recent writer = %+v, want B", got)
	}
}

func TestRemoveServerInfoGuard(t *testing.T) {
	liveRoot, id := testLiveRoot(t)
	liveDir := filepath.Join(liveRoot, id)
	a := ServerInfo{Scene: "board", URL: "http://127.0.0.1:1/#t=a", Port: 1, PID: 1, StartedAt: 1}
	b := ServerInfo{Scene: "board", URL: "http://127.0.0.1:2/#t=b", Port: 2, PID: 2, StartedAt: 2}
	if err := WriteServerInfo(liveDir, a); err != nil {
		t.Fatalf("WriteServerInfo A: %v", err)
	}
	if err := WriteServerInfo(liveDir, b); err != nil {
		t.Fatalf("WriteServerInfo B: %v", err)
	}

	// A's shutdown must leave B's advertisement alone.
	if err := RemoveServerInfo(liveDir, a); err != nil {
		t.Fatalf("RemoveServerInfo A: %v", err)
	}
	if _, ok, _ := ReadServerInfo(liveDir); !ok {
		t.Fatal("RemoveServerInfo A removed B's advertisement")
	}

	// B's own shutdown removes it.
	if err := RemoveServerInfo(liveDir, b); err != nil {
		t.Fatalf("RemoveServerInfo B: %v", err)
	}
	if _, ok, _ := ReadServerInfo(liveDir); ok {
		t.Error("RemoveServerInfo B left the advertisement")
	}
}

func TestReadServerInfoAbsent(t *testing.T) {
	liveRoot, id := testLiveRoot(t)
	liveDir := filepath.Join(liveRoot, id)

	if _, ok, err := ReadServerInfo(liveDir); ok || err != nil {
		t.Errorf("missing server.json = ok %v, err %v; want false, nil", ok, err)
	}
	if err := os.WriteFile(filepath.Join(liveDir, serverFileName), []byte("not json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, ok, err := ReadServerInfo(liveDir); ok || err != nil {
		t.Errorf("corrupt server.json = ok %v, err %v; want false, nil", ok, err)
	}
}

func TestLiveURL(t *testing.T) {
	liveRoot, id := testLiveRoot(t)
	liveDir := filepath.Join(liveRoot, id)
	info := ServerInfo{Scene: "board", URL: "http://127.0.0.1:9/#t=abc", Port: 9, PID: 1234, StartedAt: 1700000000}
	if err := WriteServerInfo(liveDir, info); err != nil {
		t.Fatalf("WriteServerInfo: %v", err)
	}

	alive := func(pid int) (int64, error) {
		if pid != info.PID {
			return 0, errors.New("no such process")
		}
		return info.StartedAt, nil
	}
	if url, ok, err := LiveURL(liveDir, "board", alive); err != nil || !ok || url != info.URL {
		t.Errorf("LiveURL(alive) = %q, %v, %v; want the URL", url, ok, err)
	}

	// A dead or unmeasurable pid.
	dead := func(int) (int64, error) { return 0, errors.New("gone") }
	if _, ok, _ := LiveURL(liveDir, "board", dead); ok {
		t.Error("LiveURL(dead pid) is live, want not live")
	}
	// A reused pid: the measured start differs.
	reused := func(int) (int64, error) { return info.StartedAt + 1, nil }
	if _, ok, _ := LiveURL(liveDir, "board", reused); ok {
		t.Error("LiveURL(reused pid) is live, want not live")
	}
	// A scene mismatch.
	if _, ok, _ := LiveURL(liveDir, "other", alive); ok {
		t.Error("LiveURL(scene mismatch) is live, want not live")
	}
	// started_at == 0 (a startup measurement failure) reads as not live.
	if err := WriteServerInfo(liveDir, ServerInfo{Scene: "board", URL: info.URL, PID: 1234, StartedAt: 0}); err != nil {
		t.Fatalf("WriteServerInfo: %v", err)
	}
	if _, ok, _ := LiveURL(liveDir, "board", alive); ok {
		t.Error("LiveURL(started_at 0) is live, want not live")
	}
}

// TestLiveScopePathsThroughASymlinkedAncestor pins the portability rule: the
// live-scope comparisons resolve symlinks on the deepest existing ancestor of
// both sides, so two spellings of one directory (macOS /var vs /private/var)
// compare equal even when the deeper path does not exist yet. Without that, an
// explicit live path reads "not under the live root" and a nested repo root is
// not refused -- both on macOS only.
func TestLiveScopePathsThroughASymlinkedAncestor(t *testing.T) {
	t.Parallel()
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	liveRoot := filepath.Join(link, "boards")
	dir := filepath.Join(liveRoot, "mm_aaaaaaaaaaaa")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	arg := filepath.Join(dir, "board.excalidraw")

	res, ok, err := ResolveLiveArg(liveRoot, liveRoot, arg)
	if err != nil || !ok || res.Scene != "board" {
		t.Errorf("ResolveLiveArg through a symlinked ancestor = (%+v, ok %v, err %v), want the live scene", res, ok, err)
	}

	// Two roots that nest are refused even when one of them does not exist yet.
	if err := DisjointScopes(filepath.Join(dir, "repo"), dir); err == nil {
		t.Error("DisjointScopes(repo root under the live directory) = nil, want a refusal")
	}
	if err := DisjointScopes(real, liveRoot); err == nil {
		t.Error("DisjointScopes(live root under the repo root) = nil, want a refusal")
	}
}
