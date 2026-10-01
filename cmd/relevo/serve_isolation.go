package main

import (
	"os/user"
	"strconv"

	"github.com/fuad-daoud/relevo/internal/isolate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

// resolveIsolation parses the configured serve.isolation and wraps the process
// runner in its boundary. A mode this build cannot run, a mode the running euid
// cannot serve, or an unknown value is returned as an error naming
// serve.isolation, so cmdServeRun can refuse it with not_available before any
// side effect: fail closed, never start and warn.
func resolveIsolation(raw string, euid int) (spawn.Runner, isolate.Mode, error) {
	mode, err := isolate.Parse(raw)
	if err != nil {
		return nil, "", err
	}
	if err := isolate.CheckPrivilege(mode, euid); err != nil {
		return nil, mode, err
	}
	runner, err := isolate.Wrap(proc.New(), mode)
	if err != nil {
		return nil, mode, err
	}
	return runner, mode, nil
}

// lookupUnixUser resolves a declared login name to the tenant a user-mode
// server runs that owner's builders as, from os/user. An unknown name is an
// error, which CheckUnixUser turns into the useradd sentence.
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

// withIsolation stamps the configured mode and image onto an admin status
// config, so `relevo serve status` reports what the admin asked for even when
// this build cannot run it. An unknown value leaves the zero mode, which
// serve.New normalizes to none.
func withIsolation(cfg serve.Config, raw string) serve.Config {
	mode, err := isolate.Parse(raw)
	if err != nil {
		return cfg
	}
	cfg.Isolation = mode
	cfg.IsolationImage = cfg.Policy.ServeIsolationImage()
	return cfg
}

// scopeFromPolicy is the systemd scope template a served round launches
// under, from an already-resolved policy scope block. nil turns scopes off:
// sc.Enabled explicitly false. Otherwise (no block, or one present but silent
// on Enabled) scopes are on by default, with CPUWeight defaulting to 100 and
// every other field passed through as given (its own zero value means "omit"
// to ScopeArgv).
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

// tenantBinExec builds the environment and credential a user-mode child of
// binExec runs with, applying isolate.UserSpec's rule: the daemon's HOME/USER/
// LOGNAME and inherited home variables are denied, and the tenant's own are
// appended, so exactly one of each reaches the child.
func tenantBinExec(t isolate.Tenant, sharedLogins bool) binExec {
	spec := isolate.UserSpec(spawn.ProcSpec{}, t, sharedLogins)
	return binExec{credential: spec.Credential, deny: spec.DenyEnv, extra: spec.Env}
}

// userReaperFor returns the ReaperFor a user-mode server installs: it builds a
// session reaper whose harness deletes run as the tenant, with the tenant's
// environment, so an abandoned session is removed under the tenant's own home
// rather than the daemon's.
func userReaperFor(sharedLogins bool) func(isolate.Tenant) relevo.SessionDeleter {
	return func(t isolate.Tenant) relevo.SessionDeleter {
		return relevo.NewSessionReaper(tenantBinExec(t, sharedLogins))
	}
}
