package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/doctor"
)

// accountEnv is a doctor.Env whose only facts are the files and sqlite3
// answers a test sets. No harness binary is ever spawned: the account checks
// read through this injected FS and command runner alone.
type accountEnv struct {
	stubDoctorEnv
	files map[string]bool
	cmds  map[string]string
}

func (e *accountEnv) Stat(path string) error {
	if e.files[path] {
		return nil
	}
	return os.ErrNotExist
}

func (e *accountEnv) HomePath(rel string) (string, error) {
	return filepath.Join("/home/u", rel), nil
}

func (e *accountEnv) Command(ctx context.Context, bin string, args ...string) ([]byte, error) {
	if out, ok := e.cmds[bin+" "+strings.Join(args, " ")]; ok {
		return []byte(out), nil
	}
	return nil, os.ErrNotExist
}

// TestAccountChecks is doctor row, tested as a pure function over an
// injected FS and command runner: each account says whether its home holds a
// login, and an opencode account reads its credential row through
// sqlite3 -readonly. Nothing is spawned.
func TestAccountChecks(t *testing.T) {
	dbPath := filepath.Join("/home/u", opencodeDBRel)
	env := &accountEnv{
		files: map[string]bool{
			"/home/u/.claude-work":                   true,
			"/home/u/.claude-work/.credentials.json": true,
			"/home/u/.codex":                         true,
			dbPath:                                   true,
		},
		cmds: map[string]string{
			"sqlite3 -readonly " + dbPath + " select count(*) from credential where integration_id = 'cline-pass' and label = 'ClinePass'": "1\n",
			"sqlite3 -readonly " + dbPath + " select count(*) from credential where integration_id = 'cline-pass' and label = 'Gone'":      "0\n",
		},
	}
	accounts := account.Set{
		{Name: "work", Harness: account.Claude, Groups: []string{"anthropic"}, ConfigDir: "/home/u/.claude-work"},
		{Name: "cx", Harness: account.Codex, Groups: []string{"openai"}, Home: "/home/u/.codex"},
		{Name: "cp1", Harness: account.OpenCode, Groups: []string{"cline-pass"}, Integration: "cline-pass", Label: "ClinePass"},
		{Name: "cp2", Harness: account.OpenCode, Groups: []string{"cline-pass"}, Integration: "cline-pass", Label: "Gone"},
	}

	checks := accountChecks(context.Background(), accounts, env)
	if len(checks) != len(accounts) {
		t.Fatalf("checks = %d, want %d", len(checks), len(accounts))
	}
	want := []struct {
		group, severity, detail string
	}{
		{"claude", "ok", "login in /home/u/.claude-work"},
		{"codex", "warn", "no login in /home/u/.codex"},
		{"opencode", "ok", "cline-pass/ClinePass credential row present"},
		{"opencode", "warn", "no cline-pass/Gone credential row"},
	}
	for i, w := range want {
		c := checks[i]
		if c.Group != w.group || c.Severity.String() != w.severity || !strings.Contains(c.Detail, w.detail) {
			t.Errorf("checks[%d] = %s %s %q, want %s %s containing %q",
				i, c.Group, c.Severity, c.Detail, w.group, w.severity, w.detail)
		}
	}
	if c := checks[1]; c.Fix == "" {
		t.Errorf("checks[1] warning must carry a login fix")
	}

	if got := accountChecks(context.Background(), nil, env); got != nil {
		t.Errorf("no accounts must build no checks, got %v", got)
	}
}

// TestAccountCheckProbeFailure: a database relevo cannot read is a probe
// failure, so the row says it could not be established rather than guessing.
func TestAccountCheckProbeFailure(t *testing.T) {
	env := &accountEnv{files: map[string]bool{filepath.Join("/home/u", opencodeDBRel): true}}
	accounts := account.Set{{Name: "cp1", Harness: account.OpenCode, Groups: []string{"cline-pass"}, Integration: "cline-pass", Label: "C1"}}

	c := accountCheck(context.Background(), accounts[0], env)
	if c.Severity != doctor.SevWarn || !c.ProbeFailed {
		t.Errorf("unreadable opencode.db = %+v, want a warn probe failure", c)
	}
}
