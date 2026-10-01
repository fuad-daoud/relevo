package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fuad-daoud/relevo/internal/board"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
)

// TestBoardIsAPeekVerb pins the route: board and its subverbs neither dial the
// machine database nor capture the agy environment.
func TestBoardIsAPeekVerb(t *testing.T) {
	for _, args := range [][]string{{"board"}, {"board", "url"}, {"board", "comments"}, {"board", "comment"}} {
		if !isPeekArgs(args) {
			t.Errorf("isPeekArgs(%q) = false, want true", args)
		}
		if mode, _, _ := routeForArgs(args); mode != routeNone {
			t.Errorf("routeForArgs(%q) = %v, want none", args, mode)
		}
		if peek := installRouteForArgs(args); !peek {
			t.Errorf("installRouteForArgs(%q) = false, want the peek that skips capture", args)
		}
	}
}

// TestBoardRefusesOutsideRepoForAnExplicitPath: without a repository an
// explicit scene path is usage. A bare board is the live board now, so it no
// longer consults the repository at all.
func TestBoardRefusesOutsideRepoForAnExplicitPath(t *testing.T) {
	orig := boardRepoRootFn
	boardRepoRootFn = func(string) (string, error) { return "", errors.New("not a git repository") }
	t.Cleanup(func() { boardRepoRootFn = orig })

	err := cmdBoard([]string{"docs/boards/board.excalidraw"})
	requireCLIError(t, err, codeUsage, "relevo help")
}

// TestBoardRefusesBadTheme: an unknown theme is usage and names the built-ins.
func TestBoardRefusesBadTheme(t *testing.T) {
	withTempRepoRoot(t)
	ce := requireCLIError(t, cmdBoard([]string{"--theme", "nope"}), codeUsage, "relevo help")
	if !strings.Contains(ce.message, "cockpit") || !strings.Contains(ce.message, "blueprint") {
		t.Errorf("refusal %q does not list the built-ins", ce.message)
	}
}

// TestBoardRefusesBadPath: a path that is not a scene is usage.
func TestBoardRefusesBadPath(t *testing.T) {
	withTempRepoRoot(t)
	requireCLIError(t, cmdBoard([]string{"notes.txt"}), codeUsage, "")
}

// TestBoardThemePrecedence: --theme wins, then relevo.boardTheme, then cockpit.
func TestBoardThemePrecedence(t *testing.T) {
	orig := boardGitConfig
	t.Cleanup(func() { boardGitConfig = orig })

	boardGitConfig = func(string, string) (string, error) { return "blueprint", nil }
	if th, err := boardResolveTheme("", ""); err != nil || th.Name != "blueprint" {
		t.Errorf("relevo.boardTheme = %v, %v; want blueprint", th, err)
	}
	if th, err := boardResolveTheme("", "cockpit"); err != nil || th.Name != "cockpit" {
		t.Errorf("--theme cockpit = %v, %v; want cockpit", th, err)
	}

	boardGitConfig = func(string, string) (string, error) { return "", nil }
	if th, err := boardResolveTheme("", ""); err != nil || th.Name != "cockpit" {
		t.Errorf("default = %v, %v; want cockpit", th, err)
	}
}

// withTempRepoRoot points the repo-root seam at a temp dir for one test.
func withTempRepoRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := boardRepoRootFn
	boardRepoRootFn = func(string) (string, error) { return dir, nil }
	t.Cleanup(func() { boardRepoRootFn = orig })
	return dir
}

// seedBoardRecords creates <stateHome>/relevo/relevo.db with recs, then closes
// it, so a later read-only open finds a complete file.
func seedBoardRecords(t *testing.T, stateHome string, recs ...mastermind.Record) {
	t.Helper()
	dir := filepath.Join(stateHome, "relevo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	d, err := db.Open(filepath.Join(dir, "relevo.db"))
	if err != nil {
		t.Fatalf("open relevo.db: %v", err)
	}
	reg := &mastermind.DBRegistry{KV: db.TxKV{DB: d}}
	for _, rec := range recs {
		if _, err := reg.Create(rec); err != nil {
			_ = d.Close()
			t.Fatalf("mastermind Create: %v", err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close relevo.db: %v", err)
	}
}

// boardRecord is one valid record for the resolution tests.
func boardRecord(id, name string) mastermind.Record {
	return mastermind.Record{
		ID: id, Name: name, HarnessKind: "claude", SessionID: "sess-" + name,
		CWD: filepath.Join("/tmp", "relevo-cwd-"+name),
	}
}

// boardStateRoot isolates the state root and returns the live root.
func boardStateRoot(t *testing.T) string {
	t.Helper()
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	return filepath.Join(stateHome, "relevo", "boards")
}

// stubRepoSeamAbsent stubs the repository seam to report no repository. A live
// board outside a repository still resolves (S2/R6), so a missing repository is
// tolerated rather than a test failure.
func stubRepoSeamAbsent(t *testing.T) {
	t.Helper()
	orig := boardRepoRootFn
	boardRepoRootFn = func(string) (string, error) { return "", errors.New("no repository") }
	t.Cleanup(func() { boardRepoRootFn = orig })
}

func TestBoardResolvesExplicitLivePath(t *testing.T) {
	liveRoot := boardStateRoot(t)
	stubRepoSeamAbsent(t)
	id := "mm_aaaaaaaaaaaa"
	liveDir := filepath.Join(liveRoot, id)
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	scene := filepath.Join(liveDir, "board.excalidraw")

	res, err := resolveBoard(t.TempDir(), "", "", scene)
	if err != nil {
		t.Fatalf("resolveBoard: %v", err)
	}
	// Paths leave canonicalized, so compare against the symlink-resolved
	// spellings: on macOS t.TempDir sits under /var, a symlink to /private/var.
	// The scene file itself does not exist yet, so resolve its parent.
	wantDir, err := filepath.EvalSymlinks(liveDir)
	if err != nil {
		t.Fatalf("EvalSymlinks liveDir: %v", err)
	}
	wantPath := filepath.Join(wantDir, "board.excalidraw")
	if res.Scope != board.ScopeLive || res.Scene != "board" || res.Path != wantPath || res.LiveDir != wantDir {
		t.Errorf("resolveBoard = %+v", res)
	}
}

func TestBoardResolvesExplicitRepoPath(t *testing.T) {
	boardStateRoot(t)
	repoRoot := withTempRepoRoot(t)

	res, err := resolveBoard(repoRoot, "", "", "docs/boards/api.excalidraw")
	if err != nil {
		t.Fatalf("resolveBoard: %v", err)
	}
	if res.Scope != board.ScopeRepo || res.Scene != "api" {
		t.Errorf("resolveBoard = %+v, want a repo api scene", res)
	}
	if want := filepath.Join(repoRoot, "docs", "boards", "api.excalidraw"); res.Path != want {
		t.Errorf("path = %q, want %q", res.Path, want)
	}
}

func TestBoardBareAndFlagAreLiveWithoutARepo(t *testing.T) {
	liveRoot := boardStateRoot(t)
	seedBoardRecords(t, filepath.Dir(filepath.Dir(liveRoot)), boardRecord("mm_aaaaaaaaaaaa", "alpha"))
	stubRepoSeamAbsent(t)
	cwd := t.TempDir()

	res, err := resolveBoard(cwd, "", "", "")
	if err != nil {
		t.Fatalf("resolveBoard(bare): %v", err)
	}
	if res.Scope != board.ScopeLive || res.Scene != board.DefaultBoard || res.FromPointer {
		t.Errorf("bare board = %+v, want live default", res)
	}
	if want := filepath.Join(liveRoot, "mm_aaaaaaaaaaaa"); res.LiveDir != want {
		t.Errorf("live dir = %q, want %q", res.LiveDir, want)
	}

	res, err = resolveBoard(cwd, "", "notes", "")
	if err != nil {
		t.Fatalf("resolveBoard(--board): %v", err)
	}
	if res.Scope != board.ScopeLive || res.Scene != "notes" {
		t.Errorf("--board notes = %+v, want live notes", res)
	}
}

// TestBoardRefusesNestedScopes pins S5 at the CLI: every form of `relevo board`
// refuses when the repository root and the live scope nest, in either
// direction, before any pointer write or listener.
func TestBoardRefusesNestedScopes(t *testing.T) {
	forms := []struct {
		name string
		flag string
		arg  string
	}{
		{"bare", "", ""},
		{"--board notes", "notes", ""},
		{"explicit live path", "", "live"},
		{"explicit repo path", "", "docs/boards/api.excalidraw"},
	}
	for _, form := range forms {
		t.Run(form.name, func(t *testing.T) {
			liveRoot := boardStateRoot(t)
			id := "mm_aaaaaaaaaaaa"
			liveDir := filepath.Join(liveRoot, id)
			repoRoot := filepath.Dir(filepath.Dir(liveRoot))
			if err := os.MkdirAll(liveDir, 0o700); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			arg := form.arg
			if arg == "live" {
				arg = filepath.Join(liveDir, "board.excalidraw")
			}

			orig := boardRepoRootFn
			boardRepoRootFn = func(string) (string, error) { return repoRoot, nil }
			t.Cleanup(func() { boardRepoRootFn = orig })

			// cwd is the repo root so the explicit-repo-path form would
			// resolve -- not refuse -- without C2's wiring.
			_, err := resolveBoard(repoRoot, id, form.flag, arg)
			requireCLIError(t, err, codeUsage, "")
		})
	}

	// And the reverse: the repository root under the live scope.
	t.Run("repository root under the live scope", func(t *testing.T) {
		liveRoot := boardStateRoot(t)
		id := "mm_aaaaaaaaaaaa"
		liveDir := filepath.Join(liveRoot, id)
		repoRoot := filepath.Join(liveDir, "repo")
		if err := os.MkdirAll(liveDir, 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		orig := boardRepoRootFn
		boardRepoRootFn = func(string) (string, error) { return repoRoot, nil }
		t.Cleanup(func() { boardRepoRootFn = orig })

		_, err := resolveBoard(repoRoot, id, "", "")
		requireCLIError(t, err, codeUsage, "")
	})
}

func TestBoardPathAndFlagAreExclusive(t *testing.T) {
	boardStateRoot(t)
	_, err := resolveBoard(t.TempDir(), "", "notes", "docs/boards/x.excalidraw")
	requireCLIError(t, err, codeUsage, "")
}

func TestBoardDisagreeingMastermindRefused(t *testing.T) {
	liveRoot := boardStateRoot(t)
	liveDir := filepath.Join(liveRoot, "mm_aaaaaaaaaaaa")
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	scene := filepath.Join(liveDir, "board.excalidraw")
	_, err := resolveBoard(t.TempDir(), "mm_bbbbbbbbbbbb", "", scene)
	requireCLIError(t, err, codeUsage, "")
}

func TestBoardUnknownRefIsNotFound(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	seedBoardRecords(t, stateHome, boardRecord("mm_aaaaaaaaaaaa", "alpha"))
	_, err := resolveBoard(t.TempDir(), "nosuch", "", "")
	requireCLIError(t, err, codeMastermindNotFound, "")
}

func TestBoardTwoRecordsAreListed(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	seedBoardRecords(t, stateHome,
		boardRecord("mm_aaaaaaaaaaaa", "alpha"),
		boardRecord("mm_bbbbbbbbbbbb", "beta"),
	)
	_, err := resolveBoard(t.TempDir(), "", "", "")
	ce := requireCLIError(t, err, codeUsage, "")
	if !strings.Contains(ce.message, "alpha") || !strings.Contains(ce.message, "beta") {
		t.Errorf("refusal %q does not list both records", ce.message)
	}
}

func TestBoardZeroRecordsNamesInit(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	seedBoardRecords(t, stateHome) // an existing database with no records
	_, err := resolveBoard(t.TempDir(), "", "", "")
	ce := requireCLIError(t, err, codeUsage, "")
	if !strings.Contains(ce.message, "mastermind init") {
		t.Errorf("refusal %q does not name relevo mastermind init", ce.message)
	}
}

func TestBoardNoDatabaseNameRefRefuses(t *testing.T) {
	boardStateRoot(t) // no relevo.db is created
	for _, ref := range []string{"alpha", ""} {
		_, err := resolveBoard(t.TempDir(), ref, "", "")
		ce := requireCLIError(t, err, codeRefused, "")
		if !strings.Contains(ce.message, "--mastermind") {
			t.Errorf("refusal %q does not name --mastermind", ce.message)
		}
	}
}

func TestBoardIDRefNeedsNoDatabase(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	liveRoot := filepath.Join(stateHome, "relevo", "boards")

	res, err := resolveBoard(t.TempDir(), "mm_bbbbbbbbbbbb", "", "")
	if err != nil {
		t.Fatalf("resolveBoard(id ref): %v", err)
	}
	if res.Scope != board.ScopeLive || res.LiveDir != filepath.Join(liveRoot, "mm_bbbbbbbbbbbb") {
		t.Errorf("resolveBoard(id ref) = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(stateHome, "relevo", "relevo.db")); !os.IsNotExist(err) {
		t.Errorf("an id ref created a database: stat error = %v, want not-exist", err)
	}
}

// seedLiveBoard writes a pointer and a live server.json (this process's own
// pid and start) for id, returning the live directory.
func seedLiveBoard(t *testing.T, liveRoot, id, scene, url string) string {
	t.Helper()
	liveDir := filepath.Join(liveRoot, id)
	if err := board.WritePointer(liveDir, scene); err != nil {
		t.Fatalf("WritePointer: %v", err)
	}
	started, err := procStartUnix(os.Getpid())
	if err != nil {
		t.Skipf("process start time unavailable: %v", err)
	}
	info := board.ServerInfo{Scene: scene, URL: url, Port: 1, PID: os.Getpid(), StartedAt: started}
	if err := board.WriteServerInfo(liveDir, info); err != nil {
		t.Fatalf("WriteServerInfo: %v", err)
	}
	return liveDir
}

func TestBoardURLAlive(t *testing.T) {
	liveRoot := boardStateRoot(t)
	id := "mm_aaaaaaaaaaaa"
	url := "http://127.0.0.1:41234/#t=deadbeef"
	seedLiveBoard(t, liveRoot, id, "board", url)
	t.Setenv("RELEVO_MASTERMIND", id)

	stdout, stderr, err := captureOutput(t, func() error { return cmdBoardURL(nil) })
	if err != nil {
		t.Fatalf("board url: %v (stderr %s)", err, stderr)
	}
	if string(stdout) != url+"\n" {
		t.Errorf("stdout = %q, want exactly %q", stdout, url+"\n")
	}
}

func TestBoardURLBoardFlag(t *testing.T) {
	liveRoot := boardStateRoot(t)
	id := "mm_aaaaaaaaaaaa"
	url := "http://127.0.0.1:40001/#t=cafe"
	seedLiveBoard(t, liveRoot, id, "notes", url)
	t.Setenv("RELEVO_MASTERMIND", id)

	stdout, stderr, err := captureOutput(t, func() error { return cmdBoardURL([]string{"--board", "notes"}) })
	if err != nil {
		t.Fatalf("board url --board notes: %v (stderr %s)", err, stderr)
	}
	if string(stdout) != url+"\n" {
		t.Errorf("stdout = %q, want exactly %q", stdout, url+"\n")
	}
}

func TestBoardURLNotAvailable(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, liveRoot, id string) string
	}{
		{"missing pointer", func(t *testing.T, liveRoot, id string) string {
			return board.DefaultBoard
		}},
		{"pointer without server", func(t *testing.T, liveRoot, id string) string {
			if err := board.WritePointer(filepath.Join(liveRoot, id), "board"); err != nil {
				t.Fatalf("WritePointer: %v", err)
			}
			return "board"
		}},
		{"dead pid", func(t *testing.T, liveRoot, id string) string {
			liveDir := filepath.Join(liveRoot, id)
			if err := board.WritePointer(liveDir, "board"); err != nil {
				t.Fatalf("WritePointer: %v", err)
			}
			info := board.ServerInfo{Scene: "board", URL: "http://127.0.0.1:1/#t=x", Port: 1, PID: 1 << 30, StartedAt: 123}
			if err := board.WriteServerInfo(liveDir, info); err != nil {
				t.Fatalf("WriteServerInfo: %v", err)
			}
			return "board"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			liveRoot := boardStateRoot(t)
			id := "mm_aaaaaaaaaaaa"
			scene := tc.setup(t, liveRoot, id)
			t.Setenv("RELEVO_MASTERMIND", id)

			stdout, _, err := captureOutput(t, func() error { return cmdBoardURL(nil) })
			ce := requireCLIError(t, err, codeNotAvailable, "")
			if len(stdout) != 0 {
				t.Errorf("stdout = %q, want it empty", stdout)
			}
			if !strings.Contains(ce.message, scene) || !strings.Contains(ce.message, id) {
				t.Errorf("refusal %q must name scene %q and MasterMind %s", ce.message, scene, id)
			}
		})
	}
}

func TestBoardURLDoesNotWritePointer(t *testing.T) {
	liveRoot := boardStateRoot(t)
	id := "mm_aaaaaaaaaaaa"
	t.Setenv("RELEVO_MASTERMIND", id)

	_, _, err := captureOutput(t, func() error { return cmdBoardURL(nil) })
	requireCLIError(t, err, codeNotAvailable, "")
	if _, statErr := os.Stat(filepath.Join(liveRoot, id, "current")); !os.IsNotExist(statErr) {
		t.Errorf("board url wrote the pointer: stat error = %v, want not-exist", statErr)
	}
}

// TestBoardResolutionNeverStartsTheDaemon is R5's proof: resolving a name
// against a seeded database starts nothing, binds no socket and writes nothing.
func TestBoardResolutionNeverStartsTheDaemon(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(stateHome, "config"))
	t.Setenv("HOME", stateHome)
	seedBoardRecords(t, stateHome, boardRecord("mm_aaaaaaaaaaaa", "alpha"))

	var calls int32
	daemonStarter = func() error { atomic.AddInt32(&calls, 1); return nil }
	t.Cleanup(func() { daemonStarter = startDaemon })
	stubRepoSeamAbsent(t)

	res, err := resolveBoard(t.TempDir(), "alpha", "", "")
	if err != nil {
		t.Fatalf("resolveBoard(name): %v", err)
	}
	if res.Scope != board.ScopeLive || res.Scene != board.DefaultBoard {
		t.Errorf("resolveBoard(name) = %+v, want the live default", res)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("resolution started the daemon %d times, want 0", got)
	}
	if _, err := os.Stat(filepath.Join(stateHome, "relevo", "relevo.sock")); !os.IsNotExist(err) {
		t.Errorf("resolution created a socket: stat error = %v, want not-exist", err)
	}
}

// TestBoardServerInfoComposition pins the advertisement a live board composes:
// nil for a repo board, the five fields for a live one (S6).
func TestBoardServerInfoComposition(t *testing.T) {
	if got := boardServerInfo("board", "http://127.0.0.1:41234/#t=x", 7, 1700000000, ""); got != nil {
		t.Errorf("repo scope = %+v, want nil", got)
	}
	got := boardServerInfo("board", "http://127.0.0.1:41234/#t=x", 7, 1700000000, "/live")
	want := board.ServerInfo{Scene: "board", URL: "http://127.0.0.1:41234/#t=x", Port: 41234, PID: 7, StartedAt: 1700000000}
	if got == nil || *got != want {
		t.Errorf("live scope = %+v, want %+v", got, want)
	}
}

// cockpitConfig points the theme resolver at the built-in default, so a test
// never reads the machine's git config.
func cockpitConfig(t *testing.T) {
	t.Helper()
	orig := boardGitConfig
	boardGitConfig = func(string, string) (string, error) { return "", nil }
	t.Cleanup(func() { boardGitConfig = orig })
}

// seedBoardScene writes a scene into root and returns its absolute path.
func seedBoardScene(t *testing.T, root, name, body string) string {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write scene: %v", err)
	}
	return p
}

// readBoardElements reads path's elements as generic objects.
func readBoardElements(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read scene: %v", err)
	}
	var doc struct {
		Elements []map[string]any `json:"elements"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode scene: %v", err)
	}
	return doc.Elements
}

// TestBoardTextJSONDocument pins `board text --json`: one line, field order
// id/x/y/text, x/y as JSON numbers, deleted and non-text elements skipped.
func TestBoardTextJSONDocument(t *testing.T) {
	cockpitConfig(t)
	root := withTempRepoRoot(t)
	t.Chdir(root)
	seedBoardScene(t, root, "board.excalidraw",
		`{"type":"excalidraw","elements":[`+
			`{"id":"a","type":"text","x":10,"y":20,"text":"hello"},`+
			`{"id":"r","type":"rectangle","x":0,"y":0},`+
			`{"id":"b","type":"text","x":12.5,"y":0,"text":"two"},`+
			`{"id":"c","type":"text","x":1,"y":1,"text":"gone","isDeleted":true}]}`)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"board", "text", "board.excalidraw", "--json"})
	})
	if err != nil {
		t.Fatalf("board text --json: %v (stderr %s)", err, stderr)
	}
	want := "[{\"id\":\"a\",\"x\":10,\"y\":20,\"text\":\"hello\"}," +
		"{\"id\":\"b\",\"x\":12.5,\"y\":0,\"text\":\"two\"}]\n"
	if string(stdout) != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// TestTextJSONEmptyIsEmptyArray pins the empty document as `[]`, never `null`.
func TestTextJSONEmptyIsEmptyArray(t *testing.T) {
	cockpitConfig(t)
	root := withTempRepoRoot(t)
	t.Chdir(root)
	seedBoardScene(t, root, "board.excalidraw", `{"type":"excalidraw","elements":[]}`)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"board", "text", "board.excalidraw", "--json"})
	})
	if err != nil {
		t.Fatalf("board text --json: %v (stderr %s)", err, stderr)
	}
	if string(stdout) != "[]\n" {
		t.Errorf("stdout = %q, want %q", stdout, "[]\n")
	}
}

// TestBoardTextHumanRows pins the human form: one row per element through the
// tabwriter, no header, and nothing at all for an empty scene.
func TestBoardTextHumanRows(t *testing.T) {
	cockpitConfig(t)
	root := withTempRepoRoot(t)
	t.Chdir(root)
	seedBoardScene(t, root, "board.excalidraw",
		`{"type":"excalidraw","elements":[`+
			`{"id":"a","type":"text","x":10,"y":20,"text":"hello"},`+
			`{"id":"b","type":"text","x":12.5,"y":0,"text":"two"}]}`)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"board", "text", "board.excalidraw"})
	})
	if err != nil {
		t.Fatalf("board text: %v (stderr %s)", err, stderr)
	}
	lines := strings.Split(strings.TrimRight(string(stdout), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("rows = %q, want two", stdout)
	}
	if got := strings.Fields(lines[0]); len(got) != 4 || got[0] != "a" || got[1] != "10" || got[2] != "20" || got[3] != "hello" {
		t.Errorf("row 0 = %v, want a 10 20 hello", got)
	}
	if got := strings.Fields(lines[1]); len(got) != 4 || got[0] != "b" || got[1] != "12.5" || got[2] != "0" || got[3] != "two" {
		t.Errorf("row 1 = %v, want b 12.5 0 two", got)
	}

	seedBoardScene(t, root, "empty.excalidraw", `{"type":"excalidraw","elements":[]}`)
	stdout, _, err = captureOutput(t, func() error {
		return run([]string{"board", "text", "empty.excalidraw"})
	})
	if err != nil {
		t.Fatalf("board text empty: %v", err)
	}
	if len(stdout) != 0 {
		t.Errorf("empty scene printed %q, want nothing", stdout)
	}
}

// TestBoardAnnotateAppends pins the annotate happy path: the stdout line, and
// a well-formed appended element that keeps the scene's other bytes.
func TestBoardAnnotateAppends(t *testing.T) {
	cockpitConfig(t)
	root := withTempRepoRoot(t)
	t.Chdir(root)
	path := seedBoardScene(t, root, "board.excalidraw", `{"type":"excalidraw","elements":[]}`)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"board", "annotate", "board.excalidraw", "--text", "hello"})
	})
	if err != nil {
		t.Fatalf("board annotate: %v (stderr %s)", err, stderr)
	}
	els := readBoardElements(t, path)
	if len(els) != 1 {
		t.Fatalf("elements = %d, want 1", len(els))
	}
	el := els[0]
	id, _ := el["id"].(string)
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) {
		t.Errorf("id = %q, want 32 hex characters", id)
	}
	want := fmt.Sprintf("board: annotated %s (id %s)\n", path, id)
	if string(stdout) != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if el["text"] != "hello" || el["type"] != "text" {
		t.Errorf("element = %v, want text hello", el)
	}
	if el["autoResize"] != true || el["fontFamily"] != float64(3) {
		t.Errorf("font safety fields = autoResize %v fontFamily %v", el["autoResize"], el["fontFamily"])
	}
	if w, _ := el["width"].(float64); w < 60 {
		t.Errorf("width = %v, want >= 60 (12 per byte)", el["width"])
	}
	if el["strokeColor"] != "#e6e8ec" {
		t.Errorf("strokeColor = %v, want the cockpit ink", el["strokeColor"])
	}
	if seed, _ := el["seed"].(float64); seed < 0 || seed >= float64(int64(1)<<31) {
		t.Errorf("seed = %v, want [0, 2^31)", el["seed"])
	}
	if updated, _ := el["updated"].(float64); updated <= 0 {
		t.Errorf("updated = %v, want a positive unix ms", el["updated"])
	}
}

// TestBoardAnnotateThemeInk pins that the repo-local theme key reaches the
// appended element's stroke colour.
func TestBoardAnnotateThemeInk(t *testing.T) {
	orig := boardGitConfig
	boardGitConfig = func(string, string) (string, error) { return "blueprint", nil }
	t.Cleanup(func() { boardGitConfig = orig })

	root := withTempRepoRoot(t)
	t.Chdir(root)
	path := seedBoardScene(t, root, "board.excalidraw", `{"type":"excalidraw","elements":[]}`)

	if _, _, err := captureOutput(t, func() error {
		return run([]string{"board", "annotate", "board.excalidraw", "--text", "hi"})
	}); err != nil {
		t.Fatalf("board annotate: %v", err)
	}
	if got := readBoardElements(t, path)[0]["strokeColor"]; got != "#dbe4f0" {
		t.Errorf("strokeColor = %v, want the blueprint ink", got)
	}
}

// TestBoardTextRefusals pins text's usage and refused outcomes.
func TestBoardTextRefusals(t *testing.T) {
	cockpitConfig(t)
	root := withTempRepoRoot(t)
	t.Chdir(root)
	seedBoardScene(t, root, "bad.excalidraw", `{"type":"other","elements":[]}`)

	cases := []struct {
		name string
		args []string
		code errorCode
		msg  string
	}{
		{"no path", []string{"board", "text"}, codeUsage, "needs one scene path"},
		{"two paths", []string{"board", "text", "a.excalidraw", "b.excalidraw"}, codeUsage, "needs one scene path"},
		{"unknown flag", []string{"board", "text", "--bogus", "a.excalidraw"}, codeUsage, ""},
		{"bad extension", []string{"board", "text", "notes.txt"}, codeUsage, "excalidraw"},
		{"missing scene", []string{"board", "text", "missing.excalidraw"}, codeRefused, "no scene at"},
		{"invalid scene", []string{"board", "text", "bad.excalidraw"}, codeRefused, "scene type is not"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := captureOutput(t, func() error { return run(tc.args) })
			ce := requireCLIError(t, err, tc.code, "")
			if tc.msg != "" && !strings.Contains(ce.message, tc.msg) {
				t.Errorf("message = %q, want it to contain %q", ce.message, tc.msg)
			}
		})
	}

	t.Run("outside repo", func(t *testing.T) {
		orig := boardRepoRootFn
		boardRepoRootFn = func(string) (string, error) { return "", errors.New("not a git repository") }
		t.Cleanup(func() { boardRepoRootFn = orig })
		_, _, err := captureOutput(t, func() error {
			return run([]string{"board", "text", "a.excalidraw"})
		})
		requireCLIError(t, err, codeUsage, "")
	})
}

// TestBoardAnnotateRefusals pins annotate's usage and refused outcomes, and
// that every usage check runs before any repository or file work.
func TestBoardAnnotateRefusals(t *testing.T) {
	cockpitConfig(t)
	root := withTempRepoRoot(t)
	t.Chdir(root)
	seedBoardScene(t, root, "bad.excalidraw", `{"type":"other","elements":[]}`)

	cases := []struct {
		name string
		args []string
		code errorCode
		msg  string
	}{
		{"no path", []string{"board", "annotate"}, codeUsage, "needs one scene path"},
		{"two paths", []string{"board", "annotate", "a.excalidraw", "b.excalidraw"}, codeUsage, "needs one scene path"},
		{"text absent", []string{"board", "annotate", "a.excalidraw"}, codeUsage, "--text is required"},
		{"text empty", []string{"board", "annotate", "a.excalidraw", "--text", ""}, codeUsage, "--text must not be empty"},
		{"lone x", []string{"board", "annotate", "a.excalidraw", "--text", "hi", "--x", "1"}, codeUsage, "--x and --y come together"},
		{"lone y", []string{"board", "annotate", "a.excalidraw", "--text", "hi", "--y", "1"}, codeUsage, "--x and --y come together"},
		{"non-finite x", []string{"board", "annotate", "a.excalidraw", "--text", "hi", "--x", "NaN", "--y", "1"}, codeUsage, "--x must be a finite number"},
		{"non-finite y", []string{"board", "annotate", "a.excalidraw", "--text", "hi", "--x", "1", "--y", "Inf"}, codeUsage, "--y must be a finite number"},
		{"unknown flag", []string{"board", "annotate", "--bogus", "a.excalidraw", "--text", "hi"}, codeUsage, ""},
		{"missing scene", []string{"board", "annotate", "missing.excalidraw", "--text", "hi"}, codeRefused, "no scene at"},
		{"invalid scene", []string{"board", "annotate", "bad.excalidraw", "--text", "hi"}, codeRefused, "scene type is not"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := captureOutput(t, func() error { return run(tc.args) })
			ce := requireCLIError(t, err, tc.code, "")
			if tc.msg != "" && !strings.Contains(ce.message, tc.msg) {
				t.Errorf("message = %q, want it to contain %q", ce.message, tc.msg)
			}
		})
	}
}
