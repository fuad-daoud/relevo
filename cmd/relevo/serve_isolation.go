package main

import (
	"fmt"
	"os/exec"
	"os/user"
	"strconv"

	"github.com/fuad-daoud/relevo/internal/isolate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
)

// containerLookPath resolves the container runtime at startup; a var so a test can
// force it absent without touching the host's PATH.
var containerLookPath = exec.LookPath

// serveProcRunner is the process runner a server's children start through. Its
// temp directory is the machine state root's own, not the serve root's: user mode
// overrides it per owner, and container mode keeps it out of the container entirely.
func serveProcRunner(stateRoot string) (*proc.Runner, error) {
	dir, err := tmpdir.TempDir(stateRoot)
	if err != nil {
		return nil, err
	}
	r := proc.New()
	r.TmpDir = dir
	return r, nil
}

// resolveIsolation parses the configured serve.isolation and wraps its boundary
// around the base runner. An unrunnable mode is refused with not_available
// before any side effect: fail closed, never start and warn. The base runner is
// built here, so the wrap order cannot be reordered by accident.
func resolveIsolation(raw string, euid int, stateRoot string) (spawn.Runner, isolate.Mode, error) {
	base, err := serveProcRunner(stateRoot)
	if err != nil {
		return nil, "", err
	}
	mode, err := isolate.Parse(raw)
	if err != nil {
		return nil, "", err
	}
	if err := isolate.CheckPrivilege(mode, euid); err != nil {
		return nil, mode, err
	}
	if mode == isolate.ModeContainer {
		if _, err := containerLookPath("podman"); err != nil {
			return nil, mode, fmt.Errorf("serve.isolation=container requires podman: install podman and ensure it is on PATH")
		}
	}
	runner, err := isolate.Wrap(base, mode)
	if err != nil {
		return nil, mode, err
	}
	return runner, mode, nil
}

// lookupUnixUser resolves a declared login name to the tenant a user-mode server
// runs that owner's builders as. An unknown name is an error, which CheckUnixUser
// turns into the useradd sentence.
func lookupUnixUser(name string) (isolate.Tenant, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return isolate.Tenant{}, err
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return isolate.Tenant{}, err
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return isolate.Tenant{}, err
	}
	return isolate.Tenant{User: u.Username, UID: uint32(uid), GID: uint32(gid), Home: u.HomeDir}, nil
}

// withIsolation stamps the configured mode and image onto an admin status config,
// so status reports what the admin asked for even when this build cannot run it.
// An unknown value leaves the zero mode, which serve.New normalizes to none.
func withIsolation(cfg serve.Config, raw string) serve.Config {
	mode, err := isolate.Parse(raw)
	if err != nil {
		return cfg
	}
	cfg.Isolation = mode
	cfg.IsolationImage = cfg.Policy.ServeIsolationImage()
	return cfg
}

// scopeFromPolicy is the systemd scope template a served round launches under.
// nil turns scopes off only when sc.Enabled is explicitly false: a nil sc, or
// one silent on Enabled, leaves scopes on with CPUWeight 100, and every other
// field passes through as given, its own zero meaning "omit" to ScopeArgv.
func scopeFromPolicy(sc *policy.ScopePolicy) *spawn.ScopeSpec {
	if sc != nil && sc.Enabled != nil && !*sc.Enabled {
		return nil
	}
	spec := &spawn.ScopeSpec{CPUWeight: 100}
	if sc != nil {
		spec.Slice = sc.Slice
		if sc.CPUWeight != 0 {
			spec.CPUWeight = sc.CPUWeight
		}
		spec.MemoryMax = sc.MemoryMax
		spec.CPUQuota = sc.CPUQuota
		spec.GateCPUQuota = sc.GateCPUQuota
		spec.AllowedCPUs = sc.AllowedCPUs
		spec.TasksMax = sc.TasksMax
	}
	return spec
}

// tenantBinExec builds the environment and credential a user-mode child of binExec
// runs with: the daemon's HOME/USER/LOGNAME are denied and the tenant's own are
// appended, so exactly one of each reaches the child.
func tenantBinExec(t isolate.Tenant, sharedLogins bool) binExec {
	spec := isolate.UserSpec(spawn.ProcSpec{}, t, sharedLogins)
	return binExec{credential: spec.Credential, deny: spec.DenyEnv, extra: spec.Env}
}

// userReaperFor returns the ReaperFor a user-mode server installs: harness deletes
// run as the tenant, so an abandoned session is removed under the tenant's own
// home rather than the daemon's.
func userReaperFor(sharedLogins bool) func(isolate.Tenant) relevo.SessionDeleter {
	return func(t isolate.Tenant) relevo.SessionDeleter {
		return relevo.NewSessionReaper(tenantBinExec(t, sharedLogins))
	}
}
