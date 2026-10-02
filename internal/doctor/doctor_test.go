package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/classify"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/store"
)

// shippedDoc returns the exact bytes this relevo ships for role/kind.
func shippedDoc(t *testing.T, role, kind string) string {
	t.Helper()
	b, err := harness.AgentDoc(role, kind)
	if err != nil {
		t.Fatalf("AgentDoc(%s, %s): %v", role, kind, err)
	}
	return string(b)
}

type fakeEnv struct {
	daemonRunning bool
	daemonErr     error
	daemonInfo    store.DaemonInfo
	daemonInfoOK  bool
	daemonInfoErr error
	lookPaths     map[string]string // binary -> path
	existingFiles map[string]bool   // path -> exists
	fileContents  map[string]string // path -> content; absent reads as empty
	homeDir       string
	homeErr       error
	versions      map[string]string // binary path -> version output
	versionErr    error
	probeErr      error
	commandOut    []byte
	commandErr    error

	// commandFn, when non-nil, answers Command directly and overrides commandOut/commandErr.
	commandFn func(bin string, args ...string) ([]byte, error)

	releaseRunning string
	releaseLatest  string
	releaseOK      bool
	releaseKind    release.Kind

	manifest map[string]string
}

func (f *fakeEnv) DaemonRunning(ctx context.Context) (bool, error) {
	if f.daemonErr != nil {
		return false, f.daemonErr
	}
	return f.daemonRunning, nil
}

func (f *fakeEnv) DaemonInfo() (store.DaemonInfo, bool, error) {
	if f.daemonInfoErr != nil {
		return store.DaemonInfo{}, false, f.daemonInfoErr
	}
	return f.daemonInfo, f.daemonInfoOK, nil
}

func (f *fakeEnv) LookPath(binary string) (string, error) {
	if path, ok := f.lookPaths[binary]; ok {
		return path, nil
	}
	return "", errors.New("executable file not found in $PATH")
}

func (f *fakeEnv) HomePath(rel string) (string, error) {
	if f.homeErr != nil {
		return "", f.homeErr
	}
	home := f.homeDir
	if home == "" {
		home = "/fake/home"
	}
	return filepath.Join(home, rel), nil
}

func (f *fakeEnv) Stat(path string) error {
	if f.existingFiles != nil && f.existingFiles[path] {
		return nil
	}
	return os.ErrNotExist
}

func (f *fakeEnv) ReadFile(path string) ([]byte, error) {
	return []byte(f.fileContents[path]), nil
}

func (f *fakeEnv) BinaryVersion(ctx context.Context, path string) (string, error) {
	if f.versionErr != nil {
		return "", f.versionErr
	}
	v, ok := f.versions[path]
	if !ok {
		return "", errors.New("no version recorded for " + path)
	}
	return v, nil
}

func (f *fakeEnv) Probe(dir string) error {
	return f.probeErr
}

func (f *fakeEnv) Command(ctx context.Context, bin string, args ...string) ([]byte, error) {
	if f.commandFn != nil {
		return f.commandFn(bin, args...)
	}
	if f.commandErr != nil {
		return nil, f.commandErr
	}
	return f.commandOut, nil
}

func (f *fakeEnv) ReleaseState() (string, string, bool, release.Kind) {
	return f.releaseRunning, f.releaseLatest, f.releaseOK, f.releaseKind
}

func (f *fakeEnv) LoadManifest() (map[string]string, error) {
	return f.manifest, nil
}

func findCheck(report Report, group, name string) *Check {
	for i := range report.Checks {
		if report.Checks[i].Group == group && report.Checks[i].Name == name {
			return &report.Checks[i]
		}
	}
	return nil
}

func countChecksForGroup(report Report, group string) int {
	count := 0
	for _, c := range report.Checks {
		if c.Group == group {
			count++
		}
	}
	return count
}

func TestConfigCheck(t *testing.T) {
	ok := ConfigCheck(nil)
	if ok.Name != "config" || ok.Group != "" || ok.Severity != SevOK {
		t.Errorf("ConfigCheck(nil) = %+v, want an OK global config row", ok)
	}

	warnings := []string{
		`policy.json: unknown key "orders" (a typo, or a key a newer relevo reads)`,
		`candidates.json: nope/p/m: unknown harness "nope" (skipped)`,
	}
	w := ConfigCheck(warnings)
	if w.Severity != SevWarn {
		t.Errorf("ConfigCheck(2) severity = %v, want SevWarn", w.Severity)
	}
	for _, want := range []string{"orders", "unknown harness"} {
		if !strings.Contains(w.Detail, want) {
			t.Errorf("ConfigCheck(2) detail %q does not contain %q", w.Detail, want)
		}
	}
}

func TestDoctorConfigRow(t *testing.T) {
	rep := Run(context.Background(), &fakeEnv{}, nil,
		WithConfigWarnings([]string{"w1", "w2"}))

	c := findCheck(rep, "", "config")
	if c == nil {
		t.Fatal("no config row in the report")
	}
	if c.Severity != SevWarn || !strings.Contains(c.Detail, "w1") || !strings.Contains(c.Detail, "w2") {
		t.Errorf("config row = %+v, want a Warn listing both warnings", c)
	}

	clean := Run(context.Background(), &fakeEnv{}, nil, WithConfigWarnings(nil))
	if c := findCheck(clean, "", "config"); c == nil || c.Severity != SevOK {
		t.Errorf("config row with no warnings = %+v, want OK", c)
	}
}

func TestDoctorMissingBinarySuppressesRemainingRows(t *testing.T) {
	env := &fakeEnv{
		lookPaths: map[string]string{}, // nothing on PATH
	}

	report := Run(context.Background(), env, []string{"opencode"})
	count := countChecksForGroup(report, "opencode")
	if count != 1 {
		t.Errorf("opencode should have exactly 1 check (binary), got %d", count)
	}
	binCheck := findCheck(report, "opencode", "binary")
	if binCheck == nil {
		t.Fatal("opencode binary check missing")
	}
	if binCheck.Severity != SevWarn {
		t.Errorf("opencode binary check severity = %v, want SevWarn", binCheck.Severity)
	}
	if binCheck.Detail != "not on PATH -- skipping the rest of this harness" {
		t.Errorf("opencode binary detail = %q, want 'not on PATH -- skipping the rest of this harness'", binCheck.Detail)
	}
}

func TestDoctorUnknownKindDegradesWithoutFailing(t *testing.T) {
	env := &fakeEnv{
		lookPaths: map[string]string{
			"droid": "/usr/bin/droid",
		},
	}

	report := Run(context.Background(), env, []string{"droid"})
	roleCheck := findCheck(report, "droid", "plan-executor")
	if roleCheck == nil {
		t.Fatal("droid plan-executor check not found")
	}
	if roleCheck.Severity != SevOK {
		t.Errorf("droid role check severity = %v, want SevOK", roleCheck.Severity)
	}
	if report.Failures() != 0 {
		t.Errorf("an unknown kind must not fail doctor, got %d failures", report.Failures())
	}
}

func TestDoctorRolesRow(t *testing.T) {
	roles := []string{"plan-executor", "researcher", "reviewer", "security-reviewer", "architect", "librarian"}
	fill := func(body func(role string) string) map[string]string {
		m := map[string]string{}
		for _, r := range roles {
			m[r] = body(r)
		}
		return m
	}
	envFor := func(contents map[string]string) *fakeEnv {
		existing, files := map[string]bool{}, map[string]string{}
		for role, content := range contents {
			p := "/fake/home/.claude/agents/" + role + ".md"
			existing[p], files[p] = true, content
		}
		return &fakeEnv{daemonRunning: true, lookPaths: map[string]string{"claude": "/usr/bin/claude"}, homeDir: "/fake/home", existingFiles: existing, fileContents: files}
	}

	tests := []struct {
		name       string
		contents   func(t *testing.T) map[string]string
		noBinary   bool
		wantRow    bool
		wantSev    Severity
		wantDetail string
		wantFix    string
	}{
		{name: "stale", wantRow: true, wantSev: SevWarn, wantDetail: "agent definitions are stale", wantFix: "relevo config agents"},
		{name: "user edited is kept", wantRow: true, wantSev: SevOK, wantDetail: "differs from every copy relevo has shipped (kept as your edit)",
			contents: func(*testing.T) map[string]string {
				return fill(func(string) string { return "---\nmodel: haiku\n---\nmine\n" })
			}},
		{name: "current", wantRow: true, wantSev: SevOK, wantDetail: "up to date",
			contents: func(t *testing.T) map[string]string {
				return fill(func(r string) string { return shippedDoc(t, r, "claude") })
			}},
		{name: "binary off PATH has no row", noBinary: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := &fakeEnv{lookPaths: map[string]string{}}
			if !tc.noBinary {
				var contents map[string]string
				if tc.contents != nil {
					contents = tc.contents(t)
				}
				env = envFor(contents)
			}
			c := findCheck(Run(context.Background(), env, []string{"claude"}), "claude", "roles")
			if !tc.wantRow {
				if c != nil {
					t.Errorf("roles row %+v, want none when the binary is off PATH", *c)
				}
				return
			}
			if c == nil {
				t.Fatal("claude has its binary on PATH, so it needs a roles row")
			}
			if c.Severity != tc.wantSev || !strings.Contains(c.Detail, tc.wantDetail) {
				t.Errorf("row = %+v, want severity %v containing %q", *c, tc.wantSev, tc.wantDetail)
			}
			if tc.wantFix != "" && c.Fix != tc.wantFix {
				t.Errorf("fix = %q, want %q", c.Fix, tc.wantFix)
			}
		})
	}
}

func TestDoctorAdoptedBindingSurvivesMissingBinary(t *testing.T) {
	env := &fakeEnv{
		lookPaths: map[string]string{}, // binary absent
	}

	normalRep := Run(context.Background(), env, []string{"claude"})
	if findCheck(normalRep, "claude", "binary") == nil {
		t.Fatal("normal Run must report the binary it could not find on PATH")
	}

	adoptedRep := Run(context.Background(), env, []string{"claude"}, WithAdopted(true))
	if c := findCheck(adoptedRep, "claude", "binary"); c != nil {
		t.Fatalf("adopted Run must not report a binary row, got %+v", *c)
	}
	for _, role := range []string{"plan-executor", "researcher", "reviewer"} {
		if c := findCheck(adoptedRep, "claude", role); c != nil {
			t.Errorf("adopted Run must not report a %s role row, got %+v", role, *c)
		}
	}
}

func TestDoctorUsableBuilderMissingRoleFileDoesNotBreakCompleteness(t *testing.T) {
	env := &fakeEnv{
		daemonRunning: true,
		lookPaths: map[string]string{
			"claude": "/usr/bin/claude",
		},
		homeDir:       "/fake/home",
		existingFiles: map[string]bool{}, // role file missing
	}

	report := Run(context.Background(), env, []string{"claude"})
	if !report.UsableBuilder {
		t.Error("UsableBuilder should be true even when role file is missing")
	}
	if report.Failures() != 0 {
		t.Errorf("expected 0 failures, got %d", report.Failures())
	}
	roleCheck := findCheck(report, "claude", "plan-executor")
	if roleCheck == nil || roleCheck.Severity != SevWarn {
		t.Errorf("missing role file check should be SevWarn, got: %+v", roleCheck)
	}
}

func TestDoctorHomePathFailureReportsErrorWithoutFix(t *testing.T) {
	env := &fakeEnv{
		daemonRunning: true,
		lookPaths: map[string]string{
			"claude": "/usr/bin/claude",
		},
		homeErr: errors.New("cannot determine user home"),
	}

	report := Run(context.Background(), env, []string{"claude"})
	c := findCheck(report, "claude", "plan-executor")
	if c == nil {
		t.Fatal("plan-executor check not found")
	}
	if c.Severity != SevWarn {
		t.Errorf("severity = %v, want SevWarn", c.Severity)
	}
	if !c.ProbeFailed {
		t.Error("ProbeFailed must be true on HomePath error")
	}
	if c.Fix != "" {
		t.Errorf("fix must be empty, got %q", c.Fix)
	}
	if !strings.Contains(c.Detail, "could not resolve home directory") {
		t.Errorf("detail = %q, want containing 'could not resolve home directory'", c.Detail)
	}
}

func TestDoctorEmitsOneRowPerRole(t *testing.T) {
	env := newFakeEnvForKind(t, "claude")
	env.existingFiles = map[string]bool{
		"/fake/home/.claude/agents/plan-executor.md": true,
		"/fake/home/.claude/agents/researcher.md":    true,
	}
	env.fileContents = map[string]string{
		"/fake/home/.claude/agents/plan-executor.md": shippedDoc(t, "plan-executor", "claude"),
		"/fake/home/.claude/agents/researcher.md":    shippedDoc(t, "researcher", "claude"),
	}

	report := Run(context.Background(), env, []string{"claude"})

	for _, role := range []string{"plan-executor", "researcher"} {
		c := findCheck(report, "claude", role)
		if c == nil {
			t.Fatalf("no %q row for claude", role)
		}
		if c.Severity != SevOK {
			t.Errorf("%s severity = %v, want ok", role, c.Severity)
		}
	}
}

func TestDoctorMissingRoleFileNamesTheRoleInTheFix(t *testing.T) {
	env := newFakeEnvForKind(t, "claude")
	env.existingFiles = map[string]bool{
		"/fake/home/.claude/agents/plan-executor.md": true,
	}

	report := Run(context.Background(), env, []string{"claude"})

	c := findCheck(report, "claude", "researcher")
	if c == nil {
		t.Fatal("no researcher row for claude")
	}
	if c.Severity != SevWarn {
		t.Errorf("severity = %v, want warn", c.Severity)
	}
	want := "relevo config agents --kind claude --agent researcher"
	if c.Fix != want {
		t.Errorf("fix = %q, want %q", c.Fix, want)
	}
}

func TestDoctorReportsTheInstalledModelPin(t *testing.T) {
	env := newFakeEnvForKind(t, "claude")
	env.existingFiles = map[string]bool{
		"/fake/home/.claude/agents/plan-executor.md": true,
		"/fake/home/.claude/agents/researcher.md":    true,
	}
	env.fileContents = map[string]string{
		"/fake/home/.claude/agents/researcher.md": "---\nname: researcher\nmodel: haiku\n---\n",
	}

	report := Run(context.Background(), env, []string{"claude"})

	c := findCheck(report, "claude", "researcher")
	if c == nil {
		t.Fatal("no researcher row")
	}
	if !strings.Contains(c.Detail, "model: haiku") {
		t.Errorf("detail = %q, want it to report the pinned model", c.Detail)
	}
}

func TestDoctorOmitsModelSuffixWhenUnpinned(t *testing.T) {
	env := newFakeEnvForKind(t, "claude")
	env.existingFiles = map[string]bool{
		"/fake/home/.claude/agents/plan-executor.md": true,
		"/fake/home/.claude/agents/researcher.md":    true,
	}
	env.fileContents = map[string]string{
		"/fake/home/.claude/agents/plan-executor.md": shippedDoc(t, "plan-executor", "claude"),
		"/fake/home/.claude/agents/researcher.md":    shippedDoc(t, "researcher", "claude"),
	}

	report := Run(context.Background(), env, []string{"claude"})

	c := findCheck(report, "claude", "plan-executor")
	if c == nil {
		t.Fatal("no plan-executor row")
	}
	if strings.Contains(c.Detail, "model:") {
		t.Errorf("detail = %q, want no model suffix", c.Detail)
	}
	if c.Severity != SevOK {
		t.Errorf("severity = %v, want ok -- an unpinned model is not a fault", c.Severity)
	}
}

func newFakeEnvForKind(t *testing.T, kind string) *fakeEnv {
	t.Helper()
	return &fakeEnv{
		daemonRunning: true,
		lookPaths:     map[string]string{kind: "/usr/bin/" + kind},
		homeDir:       "/fake/home",
	}
}

// agyEnv is an agy machine in good order; tests perturb one thing at a time.
func agyEnv(t *testing.T) *fakeEnv {
	t.Helper()
	home := "/home/u"
	env := &fakeEnv{
		homeDir:       home,
		lookPaths:     map[string]string{"agy": "/home/fuad/.local/bin/agy"},
		existingFiles: map[string]bool{},
		fileContents:  map[string]string{},
		versions:      map[string]string{"/home/fuad/.local/bin/agy": "1.2.1"},
	}
	for _, name := range []string{"plan-executor", "researcher", "reviewer", "librarian"} {
		p := home + "/.gemini/config/agents/" + name + ".md"
		env.existingFiles[p] = true
		env.fileContents[p] = shippedDoc(t, name, "agy")
	}
	return env
}

func TestDoctorAgyVersionFloor(t *testing.T) {
	cases := []struct {
		name     string
		version  string
		err      error
		wantSev  Severity
		wantDet  string
		wantFix  string
		wantProb bool
	}{
		{name: "at floor", version: "1.1.6", wantSev: SevOK, wantDet: "1.1.6 (floor 1.1.6)"},
		{name: "above floor", version: "1.2.1", wantSev: SevOK, wantDet: "1.2.1 (floor 1.1.6)"},
		{name: "below floor", version: "1.1.5", wantSev: SevFail, wantDet: "1.1.5 (below floor 1.1.6)", wantFix: "upgrade agy to >= 1.1.6"},
		{name: "garbage", version: "garbage", wantSev: SevWarn, wantDet: `unparseable version "garbage"`},
		{name: "probe fails", err: errors.New("boom"), wantSev: SevWarn, wantDet: "could not read version: boom", wantProb: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := agyEnv(t) // step 4's helper: paths and three role files pinning inherit
			env.versions = map[string]string{"/home/fuad/.local/bin/agy": tc.version}
			env.versionErr = tc.err
			report := Run(context.Background(), env, []string{"agy"})
			c := findCheck(report, "agy", "version")
			if c == nil {
				t.Fatal("agy version row not found")
			}
			if c.Severity != tc.wantSev || c.Detail != tc.wantDet || c.Fix != tc.wantFix || c.ProbeFailed != tc.wantProb {
				t.Errorf("row = %+v, want sev %v detail %q fix %q probeFailed %v", *c, tc.wantSev, tc.wantDet, tc.wantFix, tc.wantProb)
			}
		})
	}
}

func TestDoctorNoVersionRowWithoutAFloor(t *testing.T) {
	for _, kind := range []string{"claude", "opencode"} {
		env := &fakeEnv{
			lookPaths: map[string]string{kind: "/usr/bin/" + kind},
		}
		report := Run(context.Background(), env, []string{kind})
		if c := findCheck(report, kind, "version"); c != nil {
			t.Errorf("%s has no MinVersion, got version row %+v", kind, *c)
		}
	}
}

func TestDoctorAgyRoleWarnsOnATierPin(t *testing.T) {
	env := agyEnv(t)
	env.versions = map[string]string{"/home/fuad/.local/bin/agy": "1.2.1"}
	env.fileContents[env.homeDir+"/.gemini/config/agents/researcher.md"] = "---\nname: researcher\nmodel: pro\n---\nbody\n"
	report := Run(context.Background(), env, []string{"agy"})

	c := findCheck(report, "agy", "researcher")
	if c == nil {
		t.Fatal("agy researcher row not found")
	}
	if c.Severity != SevWarn {
		t.Errorf("severity = %v, want SevWarn", c.Severity)
	}
	if c.Detail != "~/.gemini/config/agents/researcher.md (model: pro) -- pins a tier; the candidate's --model is ignored" {
		t.Errorf("detail = %q", c.Detail)
	}
	if c.Fix != "set model: inherit in ~/.gemini/config/agents/researcher.md" {
		t.Errorf("fix = %q", c.Fix)
	}
	for _, name := range []string{"plan-executor", "reviewer", "librarian"} {
		if c := findCheck(report, "agy", name); c == nil || c.Severity != SevOK {
			t.Errorf("%s row = %+v, want SevOK", name, c)
		}
	}
}

func TestDoctorClaudeRoleNeverWarnsOnAPin(t *testing.T) {
	env := &fakeEnv{
		homeDir:       "/home/u",
		lookPaths:     map[string]string{"claude": "/usr/bin/claude"},
		existingFiles: map[string]bool{"/home/u/.claude/agents/plan-executor.md": true},
		fileContents:  map[string]string{"/home/u/.claude/agents/plan-executor.md": "---\nmodel: opus\n---\n"},
	}
	report := Run(context.Background(), env, []string{"claude"})
	c := findCheck(report, "claude", "plan-executor")
	if c == nil || c.Severity != SevOK || c.Detail != "~/.claude/agents/plan-executor.md (model: opus)" {
		t.Errorf("row = %+v, want SevOK with the pin reported", c)
	}
}

func TestDoctorAgyRolesAreCheckedLikeAnyKind(t *testing.T) {
	env := agyEnv(t)
	report := Run(context.Background(), env, []string{"agy"})
	for _, name := range []string{"plan-executor", "researcher", "reviewer", "librarian"} {
		c := findCheck(report, "agy", name)
		if c == nil {
			t.Fatalf("agy %s row not found", name)
		}
		want := "~/.gemini/config/agents/" + name + ".md (model: inherit)"
		if c.Severity != SevOK || c.Detail != want {
			t.Errorf("%s row = %+v, want SevOK %q", name, *c, want)
		}
	}
}

func TestDoctorAgyMissingRoleHasAFix(t *testing.T) {
	env := agyEnv(t)
	delete(env.existingFiles, "/home/u/.gemini/config/agents/reviewer.md")
	report := Run(context.Background(), env, []string{"agy"})
	c := findCheck(report, "agy", "reviewer")
	if c == nil || c.Severity != SevWarn || c.Detail != "missing: ~/.gemini/config/agents/reviewer.md" ||
		c.Fix != "relevo config agents --kind agy --agent reviewer" {
		t.Errorf("row = %+v", c)
	}
}

// relevo doctor warns on drift only when Role.ExpectModel is set (today,
// agy); the fifth subtest exercises the opposite on claude.
func TestDoctorRoleDriftFromShipped(t *testing.T) {
	shipped := shippedDoc(t, "researcher", "agy")

	t.Run("identical to shipped is OK", func(t *testing.T) {
		env := agyEnv(t)
		env.fileContents[env.homeDir+"/.gemini/config/agents/researcher.md"] = shipped
		report := Run(context.Background(), env, []string{"agy"})
		c := findCheck(report, "agy", "researcher")
		wantDetail := "~/.gemini/config/agents/researcher.md (model: inherit)"
		if c == nil || c.Severity != SevOK || c.Detail != wantDetail {
			t.Errorf("row = %+v, want SevOK %q", c, wantDetail)
		}
	})

	t.Run("shipped plus a trailing newline is still OK", func(t *testing.T) {
		env := agyEnv(t)
		env.fileContents[env.homeDir+"/.gemini/config/agents/researcher.md"] = shipped + "\n"
		report := Run(context.Background(), env, []string{"agy"})
		c := findCheck(report, "agy", "researcher")
		if c == nil || c.Severity != SevOK {
			t.Errorf("row = %+v, want SevOK -- a missing final newline from a `>` redirect must not warn", c)
		}
	})

	t.Run("one extra line warns with the print fix", func(t *testing.T) {
		env := agyEnv(t)
		env.fileContents[env.homeDir+"/.gemini/config/agents/researcher.md"] = shipped + "\nextra line\n"
		report := Run(context.Background(), env, []string{"agy"})
		c := findCheck(report, "agy", "researcher")
		if c == nil || c.Severity != SevWarn {
			t.Fatalf("row = %+v, want SevWarn", c)
		}
		if !strings.Contains(c.Detail, "differs from the definition this relevo ships") {
			t.Errorf("detail = %q, want it to mention shipped drift", c.Detail)
		}
		if c.Fix != "relevo config agents --kind agy --agent researcher --force" {
			t.Errorf("fix = %q", c.Fix)
		}
	})

	t.Run("a tier-pin mismatch still reports the pin warning, not drift", func(t *testing.T) {
		env := agyEnv(t)
		env.fileContents[env.homeDir+"/.gemini/config/agents/researcher.md"] = "---\nname: researcher\nmodel: pro\n---\nbody\n"
		report := Run(context.Background(), env, []string{"agy"})
		c := findCheck(report, "agy", "researcher")
		wantDetail := "~/.gemini/config/agents/researcher.md (model: pro) -- pins a tier; the candidate's --model is ignored"
		if c == nil || c.Severity != SevWarn || c.Detail != wantDetail {
			t.Errorf("row = %+v, want SevWarn %q", c, wantDetail)
		}
	})

	t.Run("a claude definition that differs from shipped is still OK", func(t *testing.T) {
		env := newFakeEnvForKind(t, "claude")
		env.existingFiles = map[string]bool{"/fake/home/.claude/agents/plan-executor.md": true}
		env.fileContents = map[string]string{
			"/fake/home/.claude/agents/plan-executor.md": shippedDoc(t, "plan-executor", "claude") + "\nextra line\n",
		}
		report := Run(context.Background(), env, []string{"claude"})
		c := findCheck(report, "claude", "plan-executor")
		if c == nil || c.Severity != SevOK {
			t.Errorf("row = %+v, want SevOK", c)
		}
	})
}

func TestDoctorCodexResearcherPin(t *testing.T) {
	newEnv := func(t *testing.T) *fakeEnv {
		t.Helper()
		home := "/home/u"
		env := &fakeEnv{
			homeDir:       home,
			lookPaths:     map[string]string{"codex": "/usr/bin/codex"},
			existingFiles: map[string]bool{},
			fileContents:  map[string]string{},
			versions:      map[string]string{"/usr/bin/codex": "0.155.1"},
		}
		for _, role := range []string{"plan-executor", "researcher", "reviewer", "architect", "librarian"} {
			env.existingFiles[home+"/.codex/"+role+".config.toml"] = true
		}
		return env
	}

	t.Run("researcher pinned to the shipped model is OK", func(t *testing.T) {
		env := newEnv(t)
		env.fileContents["/home/u/.codex/researcher.config.toml"] = shippedDoc(t, "researcher", "codex")
		report := Run(context.Background(), env, []string{"codex"})
		c := findCheck(report, "codex", "researcher")
		want := "~/.codex/researcher.config.toml (model: gpt-5.6-luna)"
		if c == nil || c.Severity != SevOK || c.Detail != want {
			t.Errorf("row = %+v, want SevOK %q", c, want)
		}
	})

	t.Run("researcher drifted to another model warns with the install fix", func(t *testing.T) {
		env := newEnv(t)
		drifted := strings.ReplaceAll(shippedDoc(t, "researcher", "codex"), "gpt-5.6-luna", "gpt-5.6-terra")
		env.fileContents["/home/u/.codex/researcher.config.toml"] = drifted
		report := Run(context.Background(), env, []string{"codex"})
		c := findCheck(report, "codex", "researcher")
		wantDetail := "~/.codex/researcher.config.toml (model: gpt-5.6-terra) -- pins gpt-5.6-terra; relevo ships gpt-5.6-luna"
		wantFix := "relevo config agents --kind codex --agent researcher --force"
		if c == nil || c.Severity != SevWarn || c.Detail != wantDetail || c.Fix != wantFix {
			t.Errorf("row = %+v, want SevWarn %q fix %q", c, wantDetail, wantFix)
		}
	})

	t.Run("plan-executor carries no pin suffix", func(t *testing.T) {
		env := newEnv(t)
		env.fileContents["/home/u/.codex/plan-executor.config.toml"] = shippedDoc(t, "plan-executor", "codex")
		report := Run(context.Background(), env, []string{"codex"})
		c := findCheck(report, "codex", "plan-executor")
		want := "~/.codex/plan-executor.config.toml"
		if c == nil || c.Severity != SevOK || c.Detail != want {
			t.Errorf("row = %+v, want SevOK %q", c, want)
		}
	})
}

func TestDoctorChecksOnlyTheDefinitionsGiven(t *testing.T) {
	env := newFakeEnvForKind(t, "claude")
	env.existingFiles = map[string]bool{}

	report := Run(context.Background(), env, []string{"claude"},
		WithDefinitions(map[string][]string{"claude": {"plan-executor", "researcher"}}))

	for _, name := range []string{"plan-executor", "researcher"} {
		c := findCheck(report, "claude", name)
		if c == nil {
			t.Fatalf("no %s row for claude", name)
		}
		if c.Severity != SevWarn {
			t.Errorf("%s severity = %v, want warn (file missing)", name, c.Severity)
		}
	}
	if c := findCheck(report, "claude", "reviewer"); c != nil {
		t.Errorf("reviewer row present although no candidate on claude can select it: %+v", *c)
	}
}

func TestDoctorKindAbsentFromDefinitionsKeepsEveryRow(t *testing.T) {
	env := newFakeEnvForKind(t, "claude")
	env.existingFiles = map[string]bool{}

	report := Run(context.Background(), env, []string{"claude"},
		WithDefinitions(map[string][]string{"opencode": {"plan-executor"}}))

	for _, name := range []string{"plan-executor", "researcher", "reviewer", "librarian"} {
		if findCheck(report, "claude", name) == nil {
			t.Errorf("no %s row for claude; a kind absent from the map must keep every shipped definition", name)
		}
	}
}

// findUsageCheck matches usage rows by Name and a Detail substring.
func findUsageCheck(rep Report, name, detailSub string) (Check, bool) {
	for _, c := range rep.Checks {
		if c.Name == name && strings.Contains(c.Detail, detailSub) {
			return c, true
		}
	}
	return Check{}, false
}

func TestUsageChecks(t *testing.T) {
	t.Run("sqlite3 missing with opencode configured", func(t *testing.T) {
		env := &fakeEnv{lookPaths: map[string]string{}, existingFiles: map[string]bool{}}
		rep := Run(context.Background(), env, nil, WithUsage(nil, true))
		c, ok := findUsageCheck(rep, "sqlite3", "")
		if !ok || c.Severity != SevWarn {
			t.Errorf("want a warn row for sqlite3: %+v", rep.Checks)
		}
		if !strings.Contains(c.Detail, "background wait") {
			t.Errorf("Detail = %q, want it to say an opencode mastermind's reports wait for the background wait", c.Detail)
		}
		if strings.Contains(c.Detail, "pane") {
			t.Errorf("Detail = %q, want it to name no pane", c.Detail)
		}
	})
	t.Run("sqlite3 not needed without opencode", func(t *testing.T) {
		env := &fakeEnv{lookPaths: map[string]string{}, existingFiles: map[string]bool{}}
		rep := Run(context.Background(), env, nil, WithUsage(nil, false))
		if _, ok := findUsageCheck(rep, "sqlite3", ""); ok {
			t.Error("no opencode candidate: no sqlite3 row")
		}
	})
	t.Run("prices absent is ok, stale is warn, malformed is warn", func(t *testing.T) {
		env := &fakeEnv{lookPaths: map[string]string{}, existingFiles: map[string]bool{}}
		rep := Run(context.Background(), env, nil, WithUsage(nil, false))
		if c, ok := findUsageCheck(rep, "prices", "default"); !ok || c.Severity != SevOK {
			t.Errorf("absent prices section: want an OK row naming the default: %+v", rep.Checks)
		}

		rep = Run(context.Background(), env, nil, WithUsage([]byte(`{"as_of":"2020-01-01","models":{}}`), false))
		if c, ok := findUsageCheck(rep, "prices", "2020-01-01"); !ok || c.Severity != SevWarn {
			t.Errorf("stale as_of: want a warn row: %+v", rep.Checks)
		}

		rep = Run(context.Background(), env, nil, WithUsage([]byte(`{"models": 5}`), false))
		if c, ok := findUsageCheck(rep, "prices", "does not validate"); !ok || c.Severity != SevWarn {
			t.Errorf("malformed: want a warn row: %+v", rep.Checks)
		}
	})
}

func TestExtraChecksAppended(t *testing.T) {
	env := &fakeEnv{lookPaths: map[string]string{}, existingFiles: map[string]bool{}}
	extra := []Check{
		{Group: "", Name: "servers", Severity: SevOK, Detail: "zen: enrolled as laptop"},
	}

	rep := Run(context.Background(), env, nil, WithUsage(nil, false), WithExtraChecks(extra))

	if len(rep.Checks) == 0 {
		t.Fatal("report has no checks")
	}
	last := rep.Checks[len(rep.Checks)-1]
	if last.Name != "servers" || last.Detail != "zen: enrolled as laptop" {
		t.Fatalf("last check = %+v, want the extra check appended after every check Run built (including usage)", last)
	}
	if c := findCheck(rep, "", "prices"); c == nil {
		t.Fatal("usage's own prices check must still run alongside an extra check")
	}
}

func TestClassifyCheck(t *testing.T) {
	tests := []struct {
		name       string
		st         classify.Status
		wantSev    Severity
		wantDetail string
		wantFix    string
	}{
		{name: "unconfigured", st: classify.Status{Configured: false},
			wantSev: SevOK, wantDetail: "regex only (no classify block in policy.json)"},
		{name: "configured with env key", st: classify.Status{Configured: true, Model: "jev-latest", KeySource: "env"},
			wantSev: SevOK, wantDetail: "jev-latest; key from TYPESAFE_API_KEY; not passed to builders"},
		{name: "configured with db key", st: classify.Status{Configured: true, Model: "jev-custom", KeySource: "db"},
			wantSev: SevOK, wantDetail: "jev-custom; key from the database"},
		{name: "configured with missing key", st: classify.Status{Configured: true, Model: "jev-latest"},
			wantSev: SevWarn, wantDetail: "jev-latest configured but no classifier key; the daemon falls back to regex",
			wantFix: "set TYPESAFE_API_KEY for the daemon, or store a key in the database"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := ClassifyCheck(tc.st)
			if c.Name != "classify" || c.Severity != tc.wantSev || !strings.Contains(c.Detail, tc.wantDetail) {
				t.Errorf("row = %+v, want name classify, severity %v, detail containing %q", c, tc.wantSev, tc.wantDetail)
			}
			if !strings.Contains(c.Fix, tc.wantFix) {
				t.Errorf("Fix = %q, want it to contain %q", c.Fix, tc.wantFix)
			}
		})
	}
}

func TestOpencodeAllowlistRow(t *testing.T) {
	const stateRoot = "/fake/home/.local/state/relevo"

	env := newFakeEnvForKind(t, "opencode")
	env.existingFiles = map[string]bool{}
	env.fileContents = map[string]string{}

	rep := Run(context.Background(), env, []string{"opencode"}, WithStateRoot(stateRoot))
	c := findCheck(rep, "opencode", "external_directory")
	if c == nil {
		t.Fatalf("no external_directory row for opencode: %+v", rep.Checks)
	}
	if c.Severity != SevWarn {
		t.Errorf("severity = %v, want warn (no opencode config file)", c.Severity)
	}

	other := newFakeEnvForKind(t, "claude")
	other.existingFiles = map[string]bool{}
	other.fileContents = map[string]string{}
	rep = Run(context.Background(), other, []string{"claude"}, WithStateRoot(stateRoot))
	for _, c := range rep.Checks {
		if c.Name == "external_directory" {
			t.Errorf("claude must not carry the opencode allowlist row: %+v", c)
		}
	}
}

// releaseArchiveFix builds the KindRelease fix text from this platform's
// own URLs, so the table row below passes on any platform.
func releaseArchiveFix(latest string) string {
	archive, checksums := release.AssetURLs(latest, runtime.GOOS, runtime.GOARCH)
	return fmt.Sprintf("relevo update (or download %s, check it against %s, and replace this relevo binary with the one inside)", archive, checksums)
}

func TestDoctorReleaseCheck(t *testing.T) {
	fe := func(running, latest string, kind release.Kind) fakeEnv {
		return fakeEnv{releaseRunning: running, releaseLatest: latest, releaseOK: true, releaseKind: kind}
	}
	tests := []struct {
		name         string
		env          fakeEnv
		wantSeverity Severity
		wantDetail   string
		wantFix      string
	}{
		{name: "no cache: not checked", env: fakeEnv{}, wantSeverity: SevOK, wantDetail: "not checked"},
		{name: "unparseable running side: not checked", env: fe("(devel)", "v0.7.0", release.KindLocalBuild), wantSeverity: SevOK, wantDetail: "not checked"},
		{name: "unparseable latest side: not checked", env: fe("v0.6.0", "not-a-tag", release.KindGoInstall), wantSeverity: SevOK, wantDetail: "not checked"},
		{name: "unknown install kind: not checked", env: fe("v0.6.0", "v0.7.0", release.KindUnknown), wantSeverity: SevOK, wantDetail: "not checked"},
		{name: "local build: nothing to update to", env: fe("v0.7.0-8-gbd8aed0", "v0.8.0", release.KindLocalBuild), wantSeverity: SevOK, wantDetail: "local build v0.7.0-8-gbd8aed0; nothing to update to"},
		{name: "latest not newer: current", env: fe("v0.7.0", "v0.7.0", release.KindGoInstall), wantSeverity: SevOK, wantDetail: "v0.7.0 is current"},
		{name: "behind a go install: go install", env: fe("v0.6.0", "v0.7.0", release.KindGoInstall), wantSeverity: SevWarn, wantDetail: "v0.6.0 is behind v0.7.0", wantFix: "go install github.com/fuad-daoud/relevo/cmd/relevo@latest"},
		{name: "behind a release binary: archive and checksums", env: fe("v0.8.0", "v0.9.0", release.KindRelease), wantSeverity: SevWarn, wantDetail: "v0.8.0 is behind v0.9.0", wantFix: releaseArchiveFix("v0.9.0")},
		{name: "release binary at the latest tag: current", env: fe("v0.9.0", "v0.9.0", release.KindRelease), wantSeverity: SevOK, wantDetail: "v0.9.0 is current"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := tc.env
			rep := Run(context.Background(), &env, nil)

			c := findCheck(rep, "", "release")
			if c == nil {
				t.Fatal("release check not found in report")
			}
			if c.Severity == SevFail {
				t.Errorf("release severity = SevFail; a stale relevo runs fine")
			}
			if c.Severity != tc.wantSeverity {
				t.Errorf("release severity = %v, want %v (detail %q)", c.Severity, tc.wantSeverity, c.Detail)
			}
			if c.Detail != tc.wantDetail {
				t.Errorf("release detail = %q, want %q", c.Detail, tc.wantDetail)
			}
			if c.Fix != tc.wantFix {
				t.Errorf("release fix = %q, want %q", c.Fix, tc.wantFix)
			}
		})
	}
}

func TestReleaseFix(t *testing.T) {
	tests := []struct {
		name   string
		kind   release.Kind
		latest string
		goos   string
		goarch string
		want   string
	}{
		{
			name:   "release names the archive and checksums",
			kind:   release.KindRelease,
			latest: "v0.9.0",
			goos:   "linux",
			goarch: "amd64",
			want:   "relevo update (or download https://github.com/fuad-daoud/relevo/releases/download/v0.9.0/relevo_v0.9.0_linux_amd64.tar.gz, check it against https://github.com/fuad-daoud/relevo/releases/download/v0.9.0/checksums.txt, and replace this relevo binary with the one inside)",
		},
		{
			name:   "go install keeps its command",
			kind:   release.KindGoInstall,
			latest: "v0.9.0",
			goos:   "linux",
			goarch: "amd64",
			want:   "go install github.com/fuad-daoud/relevo/cmd/relevo@latest",
		},
		{
			name:   "unknown has no fix",
			kind:   release.KindUnknown,
			latest: "v0.9.0",
			goos:   "linux",
			goarch: "amd64",
			want:   "",
		},
		{
			name:   "local build has no fix",
			kind:   release.KindLocalBuild,
			latest: "v0.9.0",
			goos:   "linux",
			goarch: "amd64",
			want:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := releaseFix(tc.kind, tc.latest, tc.goos, tc.goarch); got != tc.want {
				t.Errorf("releaseFix(%q, %q, %q, %q) = %q, want %q", tc.kind, tc.latest, tc.goos, tc.goarch, got, tc.want)
			}
		})
	}
}

func TestDoctorDaemonVersionStates(t *testing.T) {
	tests := []struct {
		name         string
		info         store.DaemonInfo
		ok           bool
		wantSeverity Severity
		wantDetail   string
		wantFix      string
	}{
		{
			name:         "missing record",
			ok:           false,
			wantSeverity: SevWarn,
			wantDetail:   "running, but started before relevo recorded its version: it will not follow upgrades until restarted once",
			wantFix:      "systemctl --user restart relevo.service, or make service",
		},
		{
			name: "refused binary",
			info: store.DaemonInfo{
				Version:      "v1",
				Exe:          "/usr/local/bin/relevo",
				ReexecFailed: &store.ReexecFailure{Reason: "policy.json: unknown field"},
			},
			ok:           true,
			wantSeverity: SevWarn,
			wantDetail:   "runs v1; the relevo binary at /usr/local/bin/relevo failed preflight (policy.json: unknown field) and was not loaded",
			wantFix:      "fix the error above; the daemon retries when the file changes",
		},
		{
			name:         "version differs",
			info:         store.DaemonInfo{Version: "v1"},
			ok:           true,
			wantSeverity: SevOK,
			wantDetail:   "runs v1; switching to v2 within seconds",
		},
		{
			name:         "equal",
			info:         store.DaemonInfo{Version: "v2"},
			ok:           true,
			wantSeverity: SevOK,
			wantDetail:   "running v2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := &fakeEnv{daemonRunning: true, daemonInfo: tc.info, daemonInfoOK: tc.ok, releaseRunning: "v2"}
			c := findCheck(Run(context.Background(), env, nil), "", "daemon")
			if c == nil {
				t.Fatal("no daemon row in the report")
			}
			if c.Severity != tc.wantSeverity {
				t.Errorf("severity = %v, want %v", c.Severity, tc.wantSeverity)
			}
			if !strings.Contains(c.Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to contain %q", c.Detail, tc.wantDetail)
			}
			if tc.wantFix != "" && !strings.Contains(c.Fix, tc.wantFix) {
				t.Errorf("fix = %q, want it to contain %q", c.Fix, tc.wantFix)
			}
		})
	}
}

func TestDoctorDaemonRowsUnchanged(t *testing.T) {
	stopped := findCheck(Run(context.Background(), &fakeEnv{daemonRunning: false}, nil), "", "daemon")
	if stopped == nil {
		t.Fatal("no daemon row for a stopped daemon")
	}
	if stopped.Severity != SevWarn || stopped.Detail != "not running" || stopped.Fix != "relevo daemon" {
		t.Errorf("stopped daemon row = %+v, want Warn / not running / relevo daemon", *stopped)
	}

	failed := findCheck(Run(context.Background(), &fakeEnv{daemonErr: errors.New("nope")}, nil), "", "daemon")
	if failed == nil {
		t.Fatal("no daemon row for a failed probe")
	}
	if failed.Severity != SevWarn || failed.Detail != "probe error: nope" || failed.Fix != "relevo daemon" || !failed.ProbeFailed {
		t.Errorf("probe error row = %+v, want Warn / probe error: nope / relevo daemon / ProbeFailed", *failed)
	}
}

func TestCustomRoleRow(t *testing.T) {
	const customPath = "/fake/home/.claude/agents/my-executor.md"

	t.Run("missing", func(t *testing.T) {
		env := newFakeEnvForKind(t, "claude")
		env.existingFiles = map[string]bool{}

		rep := Run(context.Background(), env, []string{"claude"},
			WithDefinitions(map[string][]string{"claude": {"my-executor", "plan-executor"}}))

		c := findCheck(rep, "claude", "my-executor")
		if c == nil {
			t.Fatal("a custom definition in scope needs a row")
		}
		if c.Severity != SevWarn {
			t.Errorf("missing custom row severity = %v, want SevWarn", c.Severity)
		}
		if !strings.Contains(c.Detail, "(custom)") {
			t.Errorf("detail = %q, want it marked custom", c.Detail)
		}
		wantFix := "run relevo config agents --kind claude, or install your agent definition at ~/.claude/agents/my-executor.md"
		if c.Fix != wantFix {
			t.Errorf("fix = %q, want %q", c.Fix, wantFix)
		}

		shipped := findCheck(rep, "claude", "plan-executor")
		if shipped == nil || shipped.Severity != SevWarn {
			t.Fatalf("shipped plan-executor row = %+v, want a missing-file warn", shipped)
		}
		if shipped.Fix != "relevo config agents --kind claude --agent plan-executor" {
			t.Errorf("shipped fix = %q, want relevo config agents", shipped.Fix)
		}
		if strings.Contains(shipped.Detail, "(custom)") {
			t.Errorf("shipped detail = %q, want no custom marker", shipped.Detail)
		}
	})

	t.Run("present", func(t *testing.T) {
		env := newFakeEnvForKind(t, "claude")
		env.existingFiles = map[string]bool{customPath: true}

		rep := Run(context.Background(), env, []string{"claude"},
			WithDefinitions(map[string][]string{"claude": {"my-executor", "plan-executor"}}))

		c := findCheck(rep, "claude", "my-executor")
		if c == nil {
			t.Fatal("a custom definition in scope needs a row")
		}
		if c.Severity != SevOK || c.Detail != "~/.claude/agents/my-executor.md (custom)" {
			t.Errorf("row = %+v, want SevOK and the custom detail", *c)
		}
	})
}
