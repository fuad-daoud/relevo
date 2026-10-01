package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestServeUsageOnNoArgs(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"serve"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if !strings.Contains(string(stderr), "usage: relevo serve") {
		t.Errorf("expected usage on stderr, got %q", string(stderr))
	}
}

func TestServeGCWithoutAbandonedExits2(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"serve", "gc"})
	})

	ce := requireCLIError(t, runErr, codeUsage, "relevo help")
	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if len(stderr) != 0 {
		t.Errorf("expected empty stderr before report, got %q", string(stderr))
	}
	if !strings.Contains(ce.message, "--abandoned") {
		t.Errorf("expected mention of --abandoned, got %q", ce.message)
	}
}

func TestServeUnbindWithoutOwnerExits2(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"serve", "unbind", "some-binding"})
	})

	ce := requireCLIError(t, runErr, codeUsage, "relevo help")
	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if len(stderr) != 0 {
		t.Errorf("expected empty stderr before report, got %q", string(stderr))
	}
	if !strings.Contains(ce.message, "--owner") {
		t.Errorf("expected mention of --owner, got %q", ce.message)
	}
}

// TestShowStateWithoutOwnerExits2 is TestServeLogWithoutOwnerExits2's port
// to the new form (§8): `--state` names the serve root, so `show` refuses it
// without `--owner`, naming --owner, exit 2. The refusal is the frame's coded
// usage error, so the mention is asserted on the code's message rather than on
// a stderr dump.
func TestShowStateWithoutOwnerExits2(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"show", "some-binding", "--state", t.TempDir()})
	})

	ce := requireCLIError(t, runErr, codeUsage, "relevo help")
	if !strings.Contains(ce.message, "--owner") {
		t.Errorf("expected mention of --owner in the refusal, got %q", ce.message)
	}
	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if len(stderr) != 0 {
		t.Errorf("expected nothing on stderr before report, got %q", string(stderr))
	}
}

func TestServeFlagDefaults(t *testing.T) {
	fs, sf := serveFlagSet()

	if err := fs.Parse(nil); err != nil {
		t.Fatalf("Parse(nil): %v", err)
	}

	if sf.listen != ":7777" {
		t.Errorf("default listen = %q, want :7777", sf.listen)
	}
	if sf.state != "" {
		t.Errorf("default state = %q, want empty", sf.state)
	}
	if sf.interval != 2*time.Second {
		t.Errorf("default interval = %v, want 2s", sf.interval)
	}
	if sf.insecureHTTP != false {
		t.Errorf("default insecureHTTP = %v, want false", sf.insecureHTTP)
	}
	if sf.maxBundleBytes != 512<<20 {
		t.Errorf("default maxBundleBytes = %d, want %d", sf.maxBundleBytes, 512<<20)
	}
	if sf.maxBuilders != 0 {
		t.Errorf("default maxBuilders = %d, want 0 (policy/default)", sf.maxBuilders)
	}
}

// TestServeFlagMaxBuilders pins #285's flag: --max-builders parses into
// serveFlags.maxBuilders, which cmdServeRun assigns straight to
// serve.Config.MaxBuilders. This only exercises flag parsing -- no server
// starts, no harness, no systemd (this package's TestMain isolates HOME,
// XDG_CONFIG_HOME and XDG_STATE_HOME already).
func TestServeFlagMaxBuilders(t *testing.T) {
	fs, sf := serveFlagSet()

	if err := fs.Parse([]string{"--max-builders", "3"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if sf.maxBuilders != 3 {
		t.Errorf("maxBuilders = %d, want 3", sf.maxBuilders)
	}
}

// TestScopeFromPolicy pins scopeFromPolicy's defaults and overrides (#244,
// #216, #295): scopes are on unless the resolved block explicitly turns them
// off, a zero CPUWeight defaults to 100, and every other field -- the CPU
// quota included -- passes through.
func TestScopeFromPolicy(t *testing.T) {
	enabledFalse := false
	cases := map[string]struct {
		sc   *policy.ScopePolicy
		want *spawn.ScopeSpec
	}{
		"nil block defaults on": {
			sc:   nil,
			want: &spawn.ScopeSpec{CPUWeight: 100},
		},
		"enabled false is nil": {
			sc:   &policy.ScopePolicy{Enabled: &enabledFalse},
			want: nil,
		},
		"zero weight defaults to 100": {
			sc:   &policy.ScopePolicy{},
			want: &spawn.ScopeSpec{CPUWeight: 100},
		},
		"quota passes through": {
			sc:   &policy.ScopePolicy{CPUQuota: "200%"},
			want: &spawn.ScopeSpec{CPUWeight: 100, CPUQuota: "200%"},
		},
		"gate quota passes through": {
			sc:   &policy.ScopePolicy{CPUQuota: "200%", GateCPUQuota: "300%"},
			want: &spawn.ScopeSpec{CPUWeight: 100, CPUQuota: "200%", GateCPUQuota: "300%"},
		},
		"slice and limits pass through": {
			sc: &policy.ScopePolicy{
				Slice: "relevo.slice", CPUWeight: 200, CPUQuota: "200%", MemoryMax: "2G", TasksMax: 64,
			},
			want: &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 200, CPUQuota: "200%", MemoryMax: "2G", TasksMax: 64},
		},
		"allowed cpus passes through": {
			sc:   &policy.ScopePolicy{CPUQuota: "200%", AllowedCPUs: "0-2"},
			want: &spawn.ScopeSpec{CPUWeight: 100, CPUQuota: "200%", AllowedCPUs: "0-2"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := scopeFromPolicy(c.sc)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("scopeFromPolicy = %+v, want %+v", got, c.want)
			}
		})
	}
}

// TestScopeStatusText pins what the startup line says about the resolved
// scope (#285, #295) and the gate quota suffix (#313).
func TestScopeStatusText(t *testing.T) {
	cases := map[string]struct {
		sc   *spawn.ScopeSpec
		want string
	}{
		"nil is off":            {sc: nil, want: "off"},
		"bare is on":            {sc: &spawn.ScopeSpec{}, want: "on"},
		"quota":                 {sc: &spawn.ScopeSpec{CPUQuota: "200%"}, want: "on (200%)"},
		"slice":                 {sc: &spawn.ScopeSpec{Slice: "relevo.slice"}, want: "on (slice relevo.slice)"},
		"slice and quota":       {sc: &spawn.ScopeSpec{Slice: "relevo.slice", CPUQuota: "200%"}, want: "on (slice relevo.slice, 200%)"},
		"gate only":             {sc: &spawn.ScopeSpec{GateCPUQuota: "300%"}, want: "on (gate 300%)"},
		"quota and gate":        {sc: &spawn.ScopeSpec{CPUQuota: "200%", GateCPUQuota: "300%"}, want: "on (200%, gate 300%)"},
		"slice and gate":        {sc: &spawn.ScopeSpec{Slice: "relevo.slice", GateCPUQuota: "300%"}, want: "on (slice relevo.slice, gate 300%)"},
		"slice, quota and gate": {sc: &spawn.ScopeSpec{Slice: "relevo.slice", CPUQuota: "200%", GateCPUQuota: "300%"}, want: "on (slice relevo.slice, 200%, gate 300%)"},
		"cpus":                  {sc: &spawn.ScopeSpec{AllowedCPUs: "0-2"}, want: "on (cpus 0-2, one per round)"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := scopeStatusText(c.sc); got != c.want {
				t.Errorf("scopeStatusText = %q, want %q", got, c.want)
			}
		})
	}
}

// TestServeTierRuntimeHasClock pins the #226 regression: cmdServeRun's
// startup runtime is only used to log the builder tier, but that chain
// (PickServedCandidate -> Gates) calls rt.Now(), so a runtime without a
// clock panics before relevo serve ever listens. It must build the runtime
// the way cmdServeRun does and call relevo.ServedBuilderTier on it -- no
// shell-outs, no harness, nothing outside t.TempDir().
func TestServeTierRuntimeHasClock(t *testing.T) {
	root := t.TempDir()

	candidatesJSON := `[{"harness":"claude","provider":"t","model":"m","roles":["builder"]}]`
	candidatesPath := filepath.Join(root, "candidates.json")
	if err := os.WriteFile(candidatesPath, []byte(candidatesJSON), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	candidates, err := candidate.Load(candidatesPath)
	if err != nil {
		t.Fatalf("candidate.Load: %v", err)
	}
	pol, err := policy.Load(filepath.Join(root, "policy.json"))
	if err != nil {
		t.Fatalf("policy.Load: %v", err)
	}

	rt := serveTierRuntime(candidates, pol, nil, root, nil)
	if rt.Now == nil {
		t.Fatal("serveTierRuntime returned a Runtime without a clock")
	}
	tier := relevo.ServedBuilderTier(rt) // this is the line that panicked in production
	if tier == "" {
		t.Fatal("expected a tier")
	}
}

// TestServeTierRuntimeCarriesTheRegistry pins that the runtime cmdServeRun
// logs the builder tier from carries the same registry the served rounds
// resolve through, so the log cannot name a tier the rounds do not run at.
func TestServeTierRuntimeCarriesTheRegistry(t *testing.T) {
	root := t.TempDir()
	candidatesJSON := `[{"harness":"claude","provider":"t","model":"m","roles":["builder"]}]`
	candidatesPath := filepath.Join(root, "candidates.json")
	if err := os.WriteFile(candidatesPath, []byte(candidatesJSON), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	set, err := candidate.Load(candidatesPath)
	if err != nil {
		t.Fatalf("candidate.Load: %v", err)
	}
	pol := policy.Policy{MaxTier: "yolo"}
	reg, err := roles.Build(nil, set, pol)
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}

	rt := serveTierRuntime(set, pol, reg, root, nil)
	if rt.Registry != reg {
		t.Fatal("serveTierRuntime dropped the registry: the startup log would resolve the tier without it")
	}
}

func TestServeAdminConfigHasRunnerAndClock(t *testing.T) {
	cfg := serveAdminConfig("/x", nil)
	if cfg.Root != "/x" {
		t.Errorf("Root = %q, want /x", cfg.Root)
	}
	if cfg.Runner == nil {
		t.Error("Runner is nil")
	}
	if cfg.Now == nil {
		t.Error("Now is nil")
	}
}

// TestServeUIRefusesUninitialisedRoot: relevo serve ui resolves its root
// like the other admin verbs, so an uninitialised --state dir fails before
// any tty check (CI-safe: no tty, no harness) and creates nothing.
func TestServeUIRefusesUninitialisedRoot(t *testing.T) {
	dir := t.TempDir()
	err := cmdServeUI([]string{"--state", dir})
	if err == nil {
		t.Fatal("cmdServeUI error = nil, want an uninitialised-root error")
	}
	if !strings.Contains(err.Error(), "no serve state at ") {
		t.Errorf("error = %q, want no serve state message", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "serve")); !os.IsNotExist(statErr) {
		t.Errorf("serve dir exists or stat failed: %v", statErr)
	}
}

func TestServeStatusRefusesUninitialisedRoot(t *testing.T) {
	dir := t.TempDir()
	err := cmdServeStatus([]string{"--state", dir})
	if err == nil {
		t.Fatal("cmdServeStatus error = nil, want an uninitialised-root error")
	}
	if !strings.Contains(err.Error(), "no serve state at ") {
		t.Errorf("error = %q, want no serve state message", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, "serve")) {
		t.Errorf("error = %q, want root %q", err, filepath.Join(dir, "serve"))
	}
	if _, statErr := os.Stat(filepath.Join(dir, "serve", "tmp")); !os.IsNotExist(statErr) {
		t.Errorf("serve/tmp exists or stat failed: %v", statErr)
	}
}

// TestGateServeRefusesUninitialisedRoot: `relevo gate --serve` resolves its
// root like the other admin verbs, so an uninitialised --state dir fails at
// adminRoot before anything else can run. CI-safe: it reaches no harness.
func TestGateServeRefusesUninitialisedRoot(t *testing.T) {
	dir := t.TempDir()
	_, _, runErr := captureOutput(t, func() error {
		return run([]string{"gate", "--serve", "--state", dir})
	})
	if runErr == nil {
		t.Fatal("run error = nil, want an uninitialised-root error")
	}
	if !strings.Contains(runErr.Error(), "no serve state at ") {
		t.Errorf("error = %q, want no serve state message", runErr)
	}
	if !strings.Contains(runErr.Error(), filepath.Join(dir, "serve")) {
		t.Errorf("error = %q, want root %q", runErr, filepath.Join(dir, "serve"))
	}
	if _, statErr := os.Stat(filepath.Join(dir, "serve", "tmp")); !os.IsNotExist(statErr) {
		t.Errorf("serve/tmp exists or stat failed: %v", statErr)
	}
}

// TestServeGateSubverbsWereRemoved pins D1: `serve gates`, `serve available`
// and `serve unavailable` return the usage-coded refusal naming `relevo gate
// --serve`, and exit 2.
func TestServeGateSubverbsWereRemoved(t *testing.T) {
	for _, sub := range []string{"gates", "available", "unavailable"} {
		t.Run(sub, func(t *testing.T) {
			runErr := run([]string{"serve", sub})
			ce := requireCLIError(t, runErr, codeUsage, "relevo help")
			if !strings.Contains(ce.message, "relevo gate --serve") {
				t.Errorf("message = %q, want it to name relevo gate --serve", ce.message)
			}
			var ec exitCodeErr
			if !errors.As(runErr, &ec) || ec.code != 2 {
				t.Fatalf("run = %v, want exit code 2", runErr)
			}
		})
	}
}

// TestServeReadSubverbsWereRemoved pins §4.3: `serve log`, `serve show` and
// `serve tab` return the usage-coded refusal that names the form replacing
// them, and exit 2.
func TestServeReadSubverbsWereRemoved(t *testing.T) {
	cases := []struct {
		sub  string
		want string
	}{
		{"log", "relevo show <name> --owner <label> --log"},
		{"show", "relevo show <name> --owner <label>"},
		{"tab", "relevo history --tab --owner <label|all>"},
	}
	for _, c := range cases {
		t.Run(c.sub, func(t *testing.T) {
			runErr := run([]string{"serve", c.sub})
			ce := requireCLIError(t, runErr, codeUsage, "relevo help")
			if !strings.Contains(ce.message, c.want) {
				t.Errorf("message = %q, want it to name %q", ce.message, c.want)
			}
			var ec exitCodeErr
			if !errors.As(runErr, &ec) || ec.code != 2 {
				t.Fatalf("run = %v, want exit code 2", runErr)
			}
		})
	}
}

func TestServeFlagAfterPositionalIsHonoured(t *testing.T) {
	dir := t.TempDir()
	_, _, runErr := captureOutput(t, func() error {
		return run([]string{"gate", "--serve", "sometoken", "--state", dir})
	})
	if runErr == nil {
		t.Fatal("run error = nil, want an uninitialised-root error")
	}
	if !strings.Contains(runErr.Error(), dir) {
		t.Errorf("error = %q, want it to contain %q", runErr, dir)
	}
}

// runServeInit runs `relevo serve init` with args and returns its stdout. It
// is fixture-only: init spawns nothing and reaches nothing but the state
// directory and the machine database.
func runServeInit(t *testing.T, args ...string) (string, error) {
	t.Helper()
	stdout, _, err := captureOutput(t, func() error {
		return run(append([]string{"serve", "init"}, args...))
	})
	return string(stdout), err
}

// serveSecret reads a secret from the machine database the serve verbs use.
func serveSecret(t *testing.T, name string) []byte {
	t.Helper()
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	d, err := openDB(filepath.Join(root, "relevo.db"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer func() { _ = d.Close() }()

	val, ok, err := d.SecretGet(name)
	if err != nil {
		t.Fatalf("SecretGet(%s): %v", name, err)
	}
	if !ok {
		t.Fatalf("secret %s is missing", name)
	}
	return val
}

// TestServeInitFreshLeavesARoot pins case 1 of the plan: a fresh run (no TLS
// secrets, no root) creates <state>/serve/bindings before the secrets and
// prints the fingerprint line, exit 0. Fixture-only: local key generation,
// no harness, no network, no server.
func TestServeInitFreshLeavesARoot(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	stdout, err := runServeInit(t, "--state", state)
	if err != nil {
		t.Fatalf("runServeInit: %v", err)
	}
	first := strings.SplitN(stdout, "\n", 2)[0]
	if !regexp.MustCompile(`^fingerprint sha256:[0-9a-f]{64}$`).MatchString(first) {
		t.Errorf("stdout first line = %q, want %q", first, "fingerprint sha256:<64 hex>")
	}

	info, err := os.Stat(filepath.Join(state, "serve", "bindings"))
	if err != nil {
		t.Fatalf("stat <state>/serve/bindings: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("<state>/serve/bindings is not a directory")
	}
	if len(serveSecret(t, "serve.tls.key")) == 0 {
		t.Error("machine DB is missing serve.tls.key")
	}
	if len(serveSecret(t, "serve.tls.cert")) == 0 {
		t.Error("machine DB is missing serve.tls.cert")
	}
}

// TestServeInitRepairsAMissingRoot pins case 2 of the plan (the laptop's
// case): with the TLS identity present and the root removed, the next run
// leaves the identity untouched, recreates <root>/bindings, and prints
// `already initialised; fingerprint <fp1>`.
func TestServeInitRepairsAMissingRoot(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	first, err := runServeInit(t, "--state", state)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	fp1 := strings.TrimPrefix(strings.SplitN(first, "\n", 2)[0], "fingerprint ")
	keyBefore := serveSecret(t, "serve.tls.key")
	certBefore := serveSecret(t, "serve.tls.cert")

	if err := os.RemoveAll(filepath.Join(state, "serve")); err != nil {
		t.Fatalf("RemoveAll <state>/serve: %v", err)
	}

	stdout, err := runServeInit(t, "--state", state)
	if err != nil {
		t.Fatalf("repair run: %v", err)
	}
	if !strings.HasPrefix(stdout, "already initialised; fingerprint "+fp1+"\n") {
		t.Errorf("repair run stdout = %q, want already initialised with %s", stdout, fp1)
	}

	info, err := os.Stat(filepath.Join(state, "serve", "bindings"))
	if err != nil {
		t.Fatalf("stat bindings after repair: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("bindings is not a directory after repair")
	}
	if got := serveSecret(t, "serve.tls.key"); !bytes.Equal(got, keyBefore) {
		t.Error("key bytes changed on the repair run")
	}
	if got := serveSecret(t, "serve.tls.cert"); !bytes.Equal(got, certBefore) {
		t.Error("cert bytes changed on the repair run")
	}
}

// TestServeInitSecondRunChangesNothing pins case 3 of the plan: with the root
// present, the second run exits 0 with the same line, and a sentinel inside
// bindings and the key/cert bytes are unchanged.
func TestServeInitSecondRunChangesNothing(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	first, err := runServeInit(t, "--state", state)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	fp := strings.TrimPrefix(strings.SplitN(first, "\n", 2)[0], "fingerprint ")

	sentinel := filepath.Join(state, "serve", "bindings", "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep me\n"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	keyBefore := serveSecret(t, "serve.tls.key")
	certBefore := serveSecret(t, "serve.tls.cert")

	stdout, err := runServeInit(t, "--state", state)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !strings.HasPrefix(stdout, "already initialised; fingerprint "+fp+"\n") {
		t.Errorf("second run stdout = %q, want already initialised with %s", stdout, fp)
	}
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if string(got) != "keep me\n" {
		t.Errorf("sentinel = %q, want it untouched", got)
	}
	if got := serveSecret(t, "serve.tls.key"); !bytes.Equal(got, keyBefore) {
		t.Error("key bytes changed on the second run")
	}
	if got := serveSecret(t, "serve.tls.cert"); !bytes.Equal(got, certBefore) {
		t.Error("cert bytes changed on the second run")
	}
}

// TestServeInitRootFailureIsAnError pins case 4 of the plan: a root that
// cannot be created makes cmdServeInit return before InitTLS, with no success
// line and -- on the fresh path -- no secrets written.
func TestServeInitRootFailureIsAnError(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", t.TempDir())

		// A regular file where the root must be: MkdirAll(<state>/serve/bindings) fails.
		if err := os.WriteFile(filepath.Join(state, "serve"), []byte("not a dir\n"), 0o644); err != nil {
			t.Fatalf("write file at root: %v", err)
		}

		stdout, err := runServeInit(t, "--state", state)
		if err == nil {
			t.Fatal("runServeInit = nil, want an error when the root cannot be created")
		}
		if strings.Contains(stdout, "fingerprint") {
			t.Errorf("stdout = %q, want no success line", stdout)
		}

		root, err := store.DefaultRoot()
		if err != nil {
			t.Fatalf("DefaultRoot: %v", err)
		}
		d, err := openDB(filepath.Join(root, "relevo.db"))
		if err != nil {
			t.Fatalf("openDB: %v", err)
		}
		defer func() { _ = d.Close() }()
		if _, ok, err := d.SecretGet("serve.tls.key"); err != nil {
			t.Fatalf("SecretGet(serve.tls.key): %v", err)
		} else if ok {
			t.Error("fresh failure run wrote serve.tls.key")
		}
	})

	t.Run("already initialised", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", t.TempDir())

		if _, err := runServeInit(t, "--state", state); err != nil {
			t.Fatalf("seed run: %v", err)
		}
		// Replace <root>/bindings (a directory) with a regular file.
		bindings := filepath.Join(state, "serve", "bindings")
		if err := os.RemoveAll(bindings); err != nil {
			t.Fatalf("RemoveAll bindings: %v", err)
		}
		if err := os.WriteFile(bindings, []byte("not a dir\n"), 0o644); err != nil {
			t.Fatalf("write file at bindings: %v", err)
		}

		stdout, err := runServeInit(t, "--state", state)
		if err == nil {
			t.Fatal("runServeInit = nil, want an error when bindings cannot be created")
		}
		if strings.Contains(stdout, "already initialised") {
			t.Errorf("stdout = %q, want no success line", stdout)
		}
	})
}

// TestServeEnrollRefusesUnknownUser pins step 6's refusal: `serve enroll
// --user <name>` with a name that is not on this host is refused with the
// useradd sentence, before anything is written. The name cannot exist, so only
// /etc/passwd is read: no harness, no network.
func TestServeEnrollRefusesUnknownUser(t *testing.T) {
	_, _, runErr := captureOutput(t, func() error {
		return run([]string{"serve", "enroll", "--label", "x", "--key", "ed25519 AAAA", "--user", "relevo-no-such-user-zz"})
	})
	if runErr == nil {
		t.Fatal("serve enroll with an unknown --user must fail")
	}
	if !strings.Contains(runErr.Error(), "useradd --create-home relevo-no-such-user-zz") {
		t.Errorf("error = %q, want it to name the useradd command", runErr)
	}
}

// TestServeRunRefusesUnavailableIsolation pins slice A's one rule at the
// command: a configured serve.isolation=user is refused at startup with
// not_available naming the mode, before any side effect. The wait is bounded:
// if the refusal regresses the command would start a daemon and never return,
// so the test fails on timeout rather than hanging.
func TestServeRunRefusesUnavailableIsolation(t *testing.T) {
	stateHome := t.TempDir()
	configHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)

	policyDir := filepath.Join(configHome, "relevo")
	if err := os.MkdirAll(policyDir, 0o755); err != nil {
		t.Fatalf("mkdir policy dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(policyDir, "policy.json"), []byte(`{"serve":{"isolation":"user"}}`), 0o644); err != nil {
		t.Fatalf("write policy.json: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		// 127.0.0.1:0 and insecure HTTP so a regressed command would not
		// collide with another test's port or need TLS; the bounded wait still
		// fails the test instead of letting the daemon run.
		done <- run([]string{"serve", "--listen", "127.0.0.1:0", "--insecure-http"})
	}()

	select {
	case err := <-done:
		ce := requireCLIError(t, err, codeNotAvailable, "")
		if !strings.Contains(ce.message, "serve.isolation=user") {
			t.Errorf("message = %q, want it to name serve.isolation=user", ce.message)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("relevo serve did not refuse isolation=user within 10s; it may have started a daemon")
	}
}

// TestServeRunRefusesUserModeWithoutRoot pins step 8's rule at the command: a
// configured serve.isolation=user with a non-root euid is refused with
// not_available naming root, before any side effect. Skipped when the test runs
// as root, where user mode is permitted and the daemon would start.
func TestServeRunRefusesUserModeWithoutRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: user mode is permitted")
	}
	stateHome := t.TempDir()
	configHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)

	policyDir := filepath.Join(configHome, "relevo")
	if err := os.MkdirAll(policyDir, 0o755); err != nil {
		t.Fatalf("mkdir policy dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(policyDir, "policy.json"), []byte(`{"serve":{"isolation":"user"}}`), 0o644); err != nil {
		t.Fatalf("write policy.json: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- run([]string{"serve", "--listen", "127.0.0.1:0", "--insecure-http"})
	}()

	select {
	case err := <-done:
		ce := requireCLIError(t, err, codeNotAvailable, "")
		if !strings.Contains(ce.message, "requires root") {
			t.Errorf("message = %q, want it to say root is required", ce.message)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("relevo serve did not refuse isolation=user within 10s; it may have started a daemon")
	}
}
