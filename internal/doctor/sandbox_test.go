package doctor

import (
	"strings"
	"testing"
)

// TestXDGCheckWarnsOnInheritedRoot pins the xdg row's warn branch: a set
// XDG variable whose resolved root exists and belongs to another uid is the
// inherited-parent-environment case, and the fix must name the unsetting.
func TestXDGCheckWarnsOnInheritedRoot(t *testing.T) {
	env := &fakeEnv{
		existingFiles: map[string]bool{"/home/parent/.local/state/relevo": true},
		fileOwners:    map[string]uint32{"/home/parent/.local/state/relevo": 1000},
	}
	in := SandboxInput{
		StateHome:  "/home/parent/.local/state",
		StateRoot:  "/home/parent/.local/state/relevo",
		ConfigRoot: "/home/u/.config",
		User:       "rv-demo",
		UID:        1001,
	}

	c := XDGCheck(env, in)
	if c.Name != "xdg" || c.Group != "" {
		t.Fatalf("row = %+v, want the global xdg row", c)
	}
	if c.Severity != SevWarn {
		t.Errorf("severity = %v, want warn", c.Severity)
	}
	for _, want := range []string{"XDG_STATE_HOME", "/home/parent/.local/state/relevo", "1000", "1001"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("detail = %q, want it to contain %q", c.Detail, want)
		}
	}
	if !strings.Contains(c.Fix, "unset XDG_STATE_HOME XDG_CONFIG_HOME") {
		t.Errorf("fix = %q, want it to unset both variables", c.Fix)
	}
	if !strings.Contains(c.Fix, "rv-demo") {
		t.Errorf("fix = %q, want it to name the login user", c.Fix)
	}
}

// TestXDGCheckOKCases covers every way the xdg row stays quiet: nothing set, a
// root this user owns, a root that does not exist, and a root whose owner the
// platform cannot report.
func TestXDGCheckOKCases(t *testing.T) {
	cases := []struct {
		name string
		env  *fakeEnv
		in   SandboxInput
	}{
		{
			name: "no variable set",
			env:  &fakeEnv{},
			in:   SandboxInput{StateRoot: "/home/u/.local/state/relevo", ConfigRoot: "/home/u/.config", User: "u", UID: 1000},
		},
		{
			name: "the root is this user's",
			env: &fakeEnv{
				existingFiles: map[string]bool{"/home/u/.local/state/relevo": true},
				fileOwners:    map[string]uint32{"/home/u/.local/state/relevo": 1000},
			},
			in: SandboxInput{StateHome: "/home/u/.local/state", StateRoot: "/home/u/.local/state/relevo", User: "u", UID: 1000},
		},
		{
			name: "the root does not exist yet",
			env:  &fakeEnv{},
			in:   SandboxInput{StateHome: "/home/parent/.local/state", StateRoot: "/home/parent/.local/state/relevo", User: "u", UID: 1001},
		},
		{
			name: "the owner is not knowable",
			env:  &fakeEnv{existingFiles: map[string]bool{"/x/relevo": true}},
			in:   SandboxInput{StateHome: "/x", StateRoot: "/x/relevo", User: "u", UID: 1000},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := XDGCheck(tc.env, tc.in)
			if c.Severity != SevOK {
				t.Errorf("severity = %v (%s), want ok", c.Severity, c.Detail)
			}
			if c.Fix != "" {
				t.Errorf("fix = %q, want it empty on an ok row", c.Fix)
			}
		})
	}
}

// TestXDGCheckWarnsOnForeignConfigRoot pins the config half: the config root
// alone, with XDG_STATE_HOME unset, is enough to warn.
func TestXDGCheckWarnsOnForeignConfigRoot(t *testing.T) {
	env := &fakeEnv{
		existingFiles: map[string]bool{"/home/parent/.config": true},
		fileOwners:    map[string]uint32{"/home/parent/.config": 1000},
	}
	c := XDGCheck(env, SandboxInput{ConfigHome: "/home/parent/.config", ConfigRoot: "/home/parent/.config", User: "rv-demo", UID: 1001})

	if c.Severity != SevWarn {
		t.Fatalf("severity = %v (%s), want warn", c.Severity, c.Detail)
	}
	if !strings.Contains(c.Detail, "XDG_CONFIG_HOME") {
		t.Errorf("detail = %q, want it to name XDG_CONFIG_HOME", c.Detail)
	}
}

// TestLingerCheckWarnsWithoutLingerFile pins the linger row's warn branch: no
// systemd linger file for this account, so the user manager stops at logout.
// The fix must be the loginctl line.
func TestLingerCheckWarnsWithoutLingerFile(t *testing.T) {
	in := SandboxInput{User: "rv-demo"}
	c := LingerCheck(&fakeEnv{}, in)

	if c.Name != "linger" || c.Group != "" {
		t.Fatalf("row = %+v, want the global linger row", c)
	}
	if c.Severity != SevWarn {
		t.Errorf("severity = %v, want warn", c.Severity)
	}
	if !strings.Contains(c.Detail, LingerPath("rv-demo")) {
		t.Errorf("detail = %q, want it to name %s", c.Detail, LingerPath("rv-demo"))
	}
	if c.Fix != "sudo loginctl enable-linger rv-demo" {
		t.Errorf("fix = %q, want the loginctl enable-linger line", c.Fix)
	}
}

// TestLingerCheckOKWithLingerFile pins the linger row's ok branch.
func TestLingerCheckOKWithLingerFile(t *testing.T) {
	env := &fakeEnv{existingFiles: map[string]bool{LingerPath("rv-demo"): true}}
	c := LingerCheck(env, SandboxInput{User: "rv-demo"})

	if c.Severity != SevOK {
		t.Errorf("severity = %v (%s), want ok", c.Severity, c.Detail)
	}
	if c.Fix != "" {
		t.Errorf("fix = %q, want it empty on an ok row", c.Fix)
	}
}

// TestPortCheckWarnsOnDefaultServePort pins the port row's first warn branch:
// a sandbox on 7777 collides with the production serve on the same host, and
// the fix must move it to the port the sandbox was created for.
func TestPortCheckWarnsOnDefaultServePort(t *testing.T) {
	c := PortCheck(SandboxInput{Listen: ":7777", MarkerPort: 7801})

	if c.Name != "port" || c.Group != "" {
		t.Fatalf("row = %+v, want the global port row", c)
	}
	if c.Severity != SevWarn {
		t.Errorf("severity = %v, want warn", c.Severity)
	}
	if !strings.Contains(c.Detail, "7777") {
		t.Errorf("detail = %q, want it to name the port", c.Detail)
	}
	// The two warn branches are distinguished by their detail, not just their
	// severity: a default port is a collision with the production serve, which
	// is a different fault from a unit that drifted off its own port. Without
	// this the default-port branch would be indistinguishable from the
	// mismatch one and neither test would fail if it were removed.
	if !strings.Contains(c.Detail, "default port shared with the production serve") {
		t.Errorf("detail = %q, want the collision reason, not a marker mismatch", c.Detail)
	}
	if c.Fix != "relevo serve --listen :7801" {
		t.Errorf("fix = %q, want the sandbox port", c.Fix)
	}
}

// TestPortCheckWarnsOnPortMismatch pins the second warn branch: a non-default
// port that is not the marker's, which means the unit changed after create.
func TestPortCheckWarnsOnPortMismatch(t *testing.T) {
	c := PortCheck(SandboxInput{Listen: "127.0.0.1:7802", MarkerPort: 7801})

	if c.Severity != SevWarn {
		t.Fatalf("severity = %v, want warn", c.Severity)
	}
	if !strings.Contains(c.Detail, "7802") || !strings.Contains(c.Detail, "7801") {
		t.Errorf("detail = %q, want it to name both ports", c.Detail)
	}
	if c.Fix != "relevo serve --listen :7801" {
		t.Errorf("fix = %q, want the marker's port", c.Fix)
	}
}

// TestPortCheckOKCases covers the quiet branch: the marker's own port, a
// sandbox with no recorded port on a non-default one, and no serve pointer at
// all, which is not "port 0".
func TestPortCheckOKCases(t *testing.T) {
	cases := []struct {
		name string
		in   SandboxInput
	}{
		{"the marker's own port", SandboxInput{Listen: "127.0.0.1:7801", MarkerPort: 7801}},
		{"a non-default port with no marker port", SandboxInput{Listen: ":7801"}},
		{"no serve pointer", SandboxInput{Listen: "", MarkerPort: 7801}},
		{"a malformed pointer", SandboxInput{Listen: "not-an-address", MarkerPort: 7801}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := PortCheck(tc.in)
			if c.Severity != SevOK {
				t.Errorf("severity = %v (%s), want ok", c.Severity, c.Detail)
			}
			if c.Fix != "" {
				t.Errorf("fix = %q, want it empty on an ok row", c.Fix)
			}
		})
	}
}

// TestParseSandboxMarker pins the parser: the two keys the script writes, the
// no-port case, surrounding whitespace and comments, and the nameless body
// that must read as "no sandbox" rather than as a sandbox with no port.
func TestParseSandboxMarker(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		wantOK bool
		want   SandboxMarker
	}{
		{"name and port", "name=demo\nport=7801\n", true, SandboxMarker{Name: "demo", Port: 7801}},
		{"name only", "name=demo\n", true, SandboxMarker{Name: "demo"}},
		{"whitespace and blank lines", "\n name = demo \n\nport = 7801\n", true, SandboxMarker{Name: "demo", Port: 7801}},
		{"an unparseable port is no port", "name=demo\nport=nope\n", true, SandboxMarker{Name: "demo"}},
		{"unknown keys are ignored", "name=demo\nfuture=thing\nport=7801\n", true, SandboxMarker{Name: "demo", Port: 7801}},
		{"no name", "port=7801\n", false, SandboxMarker{}},
		{"empty", "", false, SandboxMarker{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseSandboxMarker([]byte(tc.raw))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("marker = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestListenPort pins the listen-address parse, including the malformed values
// that must read as no port rather than port 0.
func TestListenPort(t *testing.T) {
	cases := []struct {
		listen string
		want   int
		wantOK bool
	}{
		{":7801", 7801, true},
		{"127.0.0.1:7801", 7801, true},
		{"[::1]:7801", 7801, true},
		{"7801", 7801, true},
		{"  :7801  ", 7801, true},
		{"", 0, false},
		{"localhost", 0, false},
		{":0", 0, false},
		{":70000", 0, false},
		{":-1", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.listen, func(t *testing.T) {
			got, ok := ListenPort(tc.listen)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("ListenPort(%q) = %d, %v; want %d, %v", tc.listen, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestLingerPath pins the file systemd keeps, since the row's detail names it.
func TestLingerPath(t *testing.T) {
	if got := LingerPath("rv-demo"); got != "/var/lib/systemd/linger/rv-demo" {
		t.Errorf("LingerPath = %q, want /var/lib/systemd/linger/rv-demo", got)
	}
}
