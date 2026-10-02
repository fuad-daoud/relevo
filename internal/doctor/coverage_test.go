package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/release"
)

// TestSeverityString pins the render string of every severity, including the
// unknown fallback a negative or out-of-range value reads as.
func TestSeverityString(t *testing.T) {
	cases := []struct {
		sev  Severity
		want string
	}{
		{SevOK, "ok"},
		{SevWarn, "warn"},
		{SevFail, "FAIL"},
		{SevInfo, "info"},
		{Severity(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.sev.String(); got != tc.want {
			t.Errorf("Severity(%d).String() = %q, want %q", tc.sev, got, tc.want)
		}
	}
}

// TestReportCounts pins the derived verdict: Failures counts fails, Warnings
// counts warns, and neither counts the others.
func TestReportCounts(t *testing.T) {
	r := Report{Checks: []Check{
		{Severity: SevFail},
		{Severity: SevWarn},
		{Severity: SevWarn},
		{Severity: SevOK},
		{Severity: SevInfo},
	}}
	if got := r.Failures(); got != 1 {
		t.Errorf("Failures = %d, want 1", got)
	}
	if got := r.Warnings(); got != 2 {
		t.Errorf("Warnings = %d, want 2", got)
	}
}

// TestRealEnvNilStore pins the real Env's nil-store answers: no daemon is
// running and no record exists, and NewEnv accepts an absent or present self.
func TestRealEnvNilStore(t *testing.T) {
	ctx := context.Background()
	env := NewEnv(nil)
	if env == nil {
		t.Fatal("NewEnv(nil) = nil, want an Env")
	}
	if running, err := env.DaemonRunning(ctx); running || err != nil {
		t.Errorf("DaemonRunning = (%v, %v), want (false, nil)", running, err)
	}
	if _, ok, err := env.DaemonInfo(); ok || err != nil {
		t.Errorf("DaemonInfo = (_, %v, %v), want ok false and no error", ok, err)
	}

	withSelf := NewEnv(nil, release.Inputs{Version: "1.2.3"})
	if withSelf == nil {
		t.Fatal("NewEnv(nil, Inputs) = nil, want an Env")
	}
}

// TestRealEnvFileAndProcessMethods pins the real Env's filesystem and process
// methods against a temp home: ReadFile, Command, BinaryVersion, Probe,
// LoadManifest and ReleaseState.
func TestRealEnvFileAndProcessMethods(t *testing.T) {
	ctx := context.Background()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	env := NewEnv(nil)

	src := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := env.ReadFile(src); err != nil || string(got) != "hello" {
		t.Errorf("ReadFile = (%q, %v), want hello", got, err)
	}

	if _, err := env.Command(ctx, "true"); err != nil {
		t.Errorf("Command(true) = %v, want nil", err)
	}

	stub := filepath.Join(t.TempDir(), "version-stub")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho v9.9.9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := env.BinaryVersion(ctx, stub); err != nil || got != "v9.9.9" {
		t.Errorf("BinaryVersion = (%q, %v), want v9.9.9", got, err)
	}

	if err := env.Probe(t.TempDir()); err != nil {
		t.Errorf("Probe(temp dir) = %v, want nil", err)
	}
	notDir := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := env.Probe(filepath.Join(notDir, "sub")); err == nil {
		t.Error("Probe(under a file) = nil, want an error")
	}

	if m, err := env.LoadManifest(); err != nil || len(m) != 0 {
		t.Errorf("LoadManifest = (%v, %v), want an empty map and no error", m, err)
	}

	running, latest, ok, kind := env.ReleaseState()
	if running != "" || latest != "" || ok {
		t.Errorf("ReleaseState = (%q, %q, %v), want empty and ok false", running, latest, ok)
	}
	if kind == "" {
		t.Error("ReleaseState kind = empty, want a classified install kind")
	}
}

// TestRoleInstallEnvIsDryRun pins the doctor's dry-run install env: it delegates
// the read-only probes and never writes a user's files.
func TestRoleInstallEnvIsDryRun(t *testing.T) {
	fe := &fakeEnv{
		lookPaths:     map[string]string{"bin": "/fake/bin"},
		homeDir:       "/fake/home",
		existingFiles: map[string]bool{"/fake/present": true},
		fileContents:  map[string]string{"/fake/present": "content"},
		manifest:      map[string]string{"shipped": "sum"},
	}
	rie := roleInstallEnv{env: fe, manifest: map[string]string{"own": "sum2"}}

	if p, err := rie.LookPath("bin"); err != nil || p != "/fake/bin" {
		t.Errorf("LookPath = (%q, %v), want /fake/bin", p, err)
	}
	if p, err := rie.HomePath("rel"); err != nil || p != "/fake/home/rel" {
		t.Errorf("HomePath = (%q, %v), want /fake/home/rel", p, err)
	}
	if err := rie.MkdirAll("/fake/new"); err != nil {
		t.Errorf("MkdirAll = %v, want nil (never writes)", err)
	}
	if err := rie.WriteFile("/fake/new", []byte("x")); err != nil {
		t.Errorf("WriteFile = %v, want nil (never writes)", err)
	}
	if err := rie.SaveManifest(map[string]string{"a": "b"}); err != nil {
		t.Errorf("SaveManifest = %v, want nil (never writes)", err)
	}

	if m, err := rie.LoadManifest(); err != nil || m["own"] != "sum2" {
		t.Errorf("LoadManifest = (%v, %v), want the caller's manifest", m, err)
	}

	if got, err := rie.ReadFile("/fake/present"); err != nil || string(got) != "content" {
		t.Errorf("ReadFile(present) = (%q, %v), want content", got, err)
	}
	if _, err := rie.ReadFile("/fake/missing"); err == nil {
		t.Error("ReadFile(missing) = nil error, want fs.ErrNotExist")
	}
}

// TestTenantStatRealFile pins tenantStat against a real file, which also
// exercises the platform's statOwner: the permission bits come back and no
// error is reported.
func TestTenantStatRealFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner-root")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o710); err != nil {
		t.Fatal(err)
	}
	_, _, mode, err := tenantStat(path)
	if err != nil {
		t.Fatalf("tenantStat = %v, want nil", err)
	}
	if mode != 0o710 {
		t.Errorf("tenantStat mode = %04o, want 0710", mode)
	}
}
