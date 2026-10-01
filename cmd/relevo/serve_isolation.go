package main

import (
	"os/user"
	"strconv"

	"github.com/fuad-daoud/relevo/internal/isolate"
	"github.com/fuad-daoud/relevo/internal/proc"
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
