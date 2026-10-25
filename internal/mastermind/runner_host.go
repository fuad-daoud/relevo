package mastermind

import (
	"strings"
)

// musePluginEnv marks a process running as a muse hook or plugin MCP server.
// Muse scrubs those children's environment to an allowlist that keeps this
// variable but drops RELEVO_RUNNER, so a hook cannot see the marker its own
// host process carries.
const musePluginEnv = "MUSE_PLUGIN_ID"

// ParseEnviron reads /proc/<pid>/environ's NUL-separated KEY=value entries
// into a map. A value may hold "=", and a final entry without its NUL still
// counts; chunks without "=" carry no key and are skipped.
func ParseEnviron(raw []byte) map[string]string {
	out := map[string]string{}
	for _, entry := range strings.Split(string(raw), "\x00") {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		out[key] = value
	}
	return out
}

// HostEnviron returns the muse host's environment: the nearest muse
// ancestor's environ through museHost, else the parent pid's. A nil seam, a
// nil reader, or an unreadable environ reads as nil, the same safe answer a
// process with no marker gives.
func HostEnviron(ppid int, museHost func(ppid int) (int, bool), environOf func(pid int) (map[string]string, bool)) map[string]string {
	pid := ppid
	if museHost != nil {
		if host, ok := museHost(ppid); ok {
			pid = host
		}
	}
	if environOf == nil {
		return nil
	}
	env, ok := environOf(pid)
	if !ok {
		return nil
	}
	return env
}

// IsRunnerHost reports whether the calling process runs for a round when its
// own environment may be scrubbed: its own marker wins without reading the
// host, and otherwise, only as a muse hook or plugin MCP server, the muse
// host's marker counts. Anything unreadable is not a runner. IsRunner stays
// the pure own-environment check; this is the host-aware variant beside it.
func IsRunnerHost(env func(string) string, ppid int, museHost func(ppid int) (int, bool), environOf func(pid int) (map[string]string, bool)) bool {
	if env == nil {
		return false
	}
	if env(RunnerEnv) != "" {
		return true
	}
	if env(musePluginEnv) == "" {
		return false
	}
	return HostEnviron(ppid, museHost, environOf)[RunnerEnv] != ""
}
