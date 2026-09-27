package proc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// waitGone polls Alive until it is false or the deadline passes.
func waitGone(t *testing.T, r *Runner, h spawn.ProcHandle, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		alive, err := r.Alive(context.Background(), h)
		if err != nil {
			t.Fatalf("Alive: %v", err)
		}
		if !alive {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive after %s", h.PID, within)
}

// start runs argv in a fresh temp dir and returns the handle, log and stream.
func start(t *testing.T, r *Runner, argv ...string) (spawn.ProcHandle, string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "001-builder.log")
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), spawn.ProcSpec{Dir: dir, Argv: argv, LogPath: log, StreamPath: stream})
	if err != nil {
		t.Fatalf("Start(%v): %v", argv, err)
	}
	return h, log, stream
}

// writeStub puts a fake systemd-run on dir, for a test to prepend to PATH.
func writeStub(t *testing.T, dir, script string) {
	t.Helper()
	p := filepath.Join(dir, "systemd-run")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// writeSystemctlStub puts a fake systemctl on dir, for a test to prepend to
// PATH, and returns its state, argv-log and stubborn paths. The state file is
// "active" or "inactive": show answers from it, stop fails while it is
// inactive, and stop or kill flips it to inactive unless the stubborn file
// exists. Every argv it receives is appended to the log. The stub uses shell
// builtins only, so PATH may be the stub's own directory alone. No real unit is
// ever touched.
func writeSystemctlStub(t *testing.T, dir, state string) (statePath, logPath, stubbornPath string) {
	t.Helper()
	statePath = filepath.Join(dir, "systemctl-state")
	logPath = filepath.Join(dir, "systemctl-argv.log")
	stubbornPath = filepath.Join(dir, "systemctl-stubborn")
	if err := os.WriteFile(statePath, []byte(state+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
echo "$@" >> "` + logPath + `"
state=""
read -r state < "` + statePath + `"
case "$1 $2" in
"--user show") printf '%s\n' "$state";;
"--user stop")
  [ "$state" = inactive ] && exit 1
  [ -e "` + stubbornPath + `" ] || echo inactive > "` + statePath + `"
  ;;
"--user kill")
  [ -e "` + stubbornPath + `" ] || echo inactive > "` + statePath + `"
  ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return statePath, logPath, stubbornPath
}

// unsetGoMaxProcs removes any inherited GOMAXPROCS for the test's duration.
func unsetGoMaxProcs(t *testing.T) {
	t.Helper()
	t.Setenv("GOMAXPROCS", "")
	if err := os.Unsetenv("GOMAXPROCS"); err != nil {
		t.Fatal(err)
	}
}

// childEnvValues returns every value of name in a spawn's `env` stream.
func childEnvValues(t *testing.T, stream, name string) []string {
	t.Helper()
	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	var values []string
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, name+"="); ok {
			values = append(values, v)
		}
	}
	return values
}

// childEnvValue returns the last value of name in a spawn's `env` stream.
func childEnvValue(t *testing.T, stream, name string) (string, bool) {
	t.Helper()
	values := childEnvValues(t, stream, name)
	if len(values) == 0 {
		return "", false
	}
	return values[len(values)-1], true
}

// acceptingStub is a systemd-run that accepts every invocation and execs the
// command after "--", as systemd-run --scope does.
const acceptingStub = `#!/bin/sh
while [ "$1" != "--" ]; do shift; done
shift
exec "$@"
`

// refusingScopeStub reproduces the stderr line a refused scope prints.
const refusingScopeStub = "#!/bin/sh\necho 'Failed to start transient scope unit: Permission denied' >&2\nexit 1\n"

// exitingStub fails without printing anything, so the probe must report its
// exit status.
const exitingStub = "#!/bin/sh\nexit 1\n"

// refusingPinStub accepts a plain scope but fails any invocation carrying
// AllowedCPUs, the shape a user manager without a delegated cpuset prints.
const refusingPinStub = `#!/bin/sh
for a in "$@"; do
  case "$a" in
    AllowedCPUs=*) echo 'Failed to set AllowedCPUs: Permission denied' >&2; exit 1;;
  esac
done
while [ "$1" != "--" ]; do shift; done
shift
exec "$@"
`

// countingScopeStub records every invocation in calls and fails like a refused
// scope, so the count is the number of scope probes.
func countingScopeStub(calls string) string {
	return `#!/bin/sh
echo called >> "` + calls + `"
echo 'Failed to start transient scope unit: Permission denied' >&2
exit 1
`
}

// countingPinStub records only invocations carrying an AllowedCPUs argument
// under a relevo-probe-cpus- unit, so the count is the pin probes.
func countingPinStub(calls string) string {
	return `#!/bin/sh
pin=0
for a in "$@"; do
  case "$a" in
    --unit=relevo-probe-cpus-*) pin=1;;
  esac
done
[ "$pin" = 1 ] && echo pin >> "` + calls + `"
while [ "$1" != "--" ]; do shift; done
shift
exec "$@"
`
}
