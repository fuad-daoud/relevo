//go:build unix

package proc

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// shExit runs script under sh and returns its *exec.ExitError and stdout.
func shExit(t *testing.T, script string) (*exec.ExitError, []byte) {
	t.Helper()
	out, err := exec.Command("sh", "-c", script).Output()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("sh -c %q: err = %v, want an *exec.ExitError", script, err)
	}
	return ee, out
}

// TestClassifyPS pins the one shape that means "no such pid" and every shape
// that must stay an error, so a caller treats the process as alive this tick.
func TestClassifyPS(t *testing.T) {
	ee1, out1 := shExit(t, "exit 1")                     // procps's "no such pid": exit 1, nothing on stdout
	ee1b, out1b := shExit(t, "echo nope; exit 1")        // exit 1 with output: not "no such pid"
	ee2, out2 := shExit(t, "exit 2")                     // any other code
	eeSig, outSig := shExit(t, "kill -TERM $$; sleep 5") // systemd's SIGTERM to the ps child

	if strings.TrimSpace(string(out1)) != "" {
		t.Fatalf("sh -c 'exit 1' printed %q, want nothing", out1)
	}
	if strings.TrimSpace(string(out1b)) == "" {
		t.Fatalf("sh -c 'echo nope; exit 1' printed nothing, want output")
	}

	cases := []struct {
		name    string
		ctxErr  error
		exitErr *exec.ExitError
		stdout  []byte
		want    psVerdict
	}{
		{
			name: "no exit error is ok",
			want: psOK,
		},
		{
			name:    "exit 1 with empty stdout is no process",
			exitErr: ee1,
			stdout:  out1,
			want:    psNoProcess,
		},
		{
			name:    "exit 1 with non-empty stdout is transient",
			exitErr: ee1b,
			stdout:  out1b,
			want:    psTransient,
		},
		{
			name:    "exit 2 is transient",
			exitErr: ee2,
			stdout:  out2,
			want:    psTransient,
		},
		{
			name:    "a signalled ps is transient",
			exitErr: eeSig,
			stdout:  outSig,
			want:    psTransient,
		},
		{
			name:    "a cancelled context is transient",
			ctxErr:  context.Canceled,
			exitErr: ee1,
			stdout:  out1,
			want:    psTransient,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyPS(tc.ctxErr, tc.exitErr, tc.stdout); got != tc.want {
				t.Errorf("classifyPS() = %v, want %v", got, tc.want)
			}
		})
	}
}
