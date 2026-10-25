package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The tests here are pure: synthesized dial errors, no socket, no listener,
// no spawned process. The dial path's real permission refusal is pinned in
// machinedb_sandbox_unix_test.go.

const sandboxTestSock = "/tmp/rvo/relevo.sock"

// sandboxDialError is the error a unix dial earns when the host's sandbox
// refuses the socket call: the shape dialUDS returns -- a *net.OpError over
// an *os.SyscallError over the errno.
func sandboxDialError(errno syscall.Errno) error {
	return &net.OpError{
		Op:   "dial",
		Net:  "unix",
		Addr: &net.UnixAddr{Name: sandboxTestSock, Net: "unix"},
		Err:  &os.SyscallError{Syscall: "connect", Err: errno},
	}
}

// sandboxRouteError is the failure the store opener hands the verbs after a
// sandboxed refusal, in the shape openDBRoute produces: dialWithStart's wrap
// of the sentinel, the socket and the first errno, inside the db dial wrap.
func sandboxRouteError(errno syscall.Errno) error {
	routeErr := fmt.Errorf("%w: %s: %w", errOwnerSandboxDenied, sandboxTestSock, sandboxDialError(errno))
	return fmt.Errorf("db: dial %s: %w: %w", sandboxTestSock, db.ErrOpen, routeErr)
}

// unavailableRouteError is the shape openDBRoute hands the verbs for a socket
// that was never answered. The first dial's missing-or-refused errno is gone
// from the text today -- that is the unchanged behaviour this pins.
func unavailableRouteError() error {
	routeErr := fmt.Errorf("%w: %s", errOwnerUnavailable, sandboxTestSock)
	return fmt.Errorf("db: dial %s: %w: %w", sandboxTestSock, db.ErrOpen, routeErr)
}

func TestDialSandboxDeniedFindsPermissionErrnosAndNothingElse(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"EACCES over the dial chain", sandboxDialError(syscall.EACCES), true},
		{"EPERM over the dial chain", sandboxDialError(syscall.EPERM), true},
		{"bare EACCES", syscall.EACCES, true},
		{"syscall error", os.NewSyscallError("connect", syscall.EPERM), true},
		{"path error", &os.PathError{Op: "connect", Path: sandboxTestSock, Err: syscall.EACCES}, true},
		{"missing socket", sandboxDialError(syscall.ENOENT), false},
		{"refused connection", sandboxDialError(syscall.ECONNREFUSED), false},
		{"io deadline", &net.OpError{Op: "dial", Net: "unix", Err: os.ErrDeadlineExceeded}, false},
		{"context deadline", fmt.Errorf("dial: %w", context.DeadlineExceeded), false},
		{"owner unavailable", errOwnerUnavailable, false},
		{"plain error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dialSandboxDenied(tc.err); got != tc.want {
				t.Errorf("dialSandboxDenied(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestWriteErrorClassifiesSandboxedDialsToTheSandboxCode(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EACCES, syscall.EPERM} {
		t.Run(errno.Error(), func(t *testing.T) {
			err := writeError(sandboxRouteError(errno))

			var ce *cliError
			if !errors.As(err, &ce) {
				t.Fatalf("writeError(%v) is not a coded error", err)
			}
			if ce.code != codeOwnerSandboxDenied {
				t.Errorf("code = %q, want %q", ce.code, codeOwnerSandboxDenied)
			}
			for _, want := range []string{"muse --yolo", "muse --disable-sandbox", "MCP"} {
				if !strings.Contains(ce.next, want) {
					t.Errorf("next = %q, want it to name %q", ce.next, want)
				}
			}
			if strings.Contains(ce.next, "bugreport") {
				t.Errorf("next = %q, want no bugreport hint", ce.next)
			}
			if strings.Contains(err.Error(), "relevo owner unavailable") {
				t.Errorf("message %q must not read as an unreachable owner", err)
			}

			var ec exitCodeErr
			if !errors.As(err, &ec) || ec.code != 1 {
				t.Errorf("exit = %v, want 1", err)
			}
		})
	}
}

func TestWriteErrorMissingSocketStaysInternalWithBugreport(t *testing.T) {
	err := writeError(unavailableRouteError())

	var ce *cliError
	if !errors.As(err, &ce) {
		t.Fatalf("writeError(%v) is not a coded error", err)
	}
	if ce.code != codeInternal {
		t.Errorf("code = %q, want %q", ce.code, codeInternal)
	}
	if ce.next != "relevo bugreport" {
		t.Errorf("next = %q, want the bugreport hint", ce.next)
	}
	if !strings.Contains(err.Error(), "relevo owner unavailable") {
		t.Errorf("message %q must keep naming the owner unavailable", err)
	}
}

func TestWriteErrorRefusedConnectionStaysInternalWithBugreport(t *testing.T) {
	err := writeError(unavailableRouteError())

	var ce *cliError
	if !errors.As(err, &ce) {
		t.Fatalf("writeError(%v) is not a coded error", err)
	}
	if ce.code != codeInternal {
		t.Errorf("code = %q, want %q", ce.code, codeInternal)
	}
	if ce.next != "relevo bugreport" {
		t.Errorf("next = %q, want the bugreport hint", ce.next)
	}
}

func TestSandboxedDialHumanAndJSONCarryCodeAndHint(t *testing.T) {
	err := writeError(sandboxRouteError(syscall.EACCES))

	var human bytes.Buffer
	if code := report(&human, err, false); code != 1 {
		t.Errorf("human exit = %d, want 1", code)
	}
	line := human.String()
	if !strings.HasPrefix(line, "relevo: owner_sandbox_denied: ") {
		t.Errorf("human line = %q, want the new code first", line)
	}
	if !strings.Contains(line, "next: relevo verbs need an unsandboxed shell") {
		t.Errorf("human line = %q, want the sandbox hint", line)
	}
	if strings.Contains(line, "bugreport") {
		t.Errorf("human line = %q, want no bugreport line", line)
	}

	var doc bytes.Buffer
	if code := report(&doc, err, true); code != 1 {
		t.Errorf("json exit = %d, want 1", code)
	}
	if n := bytes.Count(doc.Bytes(), []byte("\n")); n != 1 {
		t.Errorf("envelope spans %d lines, want 1: %q", n, doc.String())
	}
	var env errorEnvelope
	if err := json.Unmarshal(doc.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope %q: %v", doc.String(), err)
	}
	if env.Error.Code != codeOwnerSandboxDenied {
		t.Errorf("envelope code = %q, want %q", env.Error.Code, codeOwnerSandboxDenied)
	}
	if env.Error.Next != catalog[codeOwnerSandboxDenied].next {
		t.Errorf("envelope next = %q, want the catalog hint", env.Error.Next)
	}
	if strings.Contains(doc.String(), "bugreport") {
		t.Errorf("envelope %q, want no bugreport", doc.String())
	}
}

// TestFailReclassifiesSandboxDeniedInternal pins the central constructor
// rule: a failure built as internal whose arguments -- or failWrap cause --
// carry the sandbox sentinel, wrapped the way the real route wraps it
// (errOwnerUnavailable over db.ErrOpen over the sentinel), is built as
// owner_sandbox_denied with that code's catalog next. A plain internal
// keeps internal and the bugreport hint, and any other code is untouched.
func TestFailReclassifiesSandboxDeniedInternal(t *testing.T) {
	e := fmt.Errorf("%w: %w", errOwnerUnavailable, sandboxRouteError(syscall.EPERM))
	wantNext := catalog[codeOwnerSandboxDenied].next

	ce := requireCLIError(t, fail(codeInternal, "%v", e), codeOwnerSandboxDenied, wantNext)
	if ce.message != fmt.Sprintf("%v", e) {
		t.Errorf("message = %q, want the formatted text unchanged", ce.message)
	}
	if strings.Contains(ce.next, "bugreport") {
		t.Errorf("next = %q, want no bugreport hint", ce.next)
	}
	requireCLIError(t, failNext(codeInternal, "relevo bugreport", "%v", e), codeOwnerSandboxDenied, wantNext)
	requireCLIError(t, failWrap(codeInternal, e, "relevo could not build the runtime"), codeOwnerSandboxDenied, wantNext)

	requireCLIError(t, fail(codeInternal, "%v", errors.New("boom")), codeInternal, "relevo bugreport")
	requireCLIError(t, fail(codeUsage, "%v", e), codeUsage, "relevo help")
}
