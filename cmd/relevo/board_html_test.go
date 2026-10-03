package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fuad-daoud/relevo/internal/board"
)

// boardHTMLEnv isolates the state root and returns the live root plus the
// MasterMind's live directory, seeded with one record so boardMasterMind can
// resolve a name.
func boardHTMLEnv(t *testing.T) (liveRoot, liveDir string) {
	t.Helper()
	liveRoot = boardStateRoot(t)
	stateHome := os.Getenv("XDG_STATE_HOME")
	seedBoardRecords(t, stateHome, boardRecord("mm_aaaaaaaaaaaa", "alpha"))
	liveDir = filepath.Join(liveRoot, "mm_aaaaaaaaaaaa")
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return liveRoot, liveDir
}

// TestResolveBoardHTMLBareNamesALiveBoard: a bare `relevo board` is the calling
// MasterMind's live board, and it is a directory holding a board.html rather
// than a sibling .excalidraw file.
func TestResolveBoardHTMLBareNamesALiveBoard(t *testing.T) {
	liveRoot, liveDir := boardHTMLEnv(t)
	stubRepoSeamAbsent(t)

	res, err := resolveBoardHTML(t.TempDir(), "alpha", "", "")
	if err != nil {
		t.Fatalf("resolveBoardHTML bare: %v", err)
	}
	if res.Scope != board.ScopeLive {
		t.Errorf("scope = %q, want live", res.Scope)
	}
	if res.Scene != board.DefaultBoard {
		t.Errorf("scene = %q, want %q", res.Scene, board.DefaultBoard)
	}
	want := filepath.Join(liveDir, board.DefaultBoard, "board.html")
	if res.Path != want {
		t.Errorf("path = %q, want %q", res.Path, want)
	}
	if res.Owner != "alpha" {
		t.Errorf("owner = %q, want alpha", res.Owner)
	}
	_ = liveRoot
}

// TestResolveBoardHTMLBoardFlagNamesALiveBoard: --board selects the directory,
// and FromPointer is false so the caller knows to write the pointer.
func TestResolveBoardHTMLBoardFlagNamesALiveBoard(t *testing.T) {
	_, liveDir := boardHTMLEnv(t)
	stubRepoSeamAbsent(t)

	res, err := resolveBoardHTML(t.TempDir(), "alpha", "api", "")
	if err != nil {
		t.Fatalf("resolveBoardHTML --board: %v", err)
	}
	if res.Scene != "api" || res.FromPointer {
		t.Errorf("Resolved = %+v, want scene api not from the pointer", res)
	}
	if want := filepath.Join(liveDir, "api", "board.html"); res.Path != want {
		t.Errorf("path = %q, want %q", res.Path, want)
	}
}

// TestResolveBoardHTMLPointerNamesALiveBoard: a pointer file names the board and
// is read, not rewritten.
func TestResolveBoardHTMLPointerNamesALiveBoard(t *testing.T) {
	_, liveDir := boardHTMLEnv(t)
	stubRepoSeamAbsent(t)
	if err := board.WritePointer(liveDir, "from-pointer"); err != nil {
		t.Fatalf("WritePointer: %v", err)
	}

	res, err := resolveBoardHTML(t.TempDir(), "alpha", "", "")
	if err != nil {
		t.Fatalf("resolveBoardHTML pointer: %v", err)
	}
	if res.Scene != "from-pointer" || !res.FromPointer {
		t.Errorf("Resolved = %+v, want from-pointer read from the pointer", res)
	}
}

// TestResolveBoardHTMLExplicitLivePath: an explicit live board.html path
// resolves to the live scope and names its owner.
func TestResolveBoardHTMLExplicitLivePath(t *testing.T) {
	_, liveDir := boardHTMLEnv(t)
	stubRepoSeamAbsent(t)

	arg := filepath.Join(liveDir, "api", "board.html")
	res, err := resolveBoardHTML(t.TempDir(), "alpha", "", arg)
	if err != nil {
		t.Fatalf("resolveBoardHTML live path: %v", err)
	}
	if res.Scope != board.ScopeLive || res.Scene != "api" {
		t.Errorf("Resolved = %+v, want live scope, scene api", res)
	}
	if res.Owner != "alpha" {
		t.Errorf("owner = %q, want alpha", res.Owner)
	}
}

// TestResolveBoardHTMLExplicitRepoPath: an explicit path under the git top level
// is the repo scope, and its slug is the board name.
func TestResolveBoardHTMLExplicitRepoPath(t *testing.T) {
	repo := withTempRepoRoot(t)
	boardHTMLEnv(t)
	arg := filepath.Join(repo, "docs", "boards", "api", "board.html")

	res, err := resolveBoardHTML(t.TempDir(), "", "", arg)
	if err != nil {
		t.Fatalf("resolveBoardHTML repo path: %v", err)
	}
	if res.Scope != board.ScopeRepo {
		t.Errorf("scope = %q, want repo", res.Scope)
	}
	if res.Scene != "api" || res.LiveDir != "" {
		t.Errorf("Resolved = %+v, want scene api with no live dir", res)
	}
	if res.Path != arg {
		t.Errorf("path = %q, want %q", res.Path, arg)
	}
}

// TestResolveBoardHTMLPathAndBoardAreExclusive: a path and --board name
// different things, so naming both is usage.
func TestResolveBoardHTMLPathAndBoardAreExclusive(t *testing.T) {
	withTempRepoRoot(t)
	boardHTMLEnv(t)
	arg := filepath.Join(t.TempDir(), "docs", "boards", "api", "board.html")
	requireCLIError(t, mustResolveBoardHTMLErr(t, arg, "api"), codeUsage, "relevo help")
}

// mustResolveBoardHTMLErr resolves with both a path and --board and returns the
// error it produced.
func mustResolveBoardHTMLErr(t *testing.T, arg, name string) error {
	t.Helper()
	_, err := resolveBoardHTML(t.TempDir(), "", name, arg)
	if err == nil {
		t.Fatal("resolveBoardHTML with a path and --board = nil, want usage")
	}
	return err
}

// TestResolveBoardHTMLRefusesBadShapes: a bare slug directory, a wrong file name
// and a bad slug are all usage, so a path can never reach a subverb-shaped
// target.
func TestResolveBoardHTMLRefusesBadShapes(t *testing.T) {
	repo := withTempRepoRoot(t)
	boardHTMLEnv(t)
	cases := map[string]string{
		"bare slug":    filepath.Join(repo, "docs", "boards", "api"),
		"wrong file":   filepath.Join(repo, "docs", "boards", "api", "notes.html"),
		"excalidraw":   filepath.Join(repo, "docs", "boards", "api", "board.excalidraw"),
		"bad slug":     filepath.Join(repo, "docs", "boards", "API", "board.html"),
		"outside":      filepath.Join(repo, "..", "escape", "board.html"),
		"nested scope": filepath.Join(repo, "docs", "boards", "a b", "board.html"),
	}
	for name, arg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := resolveBoardHTML(t.TempDir(), "", "", arg); err == nil {
				t.Errorf("resolveBoardHTML(%q) = nil, want a refusal", arg)
			}
		})
	}
}

// TestResolveBoardHTMLExcalidrawStillResolvesThroughTheOldPath: the legacy flow
// is untouched by the HTML one. The two resolvers take the same arguments and
// reach different targets for the same .excalidraw path.
func TestResolveBoardHTMLExcalidrawStillResolvesThroughTheOldPath(t *testing.T) {
	_, liveDir := boardHTMLEnv(t)
	stubRepoSeamAbsent(t)
	scene := filepath.Join(liveDir, "api.excalidraw")

	legacy, err := resolveBoard(t.TempDir(), "", "", scene)
	if err != nil {
		t.Fatalf("resolveBoard: %v", err)
	}
	// Temp roots can carry symlinks (/var -> /private/var on macos), and the
	// resolver may return either form. The scene file itself need not exist,
	// so evaluate the directory both paths share and compare from there: the
	// test pins the target, not the spelling.
	wantDir, werr := filepath.EvalSymlinks(filepath.Dir(scene))
	if werr != nil {
		t.Fatalf("EvalSymlinks scene dir: %v", werr)
	}
	gotDir, gerr := filepath.EvalSymlinks(filepath.Dir(legacy.Path))
	if gerr != nil {
		t.Fatalf("EvalSymlinks legacy dir: %v", gerr)
	}
	if gotDir != wantDir || filepath.Base(legacy.Path) != filepath.Base(scene) {
		t.Errorf("resolveBoard path = %q, want %q", legacy.Path, scene)
	}
	if _, err := resolveBoardHTML(t.TempDir(), "", "", scene); err == nil {
		t.Error("resolveBoardHTML on an .excalidraw path = nil, want a refusal")
	}
}

// TestCmdBoardDispatchesExcalidrawToTheLegacyFlow: cmdBoard routes on the path
// alone, so an explicit .excalidraw path reaches the legacy resolver and the
// Excalidraw server, and only a board.html path (or a bare board) reaches the
// single-file flow. The route is proved by the refusals each flow produces:
// the legacy one accepts a .excalidraw path the HTML one refuses, and the two
// subverb-shaped paths cannot both be accepted.
func TestCmdBoardDispatchesExcalidrawToTheLegacyFlow(t *testing.T) {
	_, liveDir := boardHTMLEnv(t)
	stubRepoSeamAbsent(t)
	cockpitConfig(t)

	excalidraw := filepath.Join(liveDir, "api.excalidraw")
	if _, err := resolveBoard(t.TempDir(), "alpha", "", excalidraw); err != nil {
		t.Errorf("the legacy resolver refused its own scene: %v", err)
	}
	if _, err := resolveBoardHTML(t.TempDir(), "alpha", "", excalidraw); err == nil {
		t.Error("the HTML resolver accepted an .excalidraw path, so the dispatch cannot be on the file name")
	}

	// The HTML flow's own target, which the legacy resolver refuses: the two
	// shapes are disjoint in both directions.
	html := filepath.Join(liveDir, "api", "board.html")
	if _, err := resolveBoardHTML(t.TempDir(), "alpha", "", html); err != nil {
		t.Errorf("resolveBoardHTML refused its own board: %v", err)
	}
	if _, err := resolveBoard(t.TempDir(), "alpha", "", html); err == nil {
		t.Error("the legacy resolver accepted a board.html path")
	}
}

// TestCmdBoardPromoteIsDispatched pins the subverb: `promote` reaches the verb,
// so a board directory named "promote" can never shadow it.
func TestCmdBoardPromoteIsDispatched(t *testing.T) {
	repo := withTempRepoRoot(t)
	boardHTMLEnv(t)

	// The verb gets past dispatch and flag parsing and reaches its own slug
	// check, which is what proves it was dispatched as the subverb rather than
	// treated as a scene path.
	err := cmdBoard([]string{"promote", "--to", "API"})
	if err == nil {
		t.Fatal("cmdBoard promote --to API = nil, want the slug refusal")
	}
	ce, ok := err.(*cliError)
	if !ok || ce.code != codeUsage || !strings.Contains(ce.message, "API") {
		t.Errorf("promote --to API = %v, want a usage refusal naming the value", err)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "docs")); !os.IsNotExist(statErr) {
		t.Errorf("the refused promote created %s: stat error = %v, want not-exist", repo, statErr)
	}
}

// TestResolveBoardHTMLNeverStartsTheDaemon extends R5 to the HTML resolver:
// resolving a name against a seeded database starts nothing and writes nothing.
func TestResolveBoardHTMLNeverStartsTheDaemon(t *testing.T) {
	stateHome := boardHTMLStateHome(t)
	seedBoardRecords(t, stateHome, boardRecord("mm_aaaaaaaaaaaa", "alpha"))

	var calls int32
	daemonStarter = func() error { atomic.AddInt32(&calls, 1); return nil }
	t.Cleanup(func() { daemonStarter = startDaemon })
	stubRepoSeamAbsent(t)

	res, err := resolveBoardHTML(t.TempDir(), "alpha", "", "")
	if err != nil {
		t.Fatalf("resolveBoardHTML(name): %v", err)
	}
	if res.Scope != board.ScopeLive || res.Scene != board.DefaultBoard {
		t.Errorf("resolveBoardHTML(name) = %+v, want the live default", res)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("resolution started the daemon %d times, want 0", got)
	}
	if _, err := os.Stat(filepath.Join(stateHome, "relevo", "relevo.sock")); !os.IsNotExist(err) {
		t.Errorf("resolution created a socket: stat error = %v, want not-exist", err)
	}
}

// boardHTMLStateHome isolates the whole XDG state and config home, so a test
// never reads the machine's own state root.
func boardHTMLStateHome(t *testing.T) string {
	t.Helper()
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(stateHome, "config"))
	t.Setenv("HOME", stateHome)
	return stateHome
}

// TestBoardHTMLPointerIsWrittenForLiveAndNotForRepo: the pointer always names the
// board the user is looking at, so a live board writes it and a repo board does
// not. The write goes through the resolver's own rule -- live and not read from
// the pointer -- which cmdBoard applies.
func TestBoardHTMLPointerIsWrittenForLiveAndNotForRepo(t *testing.T) {
	t.Run("live writes", func(t *testing.T) {
		_, liveDir := boardHTMLEnv(t)
		stubRepoSeamAbsent(t)
		res, err := resolveBoardHTML(t.TempDir(), "alpha", "api", "")
		if err != nil {
			t.Fatalf("resolveBoardHTML: %v", err)
		}
		if res.FromPointer {
			t.Fatal("the board came from the pointer, so no write is expected")
		}
		if err := board.WritePointer(res.LiveDir, res.Scene); err != nil {
			t.Fatalf("WritePointer: %v", err)
		}
		name, present, err := board.Pointer(liveDir)
		if err != nil || !present || name != "api" {
			t.Errorf("pointer = %q/%v/%v, want api/true/nil", name, present, err)
		}
	})

	t.Run("repo does not write", func(t *testing.T) {
		repo := withTempRepoRoot(t)
		boardHTMLEnv(t)
		arg := filepath.Join(repo, "docs", "boards", "api", "board.html")
		res, err := resolveBoardHTML(t.TempDir(), "", "", arg)
		if err != nil {
			t.Fatalf("resolveBoardHTML: %v", err)
		}
		if res.Scope == board.ScopeLive {
			t.Fatalf("scope = %q, want repo", res.Scope)
		}
		if liveDirOf(res) != "" {
			t.Errorf("liveDir = %q, want empty for the repo scope", liveDirOf(res))
		}
		if _, err := os.Stat(filepath.Join(repo, "docs", "boards", "api", "current")); !os.IsNotExist(err) {
			t.Errorf("a repo board wrote a pointer: stat error = %v, want not-exist", err)
		}
	})
}

// TestBoardServerInfoCarriesTheSlug is the statusline contract for a live HTML
// board: the advertisement names the slug, because a board.html path's base name
// would be "board" for every board in both scopes.
func TestBoardServerInfoCarriesTheSlug(t *testing.T) {
	got := boardServerInfo("api", "http://127.0.0.1:41234/alpha/#t=x", 7, 1700000000, "/live")
	if got == nil {
		t.Fatal("boardServerInfo = nil, want the live advertisement")
	}
	if got.Scene != "api" {
		t.Errorf("scene = %q, want the slug api", got.Scene)
	}
	if got.URL != "http://127.0.0.1:41234/alpha/#t=x" || got.Port != 41234 {
		t.Errorf("url/port = %q/%d, want the printed url and its port", got.URL, got.Port)
	}
	if got.PID != 7 || got.StartedAt != 1700000000 {
		t.Errorf("pid/started = %d/%d, want 7/1700000000", got.PID, got.StartedAt)
	}
}

// TestBoardBlockForAnHTMLLiveBoard: the statusline finds a live HTML board from
// the state root's files alone, on the same terms as any other live board.
func TestBoardBlockForAnHTMLLiveBoard(t *testing.T) {
	root := t.TempDir()
	liveDir := filepath.Join(root, "boards", "mm_aaaaaaaaaaaa")
	if err := board.WritePointer(liveDir, "api"); err != nil {
		t.Fatalf("WritePointer: %v", err)
	}
	info := board.ServerInfo{
		Scene: "api", URL: "http://127.0.0.1:41234/alpha/#t=x",
		Port: 41234, PID: 7, StartedAt: 1700000000,
	}
	if err := board.WriteServerInfo(liveDir, info); err != nil {
		t.Fatalf("WriteServerInfo: %v", err)
	}
	procStart := func(pid int) (int64, error) {
		if pid == 7 {
			return 1700000000, nil
		}
		return 0, os.ErrNotExist
	}

	got := boardBlockFor(root, "mm_aaaaaaaaaaaa", procStart)
	if got == nil {
		t.Fatal("boardBlockFor = nil, want the live HTML board")
	}
	if got.Name != "api" {
		t.Errorf("name = %q, want the slug api", got.Name)
	}
	if got.URL != info.URL {
		t.Errorf("url = %q, want %q", got.URL, info.URL)
	}
	if got.Scope != string(board.ScopeLive) {
		t.Errorf("scope = %q, want live", got.Scope)
	}
}

// TestBoardHTMLExternalWarningNamesTheFirstReference is the startup warning: one
// line, naming the first reference and the count.
func TestBoardHTMLExternalWarningNamesTheFirstReference(t *testing.T) {
	dir := t.TempDir()
	got := boardHTMLExternalRefs(seedBoardHTML(t, dir, []byte(
		`<img src="https://cdn.example/a.png"><img src="https://cdn.example/b.png">`)))
	if len(got) != 2 {
		t.Fatalf("refs = %v, want two", got)
	}
	if got[0] != "https://cdn.example/a.png" {
		t.Errorf("first ref = %q, want the document's first", got[0])
	}
}

// TestBoardHTMLExternalWarningIsQuietWhenOffline: a self-contained board and a
// board that does not exist yet both say nothing.
func TestBoardHTMLExternalWarningIsQuietWhenOffline(t *testing.T) {
	dir := t.TempDir()
	if got := boardHTMLExternalRefs(seedBoardHTML(t, dir, []byte(
		`<h1>board</h1><svg xmlns="http://www.w3.org/2000/svg"><rect/></svg>`))); len(got) != 0 {
		t.Errorf("refs = %v, want none", got)
	}
	if got := boardHTMLExternalRefs(filepath.Join(dir, "does-not-exist", "board.html")); len(got) != 0 {
		t.Errorf("refs for a missing board = %v, want none", got)
	}
}

// seedBoardHTML writes body into a board.html under dir and returns its path.
func seedBoardHTML(t *testing.T, dir string, body []byte) string {
	t.Helper()
	path := filepath.Join(dir, "api", "board.html")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write board: %v", err)
	}
	return path
}
