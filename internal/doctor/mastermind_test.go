package doctor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/mastermind"
)

func writeDoctorFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestMusePluginRowsThroughFakeEnv pins that the muse rows read their facts
// through doctor.Env.Command: fakeEnv answers commandFn, and `muse` is never
// spawned. It covers the same states as the direct reader tests.
func TestMusePluginRowsThroughFakeEnv(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		err     error
		plugin  Severity
		hook    Severity
		version Severity
	}{
		{"installed enabled approved", museInstalledJSON("0.22.4"), nil, SevOK, SevOK, SevOK},
		{"not installed", museNotInstalledJSON(), museExitStatusError(t), SevFail, SevOK, SevOK},
		{"disabled", `{"active":false,"record":{"enabled":false,"version":"0.22.4"},"runtime_capabilities":[]}`, nil, SevFail, SevOK, SevOK},
		{"unreadable", "not json", nil, SevOK, SevOK, SevOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := &fakeEnv{commandFn: func(bin string, args ...string) ([]byte, error) {
				if bin != "muse" {
					t.Errorf("probe ran %q, want muse", bin)
				}
				return []byte(tc.out), tc.err
			}}
			in := MasterMindCheckInput{Muse: true, Running: "0.22.4", MuseInspect: env.Command}
			checks := Report{Checks: MasterMindChecks(in)}
			for name, want := range map[string]Severity{
				"plugin":         tc.plugin,
				"plugin hook":    tc.hook,
				"plugin version": tc.version,
			} {
				c := findCheck(checks, "", name)
				if c == nil || c.Severity != want {
					t.Errorf("%s row = %+v, want severity %v", name, c, want)
				}
			}
		})
	}
}

// museExitStatusError returns a real *exec.ExitError, the shape Env.Command
// answers a non-zero exit with.
func museExitStatusError(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 1").Run()
	if err == nil {
		t.Fatal("sh -c 'exit 1' must fail")
	}
	return err
}

func TestDoctorPluginRow(t *testing.T) {
	enabled := `{"enabledPlugins":{"relevo@relevo":true}}`

	t.Run("user settings enable it", func(t *testing.T) {
		home := t.TempDir()
		writeDoctorFile(t, filepath.Join(home, ".claude", "settings.json"), enabled)
		checks := MasterMindChecks(MasterMindCheckInput{Claude: true, Home: home, Repo: t.TempDir()})
		c := findCheck(Report{Checks: checks}, "", "plugin")
		if c == nil {
			t.Fatal("the plugin row must be present when a claude candidate exists")
		}
		if c.Severity != SevOK {
			t.Errorf("plugin row = %v (%s), want ok", c.Severity, c.Detail)
		}
	})

	t.Run("project settings enable it", func(t *testing.T) {
		repo := t.TempDir()
		writeDoctorFile(t, filepath.Join(repo, ".claude", "settings.json"), enabled)
		checks := MasterMindChecks(MasterMindCheckInput{Claude: true, Home: t.TempDir(), Repo: repo})
		c := findCheck(Report{Checks: checks}, "", "plugin")
		if c == nil || c.Severity != SevOK {
			t.Fatalf("plugin row = %+v, want ok from the project's settings", c)
		}
	})

	t.Run("neither settings file enables it", func(t *testing.T) {
		checks := MasterMindChecks(MasterMindCheckInput{Claude: true, Home: t.TempDir(), Repo: t.TempDir()})
		c := findCheck(Report{Checks: checks}, "", "plugin")
		if c == nil {
			t.Fatal("the plugin row must be present when a claude candidate exists")
		}
		if c.Severity != SevFail {
			t.Errorf("plugin row = %v (%s), want FAIL", c.Severity, c.Detail)
		}
		if c.Fix == "" {
			t.Error("the failing plugin row must carry the fix")
		}
	})

	t.Run("disabled is not enabled", func(t *testing.T) {
		home := t.TempDir()
		writeDoctorFile(t, filepath.Join(home, ".claude", "settings.json"), `{"enabledPlugins":{"relevo@relevo":false}}`)
		checks := MasterMindChecks(MasterMindCheckInput{Claude: true, Home: home, Repo: t.TempDir()})
		if c := findCheck(Report{Checks: checks}, "", "plugin"); c == nil || c.Severity != SevFail {
			t.Fatalf("plugin row = %+v, want FAIL for relevo@relevo: false", c)
		}
	})

	t.Run("no claude candidate", func(t *testing.T) {
		checks := MasterMindChecks(MasterMindCheckInput{Claude: false, Home: t.TempDir(), Repo: t.TempDir()})
		if c := findCheck(Report{Checks: checks}, "", "plugin"); c != nil {
			t.Errorf("no claude candidate must leave the plugin row out, got %+v", c)
		}
		if c := findCheck(Report{Checks: checks}, "", "plugin hook"); c != nil {
			t.Errorf("no claude candidate must leave the plugin hook row out, got %+v", c)
		}
	})
}

// TestDoctorPluginRowsReadClaudeConfigDir pins that a set CLAUDE_CONFIG_DIR,
// not ~/.claude, is where the plugin rows look: Claude Code keeps its
// settings and plugins there.
func TestDoctorPluginRowsReadClaudeConfigDir(t *testing.T) {
	home, claudeDir := t.TempDir(), t.TempDir()
	writeDoctorFile(t, filepath.Join(claudeDir, "settings.json"), `{"enabledPlugins":{"relevo@relevo":true}}`)
	writeDoctorFile(t, filepath.Join(claudeDir, "plugins", "installed_plugins.json"),
		`{"version":2,"plugins":{"relevo@relevo":[{"version":"0.8.0"}]}}`)
	writeDoctorFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"),
		`{"version":2,"plugins":{"relevo@relevo":[{"version":"0.1.0"}]}}`)

	checks := MasterMindChecks(MasterMindCheckInput{Claude: true, Home: home, ClaudeDir: claudeDir, Repo: t.TempDir(), Running: "v0.8.0"})
	if c := findCheck(Report{Checks: checks}, "", "plugin"); c == nil || c.Severity != SevOK {
		t.Errorf("plugin row = %+v, want ok from CLAUDE_CONFIG_DIR's settings", c)
	}
	if c := findCheck(Report{Checks: checks}, "", "plugin version"); c == nil || c.Detail != "plugin 0.8.0 matches relevo" {
		t.Errorf("plugin version row = %+v, want the version installed under CLAUDE_CONFIG_DIR", c)
	}
}

// TestDoctorPluginRowFix pins the failing row's fix: install when the plugin
// is missing, enable when it is installed but off.
func TestDoctorPluginRowFix(t *testing.T) {
	t.Run("not installed: the fix installs it", func(t *testing.T) {
		checks := MasterMindChecks(MasterMindCheckInput{Claude: true, Home: t.TempDir(), Repo: t.TempDir()})
		c := findCheck(Report{Checks: checks}, "", "plugin")
		if c == nil || c.Fix != PluginInstallCommand {
			t.Fatalf("plugin row = %+v, want the install command as its fix", c)
		}
	})

	t.Run("installed but disabled: the fix enables it", func(t *testing.T) {
		home := t.TempDir()
		writeDoctorFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"),
			`{"version":2,"plugins":{"relevo@relevo":[{"version":"0.17.3"}]}}`)
		writeDoctorFile(t, filepath.Join(home, ".claude", "settings.json"), `{"enabledPlugins":{"relevo@relevo":false}}`)
		checks := MasterMindChecks(MasterMindCheckInput{Claude: true, Home: home, Repo: t.TempDir()})
		c := findCheck(Report{Checks: checks}, "", "plugin")
		if c == nil || c.Severity != SevFail || !strings.Contains(c.Fix, "/plugin") {
			t.Fatalf("plugin row = %+v, want FAIL with the enable fix", c)
		}
	})
}

func TestDoctorMasterMindRow(t *testing.T) {
	rec := &mastermind.Record{ID: "pl_aaaaaaaabbbb", Name: "architect-1", HarnessKind: "claude", SessionID: "sess"}

	t.Run("resolved with a live claim", func(t *testing.T) {
		checks := MasterMindChecks(MasterMindCheckInput{Detected: true, Resolved: rec, MCPChild: true, ClaimLive: true})
		c := findCheck(Report{Checks: checks}, "", "MasterMind")
		if c == nil || c.Severity != SevOK {
			t.Fatalf("mastermind row = %+v, want ok", c)
		}
	})

	t.Run("resolve missed", func(t *testing.T) {
		checks := MasterMindChecks(MasterMindCheckInput{Detected: true})
		c := findCheck(Report{Checks: checks}, "", "MasterMind")
		if c == nil || c.Severity != SevFail {
			t.Fatalf("mastermind row = %+v, want FAIL when Resolve missed", c)
		}
	})

	t.Run("no live claim", func(t *testing.T) {
		checks := MasterMindChecks(MasterMindCheckInput{Detected: true, Resolved: rec, MCPChild: true})
		c := findCheck(Report{Checks: checks}, "", "MasterMind")
		if c == nil || c.Severity != SevInfo {
			t.Fatalf("mastermind row = %+v, want INFO without a live claim (tools mode is not a fault)", c)
		}
	})

	t.Run("not inside Claude Code", func(t *testing.T) {
		checks := MasterMindChecks(MasterMindCheckInput{Detected: false, Resolved: rec, ClaimLive: true})
		if c := findCheck(Report{Checks: checks}, "", "MasterMind"); c != nil {
			t.Errorf("the mastermind row exists only when Detect says claude, got %+v", c)
		}
	})

	t.Run("stale records are an info row", func(t *testing.T) {
		checks := MasterMindChecks(MasterMindCheckInput{Stale: []string{"old-1", "old-2"}})
		c := findCheck(Report{Checks: checks}, "", "MasterMinds")
		if c == nil || c.Severity != SevInfo {
			t.Fatalf("stale row = %+v, want an info row", c)
		}
		if c.Severity == SevFail || c.Severity == SevWarn {
			t.Error("the stale row must not fail or warn")
		}
	})

	t.Run("no stale records means no row", func(t *testing.T) {
		checks := MasterMindChecks(MasterMindCheckInput{})
		if c := findCheck(Report{Checks: checks}, "", "MasterMinds"); c != nil {
			t.Errorf("no stale records must leave the row out, got %+v", c)
		}
	})
}

func TestMasterMindRowNamesChat(t *testing.T) {
	rec := &mastermind.Record{ID: "pl_aaaaaaaabbbb", Name: "architect-1", HarnessKind: "claude", SessionID: "sess"}

	t.Run("chat follows the mastermind's name", func(t *testing.T) {
		checks := MasterMindChecks(MasterMindCheckInput{
			Detected: true, Resolved: rec, MCPChild: true, ClaimLive: false,
			Chat: "relevo-mastermind · https://claude.ai/code/session_01TEST",
		})
		c := findCheck(Report{Checks: checks}, "", "MasterMind")
		if c == nil {
			t.Fatal("the mastermind row is missing")
		}
		want := "MasterMind architect-1 (pl_aaaaaaaabbbb) · relevo-mastermind · https://claude.ai/code/session_01TEST: tools mode"
		if !strings.Contains(c.Detail, want) {
			t.Errorf("mastermind row detail %q does not contain %q", c.Detail, want)
		}
	})

	t.Run("empty chat is byte-identical", func(t *testing.T) {
		checks := MasterMindChecks(MasterMindCheckInput{Detected: true, Resolved: rec, MCPChild: true, ClaimLive: false})
		c := findCheck(Report{Checks: checks}, "", "MasterMind")
		if c == nil {
			t.Fatal("the mastermind row is missing")
		}
		want := "MasterMind architect-1 (pl_aaaaaaaabbbb): tools mode: reports arrive through the blocking wait tool. For push, the session's mod runs `relevo push`"
		if c.Detail != want {
			t.Errorf("mastermind row detail = %q, want %q", c.Detail, want)
		}
	})
}

// TestDoctorPluginHookRow pins the "not checked, never FAIL" rule.
func TestDoctorPluginHookRow(t *testing.T) {
	t.Run("no installed_plugins.json", func(t *testing.T) {
		checks := MasterMindChecks(MasterMindCheckInput{Claude: true, Home: t.TempDir(), Repo: t.TempDir()})
		c := findCheck(Report{Checks: checks}, "", "plugin hook")
		if c == nil || c.Severity != SevOK {
			t.Fatalf("plugin hook row = %+v, want ok (not checked)", c)
		}
	})

	t.Run("hook present", func(t *testing.T) {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "plugins", "cache", "relevo", "relevo", "0.1.0")
		writeDoctorFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"),
			`{"plugins":{"relevo@relevo":[{"installPath":"`+dir+`"}]}}`)
		writeDoctorFile(t, filepath.Join(dir, "hooks", "hooks.json"),
			`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"relevo mastermind init --hook claude"}]}]}}`)
		checks := MasterMindChecks(MasterMindCheckInput{Claude: true, Home: home, Repo: t.TempDir()})
		c := findCheck(Report{Checks: checks}, "", "plugin hook")
		if c == nil || c.Severity != SevOK {
			t.Fatalf("plugin hook row = %+v, want ok", c)
		}
	})

	t.Run("script hook present", func(t *testing.T) {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "plugins", "cache", "relevo", "relevo", "0.1.0")
		writeDoctorFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"),
			`{"plugins":{"relevo@relevo":[{"installPath":"`+dir+`"}]}}`)
		writeDoctorFile(t, filepath.Join(dir, "hooks", "hooks.json"),
			`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/scripts/mastermind-init.sh"}]}]}}`)
		checks := MasterMindChecks(MasterMindCheckInput{Claude: true, Home: home, Repo: t.TempDir()})
		c := findCheck(Report{Checks: checks}, "", "plugin hook")
		if c == nil || c.Severity != SevOK {
			t.Fatalf("plugin hook row = %+v, want ok", c)
		}
	})

	t.Run("a different plugin script fails", func(t *testing.T) {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "plugins", "cache", "relevo", "relevo", "0.1.0")
		writeDoctorFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"),
			`{"plugins":{"relevo@relevo":[{"installPath":"`+dir+`"}]}}`)
		writeDoctorFile(t, filepath.Join(dir, "hooks", "hooks.json"),
			`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/scripts/relevo-doctor.sh"}]}]}}`)
		checks := MasterMindChecks(MasterMindCheckInput{Claude: true, Home: home, Repo: t.TempDir()})
		c := findCheck(Report{Checks: checks}, "", "plugin hook")
		if c == nil || c.Severity != SevFail {
			t.Fatalf("plugin hook row = %+v, want FAIL", c)
		}
	})

	t.Run("hook missing", func(t *testing.T) {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "plugins", "cache", "relevo", "relevo", "0.1.0")
		writeDoctorFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"),
			`{"plugins":{"relevo@relevo":[{"installPath":"`+dir+`"}]}}`)
		writeDoctorFile(t, filepath.Join(dir, "hooks", "hooks.json"),
			`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"relevo doctor"}]}]}}`)
		checks := MasterMindChecks(MasterMindCheckInput{Claude: true, Home: home, Repo: t.TempDir()})
		c := findCheck(Report{Checks: checks}, "", "plugin hook")
		if c == nil || c.Severity != SevFail {
			t.Fatalf("plugin hook row = %+v, want FAIL", c)
		}
	})
}

// TestDoctorMasterMindRowNoClaimIsInfo pins that a Claude Code mastermind with no
// live push claim is INFO, never FAIL: tools mode gets reports through the
// blocking wait tool.
func TestDoctorMasterMindRowNoClaimIsInfo(t *testing.T) {
	rec := &mastermind.Record{ID: "pl_aaaaaaaabbbb", Name: "architect-1", HarnessKind: "claude", SessionID: "sess"}
	checks := MasterMindChecks(MasterMindCheckInput{Detected: true, Resolved: rec, MCPChild: true})

	c := findCheck(Report{Checks: checks}, "", "MasterMind")
	if c == nil {
		t.Fatal("the mastermind row is missing")
	}
	if c.Severity != SevInfo {
		t.Fatalf("mastermind row = %v (%s), want INFO", c.Severity, c.Detail)
	}
	for _, want := range []string{"wait tool", "relevo push"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("mastermind row detail %q must name %q", c.Detail, want)
		}
	}
}

func TestDoctorMasterMindRowNoMCPChildFails(t *testing.T) {
	rec := &mastermind.Record{ID: "pl_aaaaaaaabbbb", Name: "architect-1", HarnessKind: "claude", SessionID: "sess"}
	checks := MasterMindChecks(MasterMindCheckInput{Detected: true, Resolved: rec, ClaimLive: true})

	c := findCheck(Report{Checks: checks}, "", "MasterMind")
	if c == nil {
		t.Fatal("the mastermind row is missing")
	}
	if c.Severity != SevFail {
		t.Fatalf("mastermind row = %v (%s), want FAIL with no relevo mcp child", c.Severity, c.Detail)
	}
	if c.Fix == "" {
		t.Error("the failing mastermind row must carry the fix")
	}
}

func TestHasMCPChild(t *testing.T) {
	cases := []struct {
		name     string
		children []ChildProcess
		want     bool
	}{
		{"relevo mcp child", []ChildProcess{{PID: 2, Args: []string{"/usr/local/bin/relevo", "mcp"}}}, true},
		{"relevo mcp with flags", []ChildProcess{{PID: 2, Args: []string{"relevo", "--mastermind", "x", "mcp"}}}, true},
		{"another binary", []ChildProcess{{PID: 2, Args: []string{"relevo-wrapper", "mcp"}}}, false},
		{"relevo another verb", []ChildProcess{{PID: 2, Args: []string{"relevo", "status"}}}, false},
		{"no children", nil, false},
	}
	for _, tc := range cases {
		if got := HasMCPChild(tc.children); got != tc.want {
			t.Errorf("%s: HasMCPChild = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestDoctorPluginVersionRow pins the plugin-version row: advisory only,
// and every situation relevo cannot prove reads "not checked" (OK).
func TestDoctorPluginVersionRow(t *testing.T) {
	state := func(version string) string {
		return `{"version":2,"plugins":{"relevo@relevo":[{"version":"` + version + `","installPath":"/tmp/relevo/0.8.0"}]}}`
	}

	cases := []struct {
		name    string
		body    string // ~/.claude/plugins/installed_plugins.json; "" writes nothing
		running string
		wantSev Severity
		want    string // $STATE stands for the state file's path
	}{
		{"no running version", state("0.8.0"), "", SevOK, "not checked"},
		{"missing file", "", "v0.8.0", SevOK, "not checked (no readable $STATE)"},
		{"not json", "not json", "v0.8.0", SevOK, "not checked (no readable $STATE)"},
		{"no relevo entry", `{"version":2,"plugins":{"other@relevo":[{"version":"0.8.0"}]}}`, "v0.8.0", SevOK, "not checked (relevo plugin not installed)"},
		{"dev build matches the release", state("0.8.0"), "v0.8.0-15-gd664545", SevOK, "plugin 0.8.0 matches relevo"},
		{"exact match", state("0.8.0"), "v0.8.0", SevOK, "plugin 0.8.0 matches relevo"},
		{"older plugin warns", state("0.7.0"), "v0.8.0", SevWarn, "plugin 0.7.0, relevo v0.8.0"},
		{"running is (devel)", state("0.8.0"), "(devel)", SevOK, "not checked (relevo is (devel))"},
		{"plugin version does not parse", state("garbage"), "v0.8.0", SevOK, "not checked (relevo is v0.8.0)"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.body != "" {
				writeDoctorFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), tc.body)
			}
			checks := MasterMindChecks(MasterMindCheckInput{Claude: true, Home: home, Repo: t.TempDir(), Running: tc.running})
			c := findCheck(Report{Checks: checks}, "", "plugin version")
			if c == nil {
				t.Fatal("the plugin version row must be present when a claude candidate exists")
			}
			if c.Severity != tc.wantSev {
				t.Errorf("severity = %v (%s), want %v", c.Severity, c.Detail, tc.wantSev)
			}
			want := strings.ReplaceAll(tc.want, "$STATE", filepath.Join(home, ".claude", claudePluginStateRel))
			if c.Detail != want {
				t.Errorf("detail = %q, want %q", c.Detail, want)
			}
			if tc.wantSev == SevWarn && c.Fix != "claude plugin update relevo@relevo" {
				t.Errorf("fix = %q, want the update command", c.Fix)
			}
		})
	}

	t.Run("no claude candidate means no row", func(t *testing.T) {
		home := t.TempDir()
		writeDoctorFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), state("0.8.0"))
		checks := MasterMindChecks(MasterMindCheckInput{Claude: false, Home: home, Repo: t.TempDir(), Running: "v0.8.0"})
		if c := findCheck(Report{Checks: checks}, "", "plugin version"); c != nil {
			t.Errorf("no claude candidate must leave the plugin version row out, got %+v", c)
		}
	})
}

// TestDoctorConsentRow pins the repository-consent row: the answer, the
// command that changes it, and no row when the answer was not read.
func TestDoctorConsentRow(t *testing.T) {
	cases := []struct {
		name    string
		in      MasterMindCheckInput
		sev     Severity
		want    string
		wantFix string
	}{
		{
			name: "yes",
			in:   MasterMindCheckInput{ConsentKnown: true, RepoKnown: true, Consent: mastermind.ConsentYes},
			sev:  SevOK, want: "yes", wantFix: "relevo mastermind disable --repo",
		},
		{
			name: "no",
			in:   MasterMindCheckInput{ConsentKnown: true, RepoKnown: true, Consent: mastermind.ConsentNo},
			sev:  SevInfo, want: "no", wantFix: "relevo mastermind enable --repo",
		},
		{
			name: "unset",
			in:   MasterMindCheckInput{ConsentKnown: true, RepoKnown: true, Consent: mastermind.ConsentUnset},
			sev:  SevInfo, want: "unset", wantFix: "relevo mastermind enable --repo (or disable --repo)",
		},
		{
			name: "not a repository",
			in:   MasterMindCheckInput{ConsentKnown: true, RepoKnown: false},
			sev:  SevInfo, want: "not in a git repository", wantFix: "run relevo doctor inside the repository, or relevo mastermind init by hand",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := findCheck(Report{Checks: MasterMindChecks(tc.in)}, "", "consent")
			if c == nil {
				t.Fatal("the consent row must be present when the answer was read")
			}
			if c.Severity != tc.sev {
				t.Errorf("severity = %v (%s), want %v", c.Severity, c.Detail, tc.sev)
			}
			if !strings.Contains(c.Detail, tc.want) {
				t.Errorf("detail = %q, want it to contain %q", c.Detail, tc.want)
			}
			if c.Fix != tc.wantFix {
				t.Errorf("fix = %q, want %q", c.Fix, tc.wantFix)
			}
		})
	}

	t.Run("no read means no row", func(t *testing.T) {
		if c := findCheck(Report{Checks: MasterMindChecks(MasterMindCheckInput{})}, "", "consent"); c != nil {
			t.Errorf("consent row present without a read: %+v", c)
		}
	})
}

// museInstalledJSON is one `muse plugins inspect relevo --json` answer:
// enabled and fully trusted at the given version.
func museInstalledJSON(version string) string {
	return `{"active":true,"record":{"enabled":true,"version":"` + version + `"},` +
		`"runtime_capabilities":[` +
		`{"candidate":{"kind":"hook","stable_id":"plugin:relevo:hook:hook-a","plugin_id":"relevo"},"status":"trusted_enabled","diagnostic":null},` +
		`{"candidate":{"kind":"mcp_server","stable_id":"plugin:relevo:mcp:mcp-a","plugin_id":"relevo"},"status":"trusted_enabled","diagnostic":null}]}`
}

func museNotInstalledJSON() string {
	return `{"error":{"code":"unknown-plugin","message":"plugin ` + "`relevo`" + ` is not installed"}}`
}

// museInspectReader answers the injected probe with a fixed stdout and error.
func museInspectReader(out string, err error) MuseInspectFn {
	return func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		return []byte(out), err
	}
}

func museChecks(t *testing.T, out string, err error, running string) []Check {
	t.Helper()
	return MasterMindChecks(MasterMindCheckInput{
		Muse: true, Running: running, MuseInspect: museInspectReader(out, err),
	})
}

// TestDoctorMusePluginRowsNoGate pins the gating: with Muse false the muse
// rows are absent entirely, the same shape as the Claude gating.
func TestDoctorMusePluginRowsNoGate(t *testing.T) {
	checks := MasterMindChecks(MasterMindCheckInput{Home: t.TempDir(), Repo: t.TempDir()})
	for _, name := range []string{"plugin", "plugin hook", "plugin version"} {
		if c := findCheck(Report{Checks: checks}, "", name); c != nil {
			t.Errorf("no muse gating must leave the %q row out, got %+v", name, c)
		}
	}
}

// TestDoctorMusePluginRowInstalledOK pins the all-green shape: plugin
// present, enabled, approved, hooks and MCP trusted, versions matching.
func TestDoctorMusePluginRowInstalledOK(t *testing.T) {
	checks := museChecks(t, museInstalledJSON("0.22.4"), nil, "v0.22.4")
	if c := findCheck(Report{Checks: checks}, "", "plugin"); c == nil || c.Severity != SevOK || !strings.Contains(c.Detail, "relevo@relevo enabled in muse") {
		t.Fatalf("plugin row = %+v, want OK saying relevo@relevo is enabled in muse", c)
	}
	c := findCheck(Report{Checks: checks}, "", "plugin hook")
	if c == nil || c.Severity != SevOK || !strings.Contains(c.Detail, museStatusTrusted) {
		t.Fatalf("plugin hook row = %+v, want OK naming %s", c, museStatusTrusted)
	}
	if c := findCheck(Report{Checks: checks}, "", "plugin version"); c == nil || c.Severity != SevOK || c.Detail != "plugin 0.22.4 matches relevo" {
		t.Fatalf("plugin version row = %+v, want the Claude row's match wording", c)
	}
}

// museExitError runs `sh -c "echo $out; exit 1"` and returns its stdout with
// the real *exec.ExitError muse answers a non-zero exit with, so the probe's
// spawn-error-vs-exit-1 rule is pinned without spawning muse itself.
func museExitError(t *testing.T, out string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("sh", "-c", "printf '%s' '"+out+"' && exit 1")
	body, err := cmd.Output()
	if err == nil {
		t.Fatal("the helper command must exit non-zero")
	}
	return body, err
}

// TestDoctorMusePluginRowMissingFailsInstall pins the missing plugin: FAIL
// whose fix adds the marketplace and installs the plugin. muse answers this
// with exit 1 and the error object, which the helper reproduces.
func TestDoctorMusePluginRowMissingFailsInstall(t *testing.T) {
	out, err := museExitError(t, museNotInstalledJSON())
	checks := museChecks(t, string(out), err, "v0.22.4")
	c := findCheck(Report{Checks: checks}, "", "plugin")
	if c == nil || c.Severity != SevFail {
		t.Fatalf("plugin row = %+v, want FAIL when muse says the plugin is not installed", c)
	}
	want := "muse plugins marketplace add relevo https://github.com/fuad-daoud/relevo.git && muse plugins install relevo@relevo"
	if c.Fix != want {
		t.Errorf("plugin row fix = %q, want %q", c.Fix, want)
	}
}

// TestDoctorMusePluginRowDisabledFailsEnable pins a disabled plugin: FAIL
// with the enable fix.
func TestDoctorMusePluginRowDisabledFailsEnable(t *testing.T) {
	out := `{"active":false,"record":{"enabled":false,"version":"0.22.4"},"runtime_capabilities":[]}`
	checks := museChecks(t, out, nil, "v0.22.4")
	c := findCheck(Report{Checks: checks}, "", "plugin")
	if c == nil || c.Severity != SevFail || c.Fix != "muse plugins enable relevo" {
		t.Fatalf("plugin row = %+v, want FAIL with the enable fix", c)
	}
}

// TestDoctorMusePluginRowUnapprovedFailsApprove pins hooks and MCP that were
// never approved (or drifted after a plugin update): FAIL with the approve
// fix, and the detail names the statuses seen.
func TestDoctorMusePluginRowUnapprovedFailsApprove(t *testing.T) {
	out := `{"active":true,"record":{"enabled":true,"version":"0.22.4"},"runtime_capabilities":[` +
		`{"candidate":{"kind":"hook","stable_id":"plugin:relevo:hook:hook-a","plugin_id":"relevo"},"status":"review_needed"},` +
		`{"candidate":{"kind":"mcp_server","stable_id":"plugin:relevo:mcp:mcp-a","plugin_id":"relevo"},"status":"trusted_disabled"}]}`
	checks := museChecks(t, out, nil, "v0.22.4")
	c := findCheck(Report{Checks: checks}, "", "plugin hook")
	if c == nil || c.Severity != SevFail || c.Fix != "muse plugins approve relevo" {
		t.Fatalf("plugin hook row = %+v, want FAIL with the approve fix", c)
	}
	for _, want := range []string{"hook: review_needed", "mcp_server: trusted_disabled"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("plugin hook detail %q must name %q", c.Detail, want)
		}
	}
}

// TestDoctorMusePluginRowVersionDriftWarns pins the version row: advisory
// Warn with the update fix when record.version and relevo differ.
func TestDoctorMusePluginRowVersionDriftWarns(t *testing.T) {
	checks := museChecks(t, museInstalledJSON("0.22.3"), nil, "v0.22.4")
	c := findCheck(Report{Checks: checks}, "", "plugin version")
	if c == nil || c.Severity != SevWarn || c.Fix != "muse plugins update relevo" {
		t.Fatalf("plugin version row = %+v, want Warn with the update fix", c)
	}
	if c.Detail != "plugin 0.22.3, relevo v0.22.4" {
		t.Errorf("plugin version detail = %q, want the Claude row's drift wording", c.Detail)
	}
	if c := findCheck(Report{Checks: checks}, "", "plugin"); c == nil || c.Severity != SevOK {
		t.Errorf("a version drift must not change the enabled row: %+v", c)
	}
}

// TestDoctorMusePluginRowsUnreadableNotChecked pins the unreadable-state
// rule: a spawn error or an unparseable body leaves every muse row
// `not checked` OK, never FAIL.
func TestDoctorMusePluginRowsUnreadableNotChecked(t *testing.T) {
	cases := []struct {
		name string
		out  string
		err  error
	}{
		{"spawn error", "", errors.New("executable file not found in $PATH")},
		{"unparseable json", "not json", nil},
		{"installed answer with no record", `{"active":true}`, nil},
		{"unknown error code", `{"error":{"code":"internal"}}`, nil},
		// Exit 1 is only a fact when muse named it: the same unknown-plugin
		// body beside an error that is not an exit status reads unreadable.
		{"unknown-plugin without an exit status", museNotInstalledJSON(), errors.New("not an exit status")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checks := museChecks(t, tc.out, tc.err, "v0.22.4")
			for _, name := range []string{"plugin", "plugin hook", "plugin version"} {
				c := findCheck(Report{Checks: checks}, "", name)
				if c == nil || c.Severity != SevOK || !strings.HasPrefix(c.Detail, "not checked") {
					t.Errorf("%s row = %+v, want not checked OK", name, c)
				}
			}
		})
	}
}

// TestDoctorMuseSessionRowPinsEnvGate pins that the MasterMind row is gated
// on the MuseSession input (cmd/relevo sets it from MUSE_SESSION_ID read
// directly, not via Detect) and resolves like the Claude row: FAIL on a
// Resolve miss, OK with a live claim.
func TestDoctorMuseSessionRowPinsEnvGate(t *testing.T) {
	rec := &mastermind.Record{ID: "pl_aaaaaaaabbbb", Name: "muse-1", HarnessKind: "muse", SessionID: "sess"}

	checks := MasterMindChecks(MasterMindCheckInput{MuseSession: true})
	c := findCheck(Report{Checks: checks}, "", "MasterMind")
	if c == nil || c.Severity != SevFail {
		t.Fatalf("mastermind row = %+v, want FAIL when the muse session resolves nothing", c)
	}

	checks = MasterMindChecks(MasterMindCheckInput{MuseSession: true, Resolved: rec, MCPChild: true, ClaimLive: true})
	if c := findCheck(Report{Checks: checks}, "", "MasterMind"); c == nil || c.Severity != SevOK {
		t.Fatalf("mastermind row = %+v, want OK for a resolved muse session with a live claim", c)
	}

	checks = MasterMindChecks(MasterMindCheckInput{Resolved: rec, MCPChild: true, ClaimLive: true})
	if c := findCheck(Report{Checks: checks}, "", "MasterMind"); c != nil {
		t.Errorf("the mastermind row exists only when a session was detected, got %+v", c)
	}
}

// TestMuseInspectReaderGetsExactCommand pins the probe command: `muse
// plugins inspect relevo --json`, through the injected reader only.
func TestMuseInspectReaderGetsExactCommand(t *testing.T) {
	var gotBin string
	var gotArgs []string
	in := MasterMindCheckInput{Muse: true, MuseInspect: func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		gotBin, gotArgs = bin, args
		return []byte(museInstalledJSON("0.22.4")), nil
	}}
	MasterMindChecks(in)
	if gotBin != "muse" || strings.Join(gotArgs, " ") != "plugins inspect relevo --json" {
		t.Errorf("probe ran %s %v, want muse plugins inspect relevo --json", gotBin, gotArgs)
	}
}
