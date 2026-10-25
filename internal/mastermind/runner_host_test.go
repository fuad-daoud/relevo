package mastermind

import (
	"reflect"
	"testing"
)

// TestParseEnviron pins the /proc environ reader's shape: NUL-separated
// KEY=value entries, a value holding "=", an empty file, and a trailing
// entry without its NUL.
func TestParseEnviron(t *testing.T) {
	t.Setenv("MUSE_SESSION_ID", "")
	t.Setenv("MUSE_PLUGIN_ID", "")

	cases := []struct {
		name string
		raw  string
		want map[string]string
	}{
		{"entries", "HOME=/root\x00RELEVO_RUNNER=api\x00", map[string]string{"HOME": "/root", "RELEVO_RUNNER": "api"}},
		{"value holds =", "TOKEN=a=b=c\x00", map[string]string{"TOKEN": "a=b=c"}},
		{"empty file", "", map[string]string{}},
		{"trailing entry without NUL", "A=1\x00B=2", map[string]string{"A": "1", "B": "2"}},
		{"chunk without = is skipped", "BARE\x00A=1\x00", map[string]string{"A": "1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseEnviron([]byte(tc.raw)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseEnviron(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// runnerEnvOf builds the own-environment lookup a host-runner case reads.
func runnerEnvOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// runnerHostWith builds a host environ reader reporting vars as readable.
func runnerHostWith(vars map[string]string) func(int) (map[string]string, bool) {
	return func(int) (map[string]string, bool) { return vars, true }
}

// runnerMuseAt builds a muse-ancestor seam reporting pid as the host.
func runnerMuseAt(pid int) func(int) (int, bool) {
	return func(int) (int, bool) { return pid, true }
}

// runnerFailIfRead builds a seam failing the test when the host is read
// after the answer is already known.
func runnerFailIfRead(t *testing.T) func(int) (int, bool) {
	t.Helper()
	return func(int) (int, bool) {
		t.Error("host must not be read when the answer is already known")
		return 0, false
	}
}

// runnerFailIfEnviron is runnerFailIfRead for the environ reader.
func runnerFailIfEnviron(t *testing.T) func(int) (map[string]string, bool) {
	t.Helper()
	return func(int) (map[string]string, bool) {
		t.Error("host environ must not be read when the answer is already known")
		return nil, false
	}
}

// TestIsRunnerHostReadsMuseHostEnviron pins the scrubbed-environment rule: a
// muse hook whose own env lost the marker still counts as a runner when the
// muse host's environ carries it, and nothing else does.
func TestIsRunnerHostReadsMuseHostEnviron(t *testing.T) {
	t.Setenv("MUSE_SESSION_ID", "")
	t.Setenv("MUSE_PLUGIN_ID", "")

	cases := []struct {
		name string
		host map[string]string
		want bool
	}{
		{"host marker is a runner", map[string]string{"RELEVO_RUNNER": "api"}, true},
		{"host without the marker is not a runner", map[string]string{"HOME": "/root"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := runnerEnvOf(map[string]string{"MUSE_PLUGIN_ID": "p"})
			if got := IsRunnerHost(env, 90, runnerMuseAt(80), runnerHostWith(tc.host)); got != tc.want {
				t.Errorf("IsRunnerHost = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("unreadable host environ is not a runner", func(t *testing.T) {
		env := runnerEnvOf(map[string]string{"MUSE_PLUGIN_ID": "p"})
		unreadable := func(int) (map[string]string, bool) { return nil, false }
		if IsRunnerHost(env, 90, runnerMuseAt(80), unreadable) {
			t.Error("IsRunnerHost with an unreadable host environ = true, want false")
		}
	})
}

// TestIsRunnerHostOwnMarkerSkipsHostRead pins that a process carrying the
// marker itself never reaches the host seams.
func TestIsRunnerHostOwnMarkerSkipsHostRead(t *testing.T) {
	t.Setenv("MUSE_SESSION_ID", "")
	t.Setenv("MUSE_PLUGIN_ID", "")

	env := runnerEnvOf(map[string]string{"MUSE_PLUGIN_ID": "p", "RELEVO_RUNNER": "api"})
	if !IsRunnerHost(env, 90, runnerFailIfRead(t), runnerFailIfEnviron(t)) {
		t.Error("IsRunnerHost with the own marker = false, want true")
	}
}

// TestIsRunnerHostWithoutPluginSkipsHostRead pins that outside a muse plugin
// the host seams stay unread whatever they would report.
func TestIsRunnerHostWithoutPluginSkipsHostRead(t *testing.T) {
	t.Setenv("MUSE_SESSION_ID", "")
	t.Setenv("MUSE_PLUGIN_ID", "")

	env := runnerEnvOf(map[string]string{})
	if IsRunnerHost(env, 90, runnerFailIfRead(t), runnerFailIfEnviron(t)) {
		t.Error("IsRunnerHost without MUSE_PLUGIN_ID = true, want false")
	}
}

// TestIsRunnerHostFallsBackToParentPid pins that a missing muse ancestor
// reads the parent pid's environ instead.
func TestIsRunnerHostFallsBackToParentPid(t *testing.T) {
	t.Setenv("MUSE_SESSION_ID", "")
	t.Setenv("MUSE_PLUGIN_ID", "")

	env := runnerEnvOf(map[string]string{"MUSE_PLUGIN_ID": "p"})
	noMuse := func(int) (int, bool) { return 0, false }
	var read int
	parent := func(pid int) (map[string]string, bool) {
		read = pid
		return map[string]string{"RELEVO_RUNNER": "api"}, true
	}
	if !IsRunnerHost(env, 90, noMuse, parent) {
		t.Fatal("IsRunnerHost with the parent marker = false, want true")
	}
	if read != 90 {
		t.Errorf("environ read at pid %d, want the parent pid 90", read)
	}
}
