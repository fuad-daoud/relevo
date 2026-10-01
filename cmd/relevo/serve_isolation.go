package main

import (
	"github.com/fuad-daoud/relevo/internal/isolate"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

// resolveIsolation parses the configured serve.isolation and wraps the process
// runner in its boundary. A mode this build cannot run -- or an unknown value
// -- is returned as an error naming serve.isolation, so cmdServeRun can refuse
// it with not_available before any side effect (spec §10: fail closed, never
// start and warn).
func resolveIsolation(raw string) (spawn.Runner, isolate.Mode, error) {
	mode, err := isolate.Parse(raw)
	if err != nil {
		return nil, "", err
	}
	runner, err := isolate.Wrap(proc.New(), mode)
	if err != nil {
		return nil, mode, err
	}
	return runner, mode, nil
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
