package board

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/board/theme"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
)

// testDeps returns Deps with hermetic defaults: no repository, the real
// clock, the isolated live root, a real read-only opener, and a registry
// lookup without CLI codes. Tests override the fields they pin.
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
		ThemeSets: func(string) (string, map[string]theme.Theme, error) {
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

// TestBoardRefusesBadPath: a path that is not a board.html path is usage.
func TestBoardRefusesBadPath(t *testing.T) {
	boardStateRoot(t)
	deps := testDeps(t)
	dir := withTempRepoRoot(t, &deps)

	if err := Run(deps, dir, "", "", "", "notes.txt", false, nil); !errors.Is(err, ErrUsage) {
		t.Errorf("Run(notes.txt) = %v, want ErrUsage", err)
	}
}

func TestBoardBareAndFlagAreLiveWithoutARepo(t *testing.T) {
	liveRoot := boardStateRoot(t)
	deps := testDeps(t)
	seedBoardRecords(t, filepath.Dir(filepath.Dir(liveRoot)), boardRecord("mm_aaaaaaaaaaaa", "alpha"))
	cwd := t.TempDir()

	res, err := ResolveSceneHTML(deps, cwd, "", "", "")
	if err != nil {
		t.Fatalf("ResolveSceneHTML(bare): %v", err)
	}
	if res.Scope != ScopeLive || res.Scene != DefaultBoard || res.FromPointer {
		t.Errorf("bare board = %+v, want live default", res)
	}
	if want := filepath.Join(liveRoot, "mm_aaaaaaaaaaaa"); res.LiveDir != want {
		t.Errorf("live dir = %q, want %q", res.LiveDir, want)
	}

	res, err = ResolveSceneHTML(deps, cwd, "", "notes", "")
	if err != nil {
		t.Fatalf("ResolveSceneHTML(--board): %v", err)
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
		{"explicit repo path", "", "docs/boards/api/board.html"},
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
				arg = filepath.Join(liveDir, "board", "board.html")
			}

			deps.RepoRoot = func(string) (string, error) { return repoRoot, nil }

			// cwd is the repo root so the explicit-repo-path form would
			// resolve -- not refuse -- without C2's wiring.
			if _, err := ResolveSceneHTML(deps, repoRoot, id, form.flag, arg); !errors.Is(err, ErrUsage) {
				t.Errorf("ResolveSceneHTML(%q) = %v, want ErrUsage", form.name, err)
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

		if _, err := ResolveSceneHTML(deps, repoRoot, id, "", ""); !errors.Is(err, ErrUsage) {
			t.Errorf("ResolveSceneHTML(repo under live) = %v, want ErrUsage", err)
		}
	})
}

func TestBoardDisagreeingMastermindRefused(t *testing.T) {
	liveRoot := boardStateRoot(t)
	deps := testDeps(t)
	liveDir := filepath.Join(liveRoot, "mm_aaaaaaaaaaaa")
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	scene := filepath.Join(liveDir, "board", "board.html")
	if _, err := ResolveSceneHTML(deps, t.TempDir(), "mm_bbbbbbbbbbbb", "", scene); !errors.Is(err, ErrUsage) {
		t.Errorf("ResolveSceneHTML(disagreeing ref) = %v, want ErrUsage", err)
	}
}

func TestBoardUnknownRefIsNotFound(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	deps := testDeps(t)
	seedBoardRecords(t, stateHome, boardRecord("mm_aaaaaaaaaaaa", "alpha"))
	if _, err := ResolveSceneHTML(deps, t.TempDir(), "nosuch", "", ""); !errors.Is(err, mastermind.ErrNotFound) {
		t.Errorf("ResolveSceneHTML(unknown ref) = %v, want mastermind.ErrNotFound", err)
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
	_, err := ResolveSceneHTML(deps, t.TempDir(), "", "", "")
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("ResolveSceneHTML(two records) = %v, want ErrUsage", err)
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
	_, err := ResolveSceneHTML(deps, t.TempDir(), "", "", "")
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("ResolveSceneHTML(zero records) = %v, want ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "mastermind init") {
		t.Errorf("refusal %q does not name relevo mastermind init", err)
	}
}

func TestBoardNoDatabaseNameRefRefuses(t *testing.T) {
	boardStateRoot(t) // no relevo.db is created
	deps := testDeps(t)
	for _, ref := range []string{"alpha", ""} {
		_, err := ResolveSceneHTML(deps, t.TempDir(), ref, "", "")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("ResolveSceneHTML(%q, no database) = %v, want ErrNotFound", ref, err)
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

	res, err := ResolveSceneHTML(deps, t.TempDir(), "mm_bbbbbbbbbbbb", "", "")
	if err != nil {
		t.Fatalf("ResolveSceneHTML(id ref): %v", err)
	}
	if res.Scope != ScopeLive || res.LiveDir != filepath.Join(liveRoot, "mm_bbbbbbbbbbbb") {
		t.Errorf("ResolveSceneHTML(id ref) = %+v", res)
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
