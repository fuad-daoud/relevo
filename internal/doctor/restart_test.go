package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"
)

// cgroupReader builds a readCgroup over pid -> content, with errByPID failing
// those pids instead.
func cgroupReader(content map[int]string, errByPID map[int]error) func(pid int) (string, error) {
	return func(pid int) (string, error) {
		if err, ok := errByPID[pid]; ok {
			return "", err
		}
		s, ok := content[pid]
		if !ok {
			return "", fmt.Errorf("no content for pid %d", pid)
		}
		return s, nil
	}
}

// v2cgroup renders a cgroup v2 file for the given cgroup path.
func v2cgroup(path string) string {
	return "0::" + path + "\n"
}

func TestParseUnifiedCgroup(t *testing.T) {
	cases := []struct {
		name    string
		content string
		path    string
		ok      bool
	}{
		{"v2", "0::/user.slice/user-1000.slice/user@1000.service/relevo-round-local-x-1.scope\n", "/user.slice/user-1000.slice/user@1000.service/relevo-round-local-x-1.scope", true},
		{"v2 among v1 lines", "5:cpu:/relevo.service\n0::/relevo.service\n", "/relevo.service", true},
		{"v1 only", "11:cpu:/relevo.service\n10:memory:/relevo.service\n", "", false},
		{"empty", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path, ok := ParseUnifiedCgroup(c.content)
			if path != c.path || ok != c.ok {
				t.Errorf("ParseUnifiedCgroup(%q) = %q, %v; want %q, %v", c.content, path, ok, c.path, c.ok)
			}
		})
	}
}

func TestRestartSafety(t *testing.T) {
	type want struct {
		severity Severity
		detail   string
		fix      string
		unsafe   int
	}
	notExist := func(pid int) error { return fmt.Errorf("open /proc/%d/cgroup: %w", pid, fs.ErrNotExist) }

	cases := []struct {
		name  string
		procs []RunningProc
		read  func(pid int) (string, error)
		want  want
	}{
		{name: "no procs", read: cgroupReader(nil, nil), want: want{SevOK, "no rounds running", "", 0}},
		{name: "all in their own scope",
			procs: []RunningProc{{Binding: "web", Kind: "builder", PID: 11}, {Binding: "web", Kind: "gate", PID: 12}},
			read: cgroupReader(map[int]string{
				11: v2cgroup("/user.slice/user-1000.slice/user@1000.service/relevo-round-local-web-1.scope"),
				12: v2cgroup("/user.slice/user-1000.slice/user@1000.service/relevo-gate-web-1.scope"),
			}, nil),
			want: want{SevOK, "2 running, each in its own scope; a daemon restart leaves them running", "", 0}},
		{name: "one outside its scope",
			procs: []RunningProc{{Binding: "web", Kind: "builder", PID: 11}},
			read:  cgroupReader(map[int]string{11: v2cgroup("/user.slice/user-1000.slice/user@1000.service/relevo.service")}, nil),
			want: want{SevWarn, "1 of 1 running outside their own scope (web builder pid 11 in relevo.service): a daemon restart may kill them",
				"let them finish before restarting relevo.service", 1}},
		{name: "mix with more than three offenders truncates",
			procs: []RunningProc{
				{Binding: "web", Kind: "builder", PID: 11}, {Binding: "web", Kind: "gate", PID: 12}, {Binding: "api", Kind: "consult", PID: 13},
				{Binding: "api", Kind: "builder", PID: 14}, {Binding: "docs", Kind: "builder", PID: 15}, {Binding: "safe", Kind: "builder", PID: 16},
			},
			read: cgroupReader(map[int]string{
				11: v2cgroup("/relevo.service"), 12: v2cgroup("/relevo.service"), 13: v2cgroup("/relevo.service"),
				14: v2cgroup("/relevo.service"), 15: v2cgroup("/relevo.service"), 16: v2cgroup("/relevo-round-local-safe-1.scope"),
			}, nil),
			want: want{SevWarn, "5 of 6 running outside their own scope (web builder pid 11 in relevo.service, web gate pid 12 in relevo.service, api consult pid 13 in relevo.service, and 2 more): a daemon restart may kill them",
				"let them finish before restarting relevo.service", 5}},
		{name: "not-exist is skipped",
			procs: []RunningProc{{Binding: "web", Kind: "builder", PID: 11}, {Binding: "gone", Kind: "builder", PID: 99}},
			read:  cgroupReader(map[int]string{11: v2cgroup("/relevo-round-local-web-1.scope")}, map[int]error{99: notExist(99)}),
			want:  want{SevOK, "1 running, each in its own scope; a daemon restart leaves them running", "", 0}},
		{name: "all not-exist cannot tell", procs: []RunningProc{{Binding: "gone", Kind: "builder", PID: 99}},
			read: cgroupReader(nil, map[int]error{99: notExist(99)}), want: want{SevOK, "cannot tell on this host", "", 0}},
		{name: "permission error cannot tell", procs: []RunningProc{{Binding: "web", Kind: "builder", PID: 11}},
			read: cgroupReader(nil, map[int]error{11: fs.ErrPermission}), want: want{SevOK, "cannot tell on this host", "", 0}},
		{name: "cgroup v1 has no unified line", procs: []RunningProc{{Binding: "web", Kind: "builder", PID: 11}},
			read: cgroupReader(map[int]string{11: "11:cpu:/user.slice\n10:memory:/user.slice\n"}, nil), want: want{SevOK, "cannot tell on this host", "", 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RestartSafety(c.procs, c.read)
			if got.Name != "restart" || got.Group != "" {
				t.Errorf("check identity = group %q name %q; want \"\"/\"restart\"", got.Group, got.Name)
			}
			if got.Severity != c.want.severity || got.Detail != c.want.detail || got.Fix != c.want.fix || got.Unsafe != c.want.unsafe {
				t.Errorf("RestartSafety() = %+v; want severity %v, detail %q, fix %q, unsafe %d",
					got, c.want.severity, c.want.detail, c.want.fix, c.want.unsafe)
			}
		})
	}
}

// TestRestartSafetyReadsEveryProcess pins that the check reads each gathered
// process's cgroup: a read the caller forgot to make would show up as a pid
// with no content (a "cannot tell").
func TestRestartSafetyReadsEveryProcess(t *testing.T) {
	read := func(pid int) (string, error) {
		if pid != 7 {
			return "", errors.New("unexpected pid")
		}
		return v2cgroup("/relevo-round-local-x-1.scope"), nil
	}
	got := RestartSafety([]RunningProc{{Binding: "x", Kind: "builder", PID: 7}}, read)
	if got.Detail != "1 running, each in its own scope; a daemon restart leaves them running" {
		t.Errorf("detail = %q; want the single scoped process counted", got.Detail)
	}
}
