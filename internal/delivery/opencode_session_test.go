package delivery

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/mastermind"
)

// cliExec is usage.Exec over the real sqlite3 binary, so a test can query a
// fixture database the way the daemon does.
type cliExec struct{}

func (cliExec) Run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if err != nil {
		return nil, err
	}
	return out, nil
}

// sqliteFixture builds a fixture database under t.TempDir() with the sqlite3
// binary: no test may touch the real ~/.local/share/opencode/opencode.db.
func sqliteFixture(t *testing.T, stmts ...string) string {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skipf("sqlite3 not on PATH: %v", err)
	}
	path := filepath.Join(t.TempDir(), "opencode.db")
	for _, stmt := range stmts {
		out, err := exec.Command("sqlite3", path, stmt).CombinedOutput()
		if err != nil {
			t.Fatalf("sqlite3 %q: %v: %s", stmt, err, out)
		}
	}
	return path
}

// fakeFinderExec answers Find's shell-outs by position: tables answers the
// sqlite_master query, out answers every other query, err fails every query.
type fakeFinderExec struct {
	tables []byte
	out    []byte
	err    error
}

func (f *fakeFinderExec) Run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(args) > 0 && strings.Contains(args[len(args)-1], "sqlite_master") {
		if f.tables != nil {
			return f.tables, nil
		}
		return f.out, nil
	}
	return f.out, nil
}

func TestOpencodeSessionFinder(t *testing.T) {
	t.Parallel()

	now := time.Now()

	t.Run("JSON array with time_updated in ms and null parent_id/time_archived -> right id", func(t *testing.T) {
		jsonOut := `[
			{
				"id": "ses_target",
				"directory": "/path/to/project",
				"parent_id": null,
				"title": "My Session",
				"updated": ` + strconv.FormatInt(now.UnixMilli(), 10) + `,
				"time_archived": null
			}
		]`
		finder := OpencodeSessionFinder{
			Exec: &fakeFinderExec{
				tables: []byte(`[{"name":"session"}]`),
				out:    []byte(jsonOut),
			},
			DBPath: "/dummy/opencode.db",
		}
		id, err := finder.Find("/path/to/project/subdir", now)
		if err != nil {
			t.Fatalf("Find failed: %v", err)
		}
		if id != "ses_target" {
			t.Errorf("Find = %q, want ses_target", id)
		}
	})

	t.Run("empty output -> ErrNoOpencodeSession", func(t *testing.T) {
		finder := OpencodeSessionFinder{
			Exec:   &fakeFinderExec{out: []byte("")},
			DBPath: "/dummy/opencode.db",
		}
		_, err := finder.Find("/path/to/project", now)
		if !errors.Is(err, mastermind.ErrNoOpencodeSession) {
			t.Errorf("Find on empty output: err = %v, want errors.Is ErrNoOpencodeSession", err)
		}
	})

	t.Run("exec error -> ErrNoOpencodeSession", func(t *testing.T) {
		finder := OpencodeSessionFinder{
			Exec:   &fakeFinderExec{err: errors.New("command failed")},
			DBPath: "/dummy/opencode.db",
		}
		_, err := finder.Find("/path/to/project", now)
		if !errors.Is(err, mastermind.ErrNoOpencodeSession) {
			t.Errorf("Find on exec error: err = %v, want errors.Is ErrNoOpencodeSession", err)
		}
	})

	t.Run("decode error -> ErrNoOpencodeSession", func(t *testing.T) {
		finder := OpencodeSessionFinder{
			Exec:   &fakeFinderExec{out: []byte("invalid json")},
			DBPath: "/dummy/opencode.db",
		}
		_, err := finder.Find("/path/to/project", now)
		if !errors.Is(err, mastermind.ErrNoOpencodeSession) {
			t.Errorf("Find on decode error: err = %v, want errors.Is ErrNoOpencodeSession", err)
		}
	})
}

// TestOpencodeSessionDirectory pins the session-id lookup the guide verb
// reads: the recorded directory for a known session, a miss for an unknown id,
// and ErrNoOpencodeSession on a failed read.
func TestOpencodeSessionDirectory(t *testing.T) {
	t.Parallel()

	jsonOut := `[
		{"id": "ses_one", "directory": "/path/one", "parent_id": null, "title": "one", "updated": 1, "time_archived": null},
		{"id": "ses_two", "directory": "/path/two", "parent_id": null, "title": "two", "updated": 2, "time_archived": null}
	]`

	finder := OpencodeSessionFinder{
		Exec: &fakeFinderExec{
			tables: []byte(`[{"name":"session"}]`),
			out:    []byte(jsonOut),
		},
		DBPath: "/dummy/opencode.db",
	}
	if dir, err := finder.Directory("ses_two"); err != nil || dir != "/path/two" {
		t.Errorf("Directory(ses_two) = %q, %v, want /path/two", dir, err)
	}
	if _, err := finder.Directory("ses_missing"); !errors.Is(err, mastermind.ErrNoOpencodeSession) {
		t.Errorf("Directory(ses_missing) err = %v, want ErrNoOpencodeSession", err)
	}
	if _, err := finder.Directory(""); !errors.Is(err, mastermind.ErrNoOpencodeSession) {
		t.Errorf("Directory(\"\") err = %v, want ErrNoOpencodeSession", err)
	}

	failed := OpencodeSessionFinder{Exec: &fakeFinderExec{err: errors.New("command failed")}, DBPath: "/dummy/opencode.db"}
	if _, err := failed.Directory("ses_one"); !errors.Is(err, mastermind.ErrNoOpencodeSession) {
		t.Errorf("Directory on exec error: err = %v, want ErrNoOpencodeSession", err)
	}
}

// The OpenCode schema fragments the finder tests build their fixtures from.
const (
	v2SessionTable    = "create table session (id text, directory text, parent_id text, title text, time_updated integer, time_archived integer)"
	v2SessionV2Table  = "create table session_v2 (id text, directory text, parent_id text, title text, time_updated integer, time_idle integer, time_viewed integer, time_archived integer)"
	v2SessionMsgTable = "create table session_message (id text, session_id text, type text, seq integer, time_created integer, time_updated integer, data text)"
)

// v2Sessions returns the sessions a finder reads from db.
func v2Sessions(t *testing.T, db string) []mastermind.OpencodeSession {
	t.Helper()
	sessions, err := OpencodeSessionFinder{Exec: cliExec{}, DBPath: db}.sessions(context.Background())
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	return sessions
}

// TestOpencodeSessionFinderV2 covers the OpenCode 2.0.14 schema: v2
// sessions live in session_v2 and their activity time is the latest
// session_message row, not time_updated.
func TestOpencodeSessionFinderV2(t *testing.T) {
	t.Parallel()

	now := time.Now()
	recent := now.Add(-1 * time.Minute)
	legacyActive := now.Add(-5 * time.Minute)
	old := now.Add(-2 * time.Hour)

	t.Run("both tables: v2 activity is its latest message time; both appear; no duplicate id", func(t *testing.T) {
		db := sqliteFixture(t,
			v2SessionTable,
			v2SessionV2Table,
			v2SessionMsgTable,
			"insert into session values ('ses_v2', '/a/b', null, 'legacy duplicate', "+strconv.FormatInt(old.UnixMilli(), 10)+", null)",
			"insert into session values ('ses_legacy', '/a/b', null, 'legacy', "+strconv.FormatInt(legacyActive.UnixMilli(), 10)+", null)",
			"insert into session_v2 values ('ses_v2', '/a/b', null, 'v2', "+strconv.FormatInt(old.UnixMilli(), 10)+", null, null, null)",
			"insert into session_message values ('m1', 'ses_v2', 'user', 0, "+strconv.FormatInt(recent.UnixMilli(), 10)+", "+strconv.FormatInt(recent.UnixMilli(), 10)+", '{}')",
		)
		checkV2UnionSessions(t, v2Sessions(t, db), recent)

		finder := OpencodeSessionFinder{Exec: cliExec{}, DBPath: db}
		id, err := finder.Find("/a/b/subdir", now)
		if err != nil {
			t.Fatalf("Find: %v", err)
		}
		if id != "ses_v2" {
			t.Errorf("Find = %q, want ses_v2 (the most recently active)", id)
		}
	})

	t.Run("only session_v2 works", func(t *testing.T) {
		db := sqliteFixture(t,
			v2SessionV2Table,
			v2SessionMsgTable,
			"insert into session_v2 values ('ses_v2', '/a/b', null, 'v2', "+strconv.FormatInt(old.UnixMilli(), 10)+", "+strconv.FormatInt(recent.UnixMilli(), 10)+", null, null)",
		)
		sessions := v2Sessions(t, db)
		if len(sessions) != 1 || sessions[0].ID != "ses_v2" {
			t.Fatalf("sessions = %+v, want just ses_v2", sessions)
		}
		if !sessions[0].Updated.Equal(time.UnixMilli(recent.UnixMilli())) {
			t.Errorf("Updated = %v, want time_idle when it is the greatest", sessions[0].Updated)
		}
	})

	t.Run("only the legacy table works", func(t *testing.T) {
		db := sqliteFixture(t,
			v2SessionTable,
			"insert into session values ('ses_legacy', '/a/b', null, 'legacy', "+strconv.FormatInt(recent.UnixMilli(), 10)+", null)",
		)
		sessions := v2Sessions(t, db)
		if len(sessions) != 1 || sessions[0].ID != "ses_legacy" {
			t.Fatalf("sessions = %+v, want just ses_legacy", sessions)
		}
		if !sessions[0].Updated.Equal(time.UnixMilli(recent.UnixMilli())) {
			t.Errorf("Updated = %v, want time_updated", sessions[0].Updated)
		}
	})
}

// checkV2UnionSessions asserts the union's two rows when both tables exist:
// the legacy-only id survives and the v2 row's activity is its latest message
// time.
func checkV2UnionSessions(t *testing.T, sessions []mastermind.OpencodeSession, recent time.Time) {
	t.Helper()
	if len(sessions) != 2 {
		t.Fatalf("sessions = %+v, want exactly 2 (the legacy id in session_v2 must not duplicate)", sessions)
	}
	byID := map[string]mastermind.OpencodeSession{}
	for _, s := range sessions {
		byID[s.ID] = s
	}
	if _, ok := byID["ses_legacy"]; !ok {
		t.Errorf("sessions = %+v, want the legacy-only session present", sessions)
	}
	v2, ok := byID["ses_v2"]
	if !ok {
		t.Fatalf("sessions = %+v, want the v2 session present", sessions)
	}
	if want := time.UnixMilli(recent.UnixMilli()); !v2.Updated.Equal(want) {
		t.Errorf("v2 Updated = %v, want the latest message time %v", v2.Updated, want)
	}
	if v2.Title != "v2" {
		t.Errorf("v2 title = %q, want the session_v2 row's title", v2.Title)
	}
}
