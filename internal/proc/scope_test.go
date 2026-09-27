package proc

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/legacy"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

// scopeWant prepends systemd-run's fixed scope flags to tail, so each row's
// expected order stays explicit.
func scopeWant(tail ...string) []string {
	return append([]string{"systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit=relevo-round-abc12345-foo-1.scope"}, tail...)
}

func TestScopeArgv(t *testing.T) {
	inner := []string{"/bin/sh", "-c", "script", "relevo-supervisor", "bin"}
	cases := map[string]struct {
		spec spawn.ScopeSpec
		want []string
	}{
		"full spec": {
			spec: spawn.ScopeSpec{Unit: "relevo-round-abc12345-foo-1", Slice: "relevo.slice", CPUWeight: 200, MemoryMax: "2G", TasksMax: 50},
			want: scopeWant("--slice=relevo.slice", "-p", "CPUWeight=200", "-p", "MemoryMax=2G", "-p", "TasksMax=50",
				"--", "/bin/sh", "-c", "script", "relevo-supervisor", "bin"),
		},
		"no slice": {
			spec: spawn.ScopeSpec{Unit: "relevo-round-abc12345-foo-1", CPUWeight: 200, MemoryMax: "2G", TasksMax: 50},
			want: scopeWant("-p", "CPUWeight=200", "-p", "MemoryMax=2G", "-p", "TasksMax=50",
				"--", "/bin/sh", "-c", "script", "relevo-supervisor", "bin"),
		},
		"no memory": {
			spec: spawn.ScopeSpec{Unit: "relevo-round-abc12345-foo-1", Slice: "relevo.slice", CPUWeight: 200, TasksMax: 50},
			want: scopeWant("--slice=relevo.slice", "-p", "CPUWeight=200", "-p", "TasksMax=50",
				"--", "/bin/sh", "-c", "script", "relevo-supervisor", "bin"),
		},
		"no tasks": {
			spec: spawn.ScopeSpec{Unit: "relevo-round-abc12345-foo-1", Slice: "relevo.slice", CPUWeight: 200, MemoryMax: "2G"},
			want: scopeWant("--slice=relevo.slice", "-p", "CPUWeight=200", "-p", "MemoryMax=2G",
				"--", "/bin/sh", "-c", "script", "relevo-supervisor", "bin"),
		},
		// The quota sits between the weight and the memory pairs.
		"with quota": {
			spec: spawn.ScopeSpec{Unit: "relevo-round-abc12345-foo-1", Slice: "relevo.slice", CPUWeight: 200, CPUQuota: "200%", MemoryMax: "2G", TasksMax: 50},
			want: scopeWant("--slice=relevo.slice", "-p", "CPUWeight=200", "-p", "CPUQuota=200%", "-p", "MemoryMax=2G", "-p", "TasksMax=50",
				"--", "/bin/sh", "-c", "script", "relevo-supervisor", "bin"),
		},
		// The pin sits between the quota and the memory pairs.
		"with allowed cpus": {
			spec: spawn.ScopeSpec{Unit: "relevo-round-abc12345-foo-1", Slice: "relevo.slice", CPUWeight: 200, CPUQuota: "200%", AllowedCPUs: "2", MemoryMax: "2G", TasksMax: 50},
			want: scopeWant("--slice=relevo.slice", "-p", "CPUWeight=200", "-p", "CPUQuota=200%", "-p", "AllowedCPUs=2", "-p", "MemoryMax=2G", "-p", "TasksMax=50",
				"--", "/bin/sh", "-c", "script", "relevo-supervisor", "bin"),
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := ScopeArgv(c.spec, inner)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ScopeArgv = %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestParseRusageTrailer(t *testing.T) {
	cases := map[string]struct {
		line string
		want spawn.ProcRusage
		ok   bool
	}{
		"both fields":      {"relevo-rusage:cpu_usec=123456 mem_peak=891289600", spawn.ProcRusage{CPUMS: 123, PeakMemBytes: 891289600}, true},
		"cpu only":         {"relevo-rusage:cpu_usec=5000", spawn.ProcRusage{CPUMS: 5}, true},
		"mem only":         {"relevo-rusage:mem_peak=1024", spawn.ProcRusage{PeakMemBytes: 1024}, true},
		"unknown key":      {"relevo-rusage:cpu_usec=1000 foo=bar", spawn.ProcRusage{CPUMS: 1}, true},
		"malformed number": {"relevo-rusage:cpu_usec=notanumber", spawn.ProcRusage{}, true},
		"legacy prefix":    {legacy.RusageTrailer + "cpu_usec=12345 mem_peak=1048576", spawn.ProcRusage{CPUMS: 12, PeakMemBytes: 1048576}, true},
		"legacy cpu only":  {legacy.RusageTrailer + "cpu_usec=5000", spawn.ProcRusage{CPUMS: 5}, true},
		"wrong prefix":     {"something-else:cpu_usec=1000", spawn.ProcRusage{}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := ParseRusageTrailer(c.line)
			if ok != c.ok || got != c.want {
				t.Errorf("ParseRusageTrailer(%q) = %+v, %v; want %+v, %v", c.line, got, ok, c.want, c.ok)
			}
		})
	}
}

// TestRusageFindsTheTrailerLine covers the stream shapes Rusage must read: the
// real supervisor layout, where a blank line separates the rusage and exit
// trailers, the legacy spelling, output after the exit trailer, and no trailer.
func TestRusageFindsTheTrailerLine(t *testing.T) {
	cases := map[string]struct {
		body string
		want spawn.ProcRusage
		ok   bool
	}{
		"real supervisor layout": {
			body: "builder said hi\n" +
				"\n" + spawn.RusageTrailerPrefix + "cpu_usec=19071588 mem_peak=403206144\n" +
				"\n" + spawn.ExitTrailer + "0\n",
			want: spawn.ProcRusage{CPUMS: 19071, PeakMemBytes: 403206144},
			ok:   true,
		},
		"trailer before the exit trailer": {
			body: "builder output\n\n" + spawn.RusageTrailerPrefix + "cpu_usec=12345 mem_peak=1048576\n" + spawn.ExitTrailer + "0\n",
			want: spawn.ProcRusage{CPUMS: 12, PeakMemBytes: 1048576},
			ok:   true,
		},
		"legacy stream": {
			body: "builder output\n\n" + legacy.RusageTrailer + "cpu_usec=12345 mem_peak=1048576\n\n" + legacy.ExitTrailer + "3\n",
			want: spawn.ProcRusage{CPUMS: 12, PeakMemBytes: 1048576},
			ok:   true,
		},
		"output after the exit trailer": {
			body: "builder said hi\n" +
				"\n" + spawn.RusageTrailerPrefix + "cpu_usec=19071588 mem_peak=403206144\n" +
				"\n" + spawn.ExitTrailer + "0\n" +
				"stray output after exit\n",
			want: spawn.ProcRusage{CPUMS: 19071, PeakMemBytes: 403206144},
			ok:   true,
		},
		"no trailer": {
			body: "builder output\n\n" + spawn.ExitTrailer + "0\n",
			want: spawn.ProcRusage{},
			ok:   false,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := New()
			path := filepath.Join(t.TempDir(), strings.ReplaceAll(name, " ", "_")+".jsonl")
			if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			got, ok := r.Rusage(context.Background(), spawn.ProcHandle{}, path)
			if ok != c.ok || got != c.want {
				t.Errorf("Rusage = %+v, %v; want %+v, %v", got, ok, c.want, c.ok)
			}
		})
	}
}

// TestScopeActive pins the ActiveState values that count as loaded, and that a
// missing systemctl is not an error.
func TestScopeActive(t *testing.T) {
	cases := []struct {
		name string
		stub string
		want bool
	}{
		{"active", "#!/bin/sh\necho active\n", true},
		{"reloading", "#!/bin/sh\necho reloading\n", true},
		{"inactive", "#!/bin/sh\necho inactive\n", false},
		{"failed", "#!/bin/sh\necho failed\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(tc.stub), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			got, err := New().ScopeActive(context.Background(), "relevo-round-x-1")
			if err != nil || got != tc.want {
				t.Errorf("ScopeActive = %v, %v; want %v, nil", got, err, tc.want)
			}
		})
	}
	t.Run("no systemctl", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		got, err := New().ScopeActive(context.Background(), "relevo-round-x-1")
		if err != nil || got {
			t.Errorf("ScopeActive without systemctl = %v, %v; want false, nil", got, err)
		}
	})
}

// stubLog reads a stub's argv log, failing the test when it cannot.
func stubLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestProbeStubs puts a fake systemd-run on PATH so the probes never call the
// real one (CI has no systemd): an accepting stub, and refusals reproducing
// the stderr line systemd-run prints.
func TestProbeStubs(t *testing.T) {
	cases := []struct {
		name      string
		cpus      string
		stub      string
		wantInErr string
	}{
		{"scope probe succeeds", "", acceptingStub, ""},
		{"scope probe reports stderr", "", refusingScopeStub, "Failed to start transient scope unit: Permission denied"},
		{"pin probe succeeds", "2", acceptingStub, ""},
		{"pin probe reports stderr", "2", refusingPinStub, "Failed to set AllowedCPUs: Permission denied"},
		{"probe without stderr reports its exit status", "", exitingStub, "exit status 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeStub(t, dir, tc.stub)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

			var err error
			if tc.cpus == "" {
				err = ProbeScopes(context.Background(), "relevo.slice")
			} else {
				err = ProbeAllowedCPUs(context.Background(), "relevo.slice", tc.cpus)
			}
			if tc.wantInErr == "" {
				if err != nil {
					t.Fatalf("probe: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantInErr) {
				t.Fatalf("probe error = %v; want it to contain the stub's stderr line", err)
			}
			if tc.cpus != "" && !strings.Contains(err.Error(), "AllowedCPUs="+tc.cpus) {
				t.Errorf("probe error = %v; want it to name AllowedCPUs=%s", err, tc.cpus)
			}
		})
	}
}

// TestFirstNonEmptyLine pins the pure trimming logic shared by the scope
// probes: it returns the first non-empty trimmed line, which is the Result
// value systemctl prints. This test does not run systemctl.
func TestFirstNonEmptyLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain result", "oom-kill\n", "oom-kill"},
		{"success", "success\n", "success"},
		{"leading space", "  oom-kill  \n", "oom-kill"},
		{"empty output", "", ""},
		{"blank lines then result", "\n\noom-kill\n", "oom-kill"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := firstNonEmptyLine(tc.in)
			if got != tc.want {
				t.Errorf("firstNonEmptyLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
