package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/board/theme"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
)

// testDeps returns Deps with hermetic defaults: no repository, an empty git
// config, the seed palettes, the real clock, the isolated live root, a real
// read-only opener, and a registry lookup without CLI codes. Tests override
// the fields they pin.
func testDeps(t *testing.T) Deps {
	t.Helper()
	return Deps{
		Listen: func() (net.Listener, error) {
			return nil, errors.New("board: no listener in tests")
		},
		Open: func(string) error {
			return errors.New("board: no browser in tests")
		},
		ProcStart: func(int) (int64, error) {
			return 0, errors.New("board: no proc start in tests")
		},
		RepoRoot: func(string) (string, error) {
			return "", errors.New("no repository")
		},
		GitConfig: func(string, string) (string, error) {
			return "", nil
		},
		ThemeSets: func(string) (string, map[string]Theme, error) {
			return "", theme.Seed(), nil
		},
		Now:      time.Now,
		LiveRoot: LiveRoot,
		DialOwner: func(context.Context, string, time.Duration) (*db.DB, error) {
			return nil, errors.New("board: no owner in tests")
		},
		OpenDB: func(path string, o db.Options) (*db.DB, error) {
			return db.OpenReadOnlySplit(path, o)
		},
		DialBudget: time.Second,
		OwnerSocket: func(string) (string, error) {
			return "", errors.New("board: no owner socket in tests")
		},
		MastermindLookup: testMastermindLookup,
	}
}

// testMastermindLookup resolves one ref against the registry without CLI
// codes: an id ref through Get, a name ref through ByName.
func testMastermindLookup(reg mastermind.Registry, ref string) (mastermind.Record, error) {
	if mastermind.ValidID(ref) == nil {
		return reg.Get(ref)
	}
	return reg.ByName(ref)
}

// withTempRepoRoot points the repo-root dependency at a temp dir for one test.
func withTempRepoRoot(t *testing.T, deps *Deps) string {
	t.Helper()
	dir := t.TempDir()
	deps.RepoRoot = func(string) (string, error) { return dir, nil }
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

// captureOutput runs fn with stdout and stderr redirected and returns what
// each printed. It reads the pipes only after the writers are closed, so a
// small print cannot fill the pipe buffer and deadlock the verb under test.
func captureOutput(t *testing.T, fn func() error) (stdout []byte, stderr []byte, err error) {
	t.Helper()

	origStdout := os.Stdout
	origStderr := os.Stderr

	rOut, wOut, errOut := os.Pipe()
	if errOut != nil {
		t.Fatalf("pipe: %v", errOut)
	}
	rErr, wErr, errPipe := os.Pipe()
	if errPipe != nil {
		t.Fatalf("pipe: %v", errPipe)
	}

	os.Stdout = wOut
	os.Stderr = wErr

	runErr := fn()

	_ = wOut.Close()
	_ = wErr.Close()

	os.Stdout = origStdout
	os.Stderr = origStderr

	outBytes, _ := io.ReadAll(rOut)
	errBytes, _ := io.ReadAll(rErr)

	return outBytes, errBytes, runErr
}

// TestBoardRefusesOutsideRepoForAnExplicitPath: without a repository an
// explicit scene path is usage. A bare board is the live board now, so it no
// longer consults the repository at all.
func TestBoardRefusesOutsideRepoForAnExplicitPath(t *testing.T) {
	boardStateRoot(t)
	deps := testDeps(t)

	err := Run(deps, t.TempDir(), "", "", "", "docs/boards/board.excalidraw", false, nil)
	if !errors.Is(err, ErrUsage) {
		t.Errorf("Run(explicit path, no repo) = %v, want ErrUsage", err)
	}
}

// TestBoardRefusesBadTheme: an unknown theme is usage and names the set the
// resolution consulted.
func TestBoardRefusesBadTheme(t *testing.T) {
	boardStateRoot(t)
	deps := testDeps(t)
	dir := withTempRepoRoot(t, &deps)

	err := Run(deps, dir, "", "nope", "", "", false, nil)
	if !errors.Is(err, theme.ErrUsage) {
		t.Fatalf("Run(--theme nope) = %v, want the palette refusal", err)
	}
	if !strings.Contains(err.Error(), "cockpit") || !strings.Contains(err.Error(), "blueprint") {
		t.Errorf("refusal %q does not list the consulted set", err)
	}
}

// TestBoardRefusesBadPath: a path that is not a scene is usage.
func TestBoardRefusesBadPath(t *testing.T) {
	boardStateRoot(t)
	deps := testDeps(t)
	dir := withTempRepoRoot(t, &deps)

	if err := Run(deps, dir, "", "", "", "notes.txt", false, nil); !errors.Is(err, ErrUsage) {
		t.Errorf("Run(notes.txt) = %v, want ErrUsage", err)
	}
}

func TestBoardResolvesExplicitLivePath(t *testing.T) {
	liveRoot := boardStateRoot(t)
	deps := testDeps(t)
	id := "mm_aaaaaaaaaaaa"
	liveDir := filepath.Join(liveRoot, id)
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	scene := filepath.Join(liveDir, "board.excalidraw")

	res, err := ResolveScene(deps, t.TempDir(), "", "", scene)
	if err != nil {
		t.Fatalf("ResolveScene: %v", err)
	}
	// Paths leave canonicalized, so compare against the symlink-resolved
	// spellings: on macOS t.TempDir sits under /var, a symlink to /private/var.
	// The scene file itself does not exist yet, so resolve its parent.
	wantDir, err := filepath.EvalSymlinks(liveDir)
	if err != nil {
		t.Fatalf("EvalSymlinks liveDir: %v", err)
	}
	wantPath := filepath.Join(wantDir, "board.excalidraw")
	if res.Scope != ScopeLive || res.Scene != "board" || res.Path != wantPath || res.LiveDir != wantDir {
		t.Errorf("ResolveScene = %+v", res)
	}
}

func TestBoardResolvesExplicitRepoPath(t *testing.T) {
	boardStateRoot(t)
	deps := testDeps(t)
	repoRoot := withTempRepoRoot(t, &deps)

	res, err := ResolveScene(deps, repoRoot, "", "", "docs/boards/api.excalidraw")
	if err != nil {
		t.Fatalf("ResolveScene: %v", err)
	}
	if res.Scope != ScopeRepo || res.Scene != "api" {
		t.Errorf("ResolveScene = %+v, want a repo api scene", res)
	}
	if want := filepath.Join(repoRoot, "docs", "boards", "api.excalidraw"); res.Path != want {
		t.Errorf("path = %q, want %q", res.Path, want)
	}
}

func TestBoardBareAndFlagAreLiveWithoutARepo(t *testing.T) {
	liveRoot := boardStateRoot(t)
	deps := testDeps(t)
	seedBoardRecords(t, filepath.Dir(filepath.Dir(liveRoot)), boardRecord("mm_aaaaaaaaaaaa", "alpha"))
	cwd := t.TempDir()

	res, err := ResolveScene(deps, cwd, "", "", "")
	if err != nil {
		t.Fatalf("ResolveScene(bare): %v", err)
	}
	if res.Scope != ScopeLive || res.Scene != DefaultBoard || res.FromPointer {
		t.Errorf("bare board = %+v, want live default", res)
	}
	if want := filepath.Join(liveRoot, "mm_aaaaaaaaaaaa"); res.LiveDir != want {
		t.Errorf("live dir = %q, want %q", res.LiveDir, want)
	}

	res, err = ResolveScene(deps, cwd, "", "notes", "")
	if err != nil {
		t.Fatalf("ResolveScene(--board): %v", err)
	}
	if res.Scope != ScopeLive || res.Scene != "notes" {
		t.Errorf("--board notes = %+v, want live notes", res)
	}
}

// TestBoardRefusesNestedScopes pins S5: every form of `relevo board`
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
			deps := testDeps(t)
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

			deps.RepoRoot = func(string) (string, error) { return repoRoot, nil }

			// cwd is the repo root so the explicit-repo-path form would
			// resolve -- not refuse -- without C2's wiring.
			if _, err := ResolveScene(deps, repoRoot, id, form.flag, arg); !errors.Is(err, ErrUsage) {
				t.Errorf("ResolveScene(%q) = %v, want ErrUsage", form.name, err)
			}
		})
	}

	// And the reverse: the repository root under the live scope.
	t.Run("repository root under the live scope", func(t *testing.T) {
		liveRoot := boardStateRoot(t)
		deps := testDeps(t)
		id := "mm_aaaaaaaaaaaa"
		liveDir := filepath.Join(liveRoot, id)
		repoRoot := filepath.Join(liveDir, "repo")
		if err := os.MkdirAll(liveDir, 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		deps.RepoRoot = func(string) (string, error) { return repoRoot, nil }

		if _, err := ResolveScene(deps, repoRoot, id, "", ""); !errors.Is(err, ErrUsage) {
			t.Errorf("ResolveScene(repo under live) = %v, want ErrUsage", err)
		}
	})
}

func TestBoardPathAndFlagAreExclusive(t *testing.T) {
	boardStateRoot(t)
	deps := testDeps(t)
	if _, err := ResolveScene(deps, t.TempDir(), "", "notes", "docs/boards/x.excalidraw"); !errors.Is(err, ErrUsage) {
		t.Errorf("ResolveScene(path and --board) = %v, want ErrUsage", err)
	}
}

func TestBoardDisagreeingMastermindRefused(t *testing.T) {
	liveRoot := boardStateRoot(t)
	deps := testDeps(t)
	liveDir := filepath.Join(liveRoot, "mm_aaaaaaaaaaaa")
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	scene := filepath.Join(liveDir, "board.excalidraw")
	if _, err := ResolveScene(deps, t.TempDir(), "mm_bbbbbbbbbbbb", "", scene); !errors.Is(err, ErrUsage) {
		t.Errorf("ResolveScene(disagreeing ref) = %v, want ErrUsage", err)
	}
}

func TestBoardUnknownRefIsNotFound(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	deps := testDeps(t)
	seedBoardRecords(t, stateHome, boardRecord("mm_aaaaaaaaaaaa", "alpha"))
	if _, err := ResolveScene(deps, t.TempDir(), "nosuch", "", ""); !errors.Is(err, mastermind.ErrNotFound) {
		t.Errorf("ResolveScene(unknown ref) = %v, want mastermind.ErrNotFound", err)
	}
}

func TestBoardTwoRecordsAreListed(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	deps := testDeps(t)
	seedBoardRecords(t, stateHome,
		boardRecord("mm_aaaaaaaaaaaa", "alpha"),
		boardRecord("mm_bbbbbbbbbbbb", "beta"),
	)
	_, err := ResolveScene(deps, t.TempDir(), "", "", "")
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("ResolveScene(two records) = %v, want ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "beta") {
		t.Errorf("refusal %q does not list both records", err)
	}
}

func TestBoardZeroRecordsNamesInit(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	deps := testDeps(t)
	seedBoardRecords(t, stateHome) // an existing database with no records
	_, err := ResolveScene(deps, t.TempDir(), "", "", "")
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("ResolveScene(zero records) = %v, want ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "mastermind init") {
		t.Errorf("refusal %q does not name relevo mastermind init", err)
	}
}

func TestBoardNoDatabaseNameRefRefuses(t *testing.T) {
	boardStateRoot(t) // no relevo.db is created
	deps := testDeps(t)
	for _, ref := range []string{"alpha", ""} {
		_, err := ResolveScene(deps, t.TempDir(), ref, "", "")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("ResolveScene(%q, no database) = %v, want ErrNotFound", ref, err)
			continue
		}
		if !strings.Contains(err.Error(), "--mastermind") {
			t.Errorf("refusal %q does not name --mastermind", err)
		}
	}
}

func TestBoardIDRefNeedsNoDatabase(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	deps := testDeps(t)
	liveRoot := filepath.Join(stateHome, "relevo", "boards")

	res, err := ResolveScene(deps, t.TempDir(), "mm_bbbbbbbbbbbb", "", "")
	if err != nil {
		t.Fatalf("ResolveScene(id ref): %v", err)
	}
	if res.Scope != ScopeLive || res.LiveDir != filepath.Join(liveRoot, "mm_bbbbbbbbbbbb") {
		t.Errorf("ResolveScene(id ref) = %+v", res)
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
	if err := WritePointer(liveDir, scene); err != nil {
		t.Fatalf("WritePointer: %v", err)
	}
	info := ServerInfo{Scene: scene, URL: url, Port: 1, PID: os.Getpid(), StartedAt: 4242}
	if err := WriteServerInfo(liveDir, info); err != nil {
		t.Fatalf("WriteServerInfo: %v", err)
	}
	return liveDir
}

// liveProcStart answers this process's start with 4242 and refuses every other
// pid, so a live server.json the test wrote reads live and any other pid reads
// dead.
func liveProcStart(pid int) (int64, error) {
	if pid == os.Getpid() {
		return 4242, nil
	}
	return 0, errors.New("gone")
}

func TestBoardURLAlive(t *testing.T) {
	liveRoot := boardStateRoot(t)
	deps := testDeps(t)
	deps.ProcStart = liveProcStart
	id := "mm_aaaaaaaaaaaa"
	url := "http://127.0.0.1:41234/#t=deadbeef"
	seedLiveBoard(t, liveRoot, id, "board", url)

	stdout, stderr, err := captureOutput(t, func() error { return URL(deps, id, "") })
	if err != nil {
		t.Fatalf("board url: %v (stderr %s)", err, stderr)
	}
	if string(stdout) != url+"\n" {
		t.Errorf("stdout = %q, want exactly %q", stdout, url+"\n")
	}
}

func TestBoardURLBoardFlag(t *testing.T) {
	liveRoot := boardStateRoot(t)
	deps := testDeps(t)
	deps.ProcStart = liveProcStart
	id := "mm_aaaaaaaaaaaa"
	url := "http://127.0.0.1:40001/#t=cafe"
	seedLiveBoard(t, liveRoot, id, "notes", url)

	stdout, stderr, err := captureOutput(t, func() error { return URL(deps, id, "notes") })
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
			return DefaultBoard
		}},
		{"pointer without server", func(t *testing.T, liveRoot, id string) string {
			if err := WritePointer(filepath.Join(liveRoot, id), "board"); err != nil {
				t.Fatalf("WritePointer: %v", err)
			}
			return "board"
		}},
		{"dead pid", func(t *testing.T, liveRoot, id string) string {
			liveDir := filepath.Join(liveRoot, id)
			if err := WritePointer(liveDir, "board"); err != nil {
				t.Fatalf("WritePointer: %v", err)
			}
			info := ServerInfo{Scene: "board", URL: "http://127.0.0.1:1/#t=x", Port: 1, PID: 1 << 30, StartedAt: 123}
			if err := WriteServerInfo(liveDir, info); err != nil {
				t.Fatalf("WriteServerInfo: %v", err)
			}
			return "board"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			liveRoot := boardStateRoot(t)
			deps := testDeps(t)
			deps.ProcStart = liveProcStart
			id := "mm_aaaaaaaaaaaa"
			scene := tc.setup(t, liveRoot, id)

			stdout, _, err := captureOutput(t, func() error { return URL(deps, id, "") })
			if !errors.Is(err, ErrNotAvailable) {
				t.Fatalf("board url = %v, want ErrNotAvailable", err)
			}
			if len(stdout) != 0 {
				t.Errorf("stdout = %q, want it empty", stdout)
			}
			if !strings.Contains(err.Error(), scene) || !strings.Contains(err.Error(), id) {
				t.Errorf("refusal %q must name scene %q and MasterMind %s", err, scene, id)
			}
		})
	}
}

func TestBoardURLDoesNotWritePointer(t *testing.T) {
	liveRoot := boardStateRoot(t)
	deps := testDeps(t)
	deps.ProcStart = liveProcStart
	id := "mm_aaaaaaaaaaaa"

	_, _, err := captureOutput(t, func() error { return URL(deps, id, "") })
	if !errors.Is(err, ErrNotAvailable) {
		t.Fatalf("board url = %v, want ErrNotAvailable", err)
	}
	if _, statErr := os.Stat(filepath.Join(liveRoot, id, "current")); !os.IsNotExist(statErr) {
		t.Errorf("board url wrote the pointer: stat error = %v, want not-exist", statErr)
	}
}

// TestBoardServerInfoComposition pins the advertisement a live board composes:
// nil for a repo board, the five fields for a live one (S6).
func TestBoardServerInfoComposition(t *testing.T) {
	if got := Advertise("board", "http://127.0.0.1:41234/#t=x", 7, 1700000000, ""); got != nil {
		t.Errorf("repo scope = %+v, want nil", got)
	}
	got := Advertise("board", "http://127.0.0.1:41234/#t=x", 7, 1700000000, "/live")
	want := ServerInfo{Scene: "board", URL: "http://127.0.0.1:41234/#t=x", Port: 41234, PID: 7, StartedAt: 1700000000}
	if got == nil || *got != want {
		t.Errorf("live scope = %+v, want %+v", got, want)
	}
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
	boardStateRoot(t)
	deps := testDeps(t)
	root := withTempRepoRoot(t, &deps)
	seedBoardScene(t, root, "board.excalidraw",
		`{"type":"excalidraw","elements":[`+
			`{"id":"a","type":"text","x":10,"y":20,"text":"hello"},`+
			`{"id":"r","type":"rectangle","x":0,"y":0},`+
			`{"id":"b","type":"text","x":12.5,"y":0,"text":"two"},`+
			`{"id":"c","type":"text","x":1,"y":1,"text":"gone","isDeleted":true}]}`)

	stdout, stderr, err := captureOutput(t, func() error {
		return Text(deps, root, "board.excalidraw", true)
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
	boardStateRoot(t)
	deps := testDeps(t)
	root := withTempRepoRoot(t, &deps)
	seedBoardScene(t, root, "board.excalidraw", `{"type":"excalidraw","elements":[]}`)

	stdout, stderr, err := captureOutput(t, func() error {
		return Text(deps, root, "board.excalidraw", true)
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
	boardStateRoot(t)
	deps := testDeps(t)
	root := withTempRepoRoot(t, &deps)
	seedBoardScene(t, root, "board.excalidraw",
		`{"type":"excalidraw","elements":[`+
			`{"id":"a","type":"text","x":10,"y":20,"text":"hello"},`+
			`{"id":"b","type":"text","x":12.5,"y":0,"text":"two"}]}`)

	stdout, stderr, err := captureOutput(t, func() error {
		return Text(deps, root, "board.excalidraw", false)
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
		return Text(deps, root, "empty.excalidraw", false)
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
	boardStateRoot(t)
	deps := testDeps(t)
	root := withTempRepoRoot(t, &deps)
	path := seedBoardScene(t, root, "board.excalidraw", `{"type":"excalidraw","elements":[]}`)

	stdout, stderr, err := captureOutput(t, func() error {
		return AnnotateScene(deps, root, "board.excalidraw", AnnotateRequest{Text: "hello", HasText: true})
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
	boardStateRoot(t)
	deps := testDeps(t)
	deps.GitConfig = func(string, string) (string, error) { return "blueprint", nil }

	root := withTempRepoRoot(t, &deps)
	path := seedBoardScene(t, root, "board.excalidraw", `{"type":"excalidraw","elements":[]}`)

	if _, _, err := captureOutput(t, func() error {
		return AnnotateScene(deps, root, "board.excalidraw", AnnotateRequest{Text: "hi", HasText: true})
	}); err != nil {
		t.Fatalf("board annotate: %v", err)
	}
	if got := readBoardElements(t, path)[0]["strokeColor"]; got != "#dbe4f0" {
		t.Errorf("strokeColor = %v, want the blueprint ink", got)
	}
}

// TestBoardTextRefusals pins text's usage and refused outcomes.
func TestBoardTextRefusals(t *testing.T) {
	boardStateRoot(t)
	deps := testDeps(t)
	root := withTempRepoRoot(t, &deps)
	seedBoardScene(t, root, "bad.excalidraw", `{"type":"other","elements":[]}`)

	cases := []struct {
		name string
		arg  string
		want error
		msg  string
	}{
		{"bad extension", "notes.txt", ErrUsage, "excalidraw"},
		{"missing scene", "missing.excalidraw", ErrNotFound, "no scene at"},
		{"invalid scene", "bad.excalidraw", ErrInvalid, "scene type is not"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := captureOutput(t, func() error { return Text(deps, root, tc.arg, false) })
			if !errors.Is(err, tc.want) {
				t.Fatalf("Text(%q) = %v, want %v", tc.arg, err, tc.want)
			}
			if tc.msg != "" && !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("message = %q, want it to contain %q", err.Error(), tc.msg)
			}
		})
	}

	t.Run("outside repo", func(t *testing.T) {
		deps := testDeps(t)
		_, _, err := captureOutput(t, func() error { return Text(deps, t.TempDir(), "a.excalidraw", false) })
		if !errors.Is(err, ErrUsage) {
			t.Errorf("Text(outside repo) = %v, want ErrUsage", err)
		}
	})
}

// TestBoardAnnotateRefusals pins annotate's usage and refused outcomes, and
// that every usage check runs before any repository or file work.
func TestBoardAnnotateRefusals(t *testing.T) {
	boardStateRoot(t)
	deps := testDeps(t)
	root := withTempRepoRoot(t, &deps)
	seedBoardScene(t, root, "bad.excalidraw", `{"type":"other","elements":[]}`)

	cases := []struct {
		name string
		arg  string
		req  AnnotateRequest
		want error
		msg  string
	}{
		{"text absent", "a.excalidraw", AnnotateRequest{}, ErrUsage, "--text is required"},
		{"text empty", "a.excalidraw", AnnotateRequest{Text: "", HasText: true}, ErrUsage, "--text must not be empty"},
		{"lone x", "a.excalidraw", AnnotateRequest{Text: "hi", HasText: true, HasX: true, X: 1}, ErrUsage, "--x and --y come together"},
		{"lone y", "a.excalidraw", AnnotateRequest{Text: "hi", HasText: true, HasY: true, Y: 1}, ErrUsage, "--x and --y come together"},
		{"non-finite x", "a.excalidraw", AnnotateRequest{Text: "hi", HasText: true, HasX: true, HasY: true, Y: 1, X: math.NaN()}, ErrUsage, "--x must be a finite number"},
		{"non-finite y", "a.excalidraw", AnnotateRequest{Text: "hi", HasText: true, HasX: true, HasY: true, X: 1, Y: math.Inf(1)}, ErrUsage, "--y must be a finite number"},
		{"missing scene", "missing.excalidraw", AnnotateRequest{Text: "hi", HasText: true}, ErrNotFound, "no scene at"},
		{"invalid scene", "bad.excalidraw", AnnotateRequest{Text: "hi", HasText: true}, ErrInvalid, "scene type is not"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := captureOutput(t, func() error { return AnnotateScene(deps, root, tc.arg, tc.req) })
			if !errors.Is(err, tc.want) {
				t.Fatalf("AnnotateScene(%q) = %v, want %v", tc.arg, err, tc.want)
			}
			if tc.msg != "" && !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("message = %q, want it to contain %q", err.Error(), tc.msg)
			}
		})
	}
}

// TestBoardURLNamesTheOwner pins the printed URL: a live board carries one
// owner segment (so a copied URL says whose board it is), a repo board carries
// none, and the per-run token always stays in the fragment.
func TestBoardURLNamesTheOwner(t *testing.T) {
	if got, want := FormatURL("127.0.0.1:9", "opencode-120", "abc"), "http://127.0.0.1:9/opencode-120/#t=abc"; got != want {
		t.Errorf("FormatURL(owner) = %q, want %q", got, want)
	}
	if got, want := FormatURL("127.0.0.1:9", "", "abc"), "http://127.0.0.1:9/#t=abc"; got != want {
		t.Errorf("FormatURL(repo) = %q, want %q", got, want)
	}
	if got, want := FormatURL("127.0.0.1:9", "a b", "abc"), "http://127.0.0.1:9/a%20b/#t=abc"; got != want {
		t.Errorf("FormatURL(escaped) = %q, want %q", got, want)
	}
}
