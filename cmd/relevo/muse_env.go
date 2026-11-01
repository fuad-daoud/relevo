package main

import (
	"os"

	"github.com/fuad-daoud/relevo/internal/mastermind"
)

// museRunnerHost and museRunnerEnviron are the seams isRunnerHost and the XDG
// copy read the muse host through. Tests replace them with fakes.
var (
	museRunnerHost    = mastermind.MuseHostSeam(museCommOf, musePPidOf)
	museRunnerEnviron = museEnvironOf
)

// isRunnerHost reports whether this process runs for a round: its own marker, or
// its muse host's under a scrubbed environment.
func isRunnerHost() bool {
	return mastermind.IsRunnerHost(os.Getenv, os.Getppid(), museRunnerHost, museRunnerEnviron)
}

// museXDGRoots are the only variables the XDG copy moves.
var museXDGRoots = []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME"}

// museXDGFromHost decides which XDG roots this process copies from its muse host:
// only unset roots the host sets; a set variable is never overridden.
func museXDGFromHost(env func(string) string, hostEnv func(string) string) map[string]string {
	if env("MUSE_PLUGIN_ID") == "" {
		return nil
	}
	out := map[string]string{}
	for _, key := range museXDGRoots {
		if env(key) != "" {
			continue
		}
		if value := hostEnv(key); value != "" {
			out[key] = value
		}
	}
	return out
}

// applyMuseHostEnv copies the muse host's XDG roots into this process before any
// state or config path is resolved, so a scrubbed hook opens the same database
// its shell uses.
func applyMuseHostEnv() {
	if os.Getenv("MUSE_PLUGIN_ID") == "" {
		return
	}
	host := mastermind.HostEnviron(os.Getppid(), museRunnerHost, museRunnerEnviron)
	if host == nil {
		return
	}
	for key, value := range museXDGFromHost(os.Getenv, func(k string) string { return host[k] }) {
		_ = os.Setenv(key, value)
	}
}
