package doctor

import (
	"errors"
	"strings"
	"testing"
)

// errReadEnv is fakeEnv with ReadFile failing: the controllers file exists on
// a real host but relevo cannot read it, the ProbeFailed case.
type errReadEnv struct {
	Env
	err error
}

func (e errReadEnv) ReadFile(string) ([]byte, error) { return nil, e.err }

func TestUserManagerControllersPath(t *testing.T) {
	want := "/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/cgroup.controllers"
	if got := UserManagerControllersPath(1000); got != want {
		t.Errorf("UserManagerControllersPath(1000) = %q, want %q", got, want)
	}
}

const scopeControllersPath = "/fake/cgroup.controllers"

func scopeEnvWith(contents string) *fakeEnv {
	return &fakeEnv{fileContents: map[string]string{scopeControllersPath: contents}}
}

func scopeBlock(key, cpus string, max int) []ScopeBlock {
	return []ScopeBlock{{Key: key, AllowedCPUs: cpus, MaxCPU: max}}
}

// TestScopeChecksEmpty pins that a nil or all-empty block list produces no
// rows at all: ScopeChecks never probes the host for a block nobody set.
func TestScopeChecksEmpty(t *testing.T) {
	env := scopeEnvWith("cpuset cpu io memory pids\n")
	if got := ScopeChecks(env, nil, scopeControllersPath, 4); got != nil {
		t.Errorf("ScopeChecks(nil) = %+v, want nil", got)
	}
	blocks := append(scopeBlock("scope.allowed_cpus", "", 0), scopeBlock("serve.scope.allowed_cpus", "", 0)...)
	if got := ScopeChecks(env, blocks, scopeControllersPath, 4); got != nil {
		t.Errorf("ScopeChecks(empty blocks) = %+v, want nil", got)
	}
}

// TestScopeChecksRow pins the row's precedence: unreadable, not delegated,
// a core this host lacks, then ok.
func TestScopeChecksRow(t *testing.T) {
	tests := []struct {
		name       string
		env        Env
		cpus       string
		max        int
		ncpu       int
		wantSev    Severity
		wantProbe  bool
		wantDetail string
		wantFixHas string // "" means Fix must be exactly empty
	}{
		{name: "delegated is ok", env: scopeEnvWith("cpuset cpu io memory pids\n"), cpus: "0-2", max: 2, ncpu: 4,
			wantSev: SevOK, wantDetail: "cpuset delegated; rounds pinned one per core from 0-2"},
		{name: "not delegated warns with the drop-in fix", env: scopeEnvWith("cpu io memory pids"), cpus: "0-2", max: 2, ncpu: 4,
			wantSev: SevWarn, wantDetail: "scope.allowed_cpus = 0-2 is set but cpuset is not delegated", wantFixHas: "Delegate=cpu cpuset"},
		{name: "unreadable is a failed probe", env: errReadEnv{Env: &fakeEnv{}, err: errors.New("permission denied")}, cpus: "0-2", max: 2, ncpu: 4,
			wantSev: SevWarn, wantProbe: true, wantDetail: "cannot read " + scopeControllersPath},
		{name: "a core this host lacks warns", env: scopeEnvWith("cpuset cpu io memory pids\n"), cpus: "0-7", max: 7, ncpu: 4,
			wantSev: SevWarn, wantDetail: "scope.allowed_cpus names cpu 7 but this host has 4 (0-3)", wantFixHas: "narrow scope.allowed_cpus in config policy"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ScopeChecks(tc.env, scopeBlock("scope.allowed_cpus", tc.cpus, tc.max), scopeControllersPath, tc.ncpu)
			if len(got) != 1 {
				t.Fatalf("checks = %+v, want one", got)
			}
			row := got[0]
			if row.Group != "scope" || row.Name != "allowed_cpus" {
				t.Errorf("row = %s/%s, want scope/allowed_cpus", row.Group, row.Name)
			}
			if row.Severity != tc.wantSev || row.ProbeFailed != tc.wantProbe {
				t.Errorf("row = %+v, want severity %v probeFailed %v", row, tc.wantSev, tc.wantProbe)
			}
			if !strings.Contains(row.Detail, tc.wantDetail) {
				t.Errorf("Detail = %q, want it to contain %q", row.Detail, tc.wantDetail)
			}
			switch {
			case tc.wantFixHas == "" && row.Fix != "":
				t.Errorf("Fix = %q, want empty", row.Fix)
			case tc.wantFixHas != "" && !strings.Contains(row.Fix, tc.wantFixHas):
				t.Errorf("Fix = %q, want it to contain %q", row.Fix, tc.wantFixHas)
			}
		})
	}
}

// TestScopeChecksTwoBlocks pins that each block in scope gets its own row,
// named and detailed independently.
func TestScopeChecksTwoBlocks(t *testing.T) {
	blocks := append(scopeBlock("scope.allowed_cpus", "0-2", 2), scopeBlock("serve.scope.allowed_cpus", "0-3", 3)...)
	got := ScopeChecks(scopeEnvWith("cpuset cpu io memory pids\n"), blocks, scopeControllersPath, 8)
	if len(got) != 2 {
		t.Fatalf("checks = %+v, want two", got)
	}
	if got[0].Name != "allowed_cpus" || got[1].Name != "serve.allowed_cpus" {
		t.Errorf("names = %q/%q, want allowed_cpus/serve.allowed_cpus", got[0].Name, got[1].Name)
	}
	if got[0].Detail == got[1].Detail {
		t.Errorf("two blocks share the detail %q; each must name its own pool", got[0].Detail)
	}
}
