package proc

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

func TestParseSystemdVersion(t *testing.T) {
	cases := map[string]struct {
		out  string
		want int
		ok   bool
	}{
		"release":          {"systemd 262 (262-1-arch)\n+PAM +AUDIT\n", 262, true},
		"distro suffix":    {"systemd 255 (255.4-1ubuntu8)\n", 255, true},
		"pre-release":      {"systemd 257~rc2 (257~rc2-1)\n", 257, true},
		"leading blank":    {"\nsystemd 254\n", 254, true},
		"not systemd":      {"udevadm 262\n", 0, false},
		"no number":        {"systemd unknown\n", 0, false},
		"empty":            {"", 0, false},
		"name only":        {"systemd\n", 0, false},
		"number not first": {"systemd v262\n", 0, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := parseSystemdVersion(c.out)
			if got != c.want || ok != c.ok {
				t.Errorf("parseSystemdVersion(%q) = %d, %v; want %d, %v", c.out, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestSystemdRunVersionReadsTheBinaryOnPath(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "#!/bin/sh\n[ \"$1\" = --version ] || exit 9\nprintf 'systemd 258 (258-1)\\n+PAM\\n'\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	got, err := SystemdRunVersion(context.Background())
	if err != nil || got != 258 {
		t.Fatalf("SystemdRunVersion = %d, %v; want 258", got, err)
	}

	writeStub(t, dir, "#!/bin/sh\necho garbage\n")
	if _, err := SystemdRunVersion(context.Background()); err == nil {
		t.Fatal("SystemdRunVersion on unparseable output: want an error")
	}
}

// On a host whose systemd-run expands a scope's command line, the switch keeps
// a leading $NAME, a ${NAME} and a $$ exactly as given. It runs the real
// systemd-run, so it skips where user scopes are unavailable.
func TestScopeWithNoExpandPassesDollarsThroughVerbatim(t *testing.T) {
	ctx := context.Background()
	v, err := SystemdRunVersion(ctx)
	if err != nil || v < expandSwitchFrom {
		t.Skipf("systemd-run without --expand-environment (version %d, %v)", v, err)
	}
	if err := ProbeScopes(ctx, ""); err != nil {
		t.Skipf("user scopes unavailable: %v", err)
	}
	unit, err := probeUnit("relevo-test-noexpand-")
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"$HOME/gate.sh 2>&1", "a${HOME}b", "cost $$5"}
	argv := ScopeArgv(spawn.ScopeSpec{Unit: unit, CPUWeight: 100, NoExpand: true}, append([]string{"printf", `%s\n`}, args...))
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("systemd-run: %v: %s", err, stderr.String())
	}
	if got, want := string(out), strings.Join(args, "\n")+"\n"; got != want {
		t.Errorf("scoped argv = %q, want %q verbatim", got, want)
	}
}
