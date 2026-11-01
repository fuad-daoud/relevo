package main

import (
	"os"
	"path/filepath"
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

// TestBoardPromoteIsAPeekVerb: promote is a copy, not a read of the machine, so
// it installs no route and skips the agy environment capture like every other
// board subverb.
func TestBoardPromoteIsAPeekVerb(t *testing.T) {
	args := []string{"board", "promote"}
	if !isPeekArgs(args) {
		t.Error("isPeekArgs(board promote) = false, want true")
	}
	if mode, _, _ := routeForArgs(args); mode != routeNone {
		t.Errorf("routeForArgs(board promote) = %v, want none", mode)
	}
	if peek := installRouteForArgs(args); !peek {
		t.Error("installRouteForArgs(board promote) = false, want the peek that skips capture")
	}
}

// TestBoardPromoteRefusesAScenePath pins the dispatch: promote takes no scene
// path, so a path is usage before any repository or file work.
func TestBoardPromoteRefusesAScenePath(t *testing.T) {
	t.Chdir(t.TempDir())
	err := cmdBoardPromote([]string{"docs/boards/api/board.html"})
	_ = requireCLIError(t, err, codeUsage, "relevo help")
}

// TestBoardPromoteRefusesOutsideARepository: promoting lands in the repo, so
// outside one there is nowhere to put it.
func TestBoardPromoteRefusesOutsideARepository(t *testing.T) {
	t.Chdir(t.TempDir()) // no git repository here
	_ = requireCLIError(t, cmdBoardPromote([]string{"--mastermind", "alpha"}), codeUsage, "")
}

// seedDispatchRecords creates <stateHome>/relevo/relevo.db with recs, then
// closes it, so a later read-only open finds a complete file.
func seedDispatchRecords(t *testing.T, stateHome string, recs ...mastermind.Record) {
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

// dispatchRecord is one valid record for the resolution tests.
func dispatchRecord(id, name string) mastermind.Record {
	return mastermind.Record{
		ID: id, Name: name, HarnessKind: "claude", SessionID: "sess-" + name,
		CWD: filepath.Join("/tmp", "relevo-cwd-"+name),
	}
}

// isolateDispatchState isolates the whole XDG state and config home, so a test
// never reads the machine's own state root.
func isolateDispatchState(t *testing.T) string {
	t.Helper()
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(stateHome, "config"))
	t.Setenv("HOME", stateHome)
	return stateHome
}

// TestBoardResolutionNeverStartsTheDaemon is R5's proof: resolving a name
// against a seeded database starts nothing, binds no socket and writes nothing.
func TestBoardResolutionNeverStartsTheDaemon(t *testing.T) {
	stateHome := isolateDispatchState(t)
	seedDispatchRecords(t, stateHome, dispatchRecord("mm_aaaaaaaaaaaa", "alpha"))

	var calls int32
	daemonStarter = func() error { atomic.AddInt32(&calls, 1); return nil }
	t.Cleanup(func() { daemonStarter = startDaemon })

	res, err := board.ResolveScene(boardDeps(), t.TempDir(), "alpha", "", "")
	if err != nil {
		t.Fatalf("ResolveScene(name): %v", boardRefusal(err))
	}
	if res.Scope != board.ScopeLive || res.Scene != board.DefaultBoard {
		t.Errorf("ResolveScene(name) = %+v, want the live default", res)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("resolution started the daemon %d times, want 0", got)
	}
	if _, err := os.Stat(filepath.Join(stateHome, "relevo", "relevo.sock")); !os.IsNotExist(err) {
		t.Errorf("resolution created a socket: stat error = %v, want not-exist", err)
	}
}

// TestResolveBoardHTMLNeverStartsTheDaemon extends R5 to the HTML resolver:
// resolving a name against a seeded database starts nothing and writes nothing.
func TestResolveBoardHTMLNeverStartsTheDaemon(t *testing.T) {
	stateHome := isolateDispatchState(t)
	seedDispatchRecords(t, stateHome, dispatchRecord("mm_aaaaaaaaaaaa", "alpha"))

	var calls int32
	daemonStarter = func() error { atomic.AddInt32(&calls, 1); return nil }
	t.Cleanup(func() { daemonStarter = startDaemon })

	res, err := board.ResolveSceneHTML(boardDeps(), t.TempDir(), "alpha", "", "")
	if err != nil {
		t.Fatalf("ResolveSceneHTML(name): %v", boardRefusal(err))
	}
	if res.Scope != board.ScopeLive || res.Scene != board.DefaultBoard {
		t.Errorf("ResolveSceneHTML(name) = %+v, want the live default", res)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("resolution started the daemon %d times, want 0", got)
	}
	if _, err := os.Stat(filepath.Join(stateHome, "relevo", "relevo.sock")); !os.IsNotExist(err) {
		t.Errorf("resolution created a socket: stat error = %v, want not-exist", err)
	}
}
