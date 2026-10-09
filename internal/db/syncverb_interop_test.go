//go:build unix

package db_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
)

// The interop pins live here because they are about the wire, and the wire is
// where an upgrade window is either honoured or lost. Both directions are
// additive: S7 adds kinds and never bumps Version, so an old peer and a new one
// keep talking across an upgrade in either order.

// verbInteropToken is the token a verb request carries. The interop assertions
// are about a token surviving an upgrade window, so this one must be long and
// unmistakable -- a short value could appear in a refusal's fixed text by
// accident and the assertion would pass for the wrong reason.
const verbInteropToken = "FIXTURE-TOKEN-2b7d4e1a9c6f8035e2d4a7b1c9f60e35"

// TestSyncOldClientNewOwner pins that the new owner serves an old client
// byte-for-byte as before.
//
// The old client is modelled by writing the frames it would write -- hello,
// exec, query, and a close -- and reading the frames it expects back. It knows
// nothing about sync verbs, which is the point: an owner that changed the
// handshake, the welcome or either statement path to accommodate them would
// break every client already deployed, and this test is what says it did not.
func TestSyncOldClientNewOwner(t *testing.T) {
	_, raw := interopOwner(t, func(*owner.Server) {})

	welcome := oldClientHandshake(t, raw)
	assertVersionUnbumped(t, welcome)

	// An exec, the request an old client makes most.
	if err := raw.send(wire.KindExec, &wire.Exec{
		Header: wire.Header{Type: wire.TypeExec, ID: 1},
		Query:  `CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)`,
	}, nil); err != nil {
		t.Fatalf("send exec: %v", err)
	}
	if kind, _, err := raw.read(); err != nil {
		t.Fatalf("read the exec answer: %v", err)
	} else if kind != wire.KindDone {
		t.Fatalf("exec answered kind %d, want a done", kind)
	}

	oldClientQuery(t, raw)
}

// oldClientHandshake performs the handshake an old client performs and returns
// the welcome: only proto, version and a zero schema_know, with no ad-hoc
// marker and no scope.
func oldClientHandshake(t *testing.T, raw *rawInterop) wire.Welcome {
	t.Helper()
	if err := raw.send(wire.KindHello, &wire.Hello{
		Header:  wire.Header{Type: wire.TypeHello},
		Proto:   wire.Proto,
		Version: wire.Version,
	}, nil); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	kind, frame, err := raw.read()
	if err != nil {
		t.Fatalf("read welcome: %v", err)
	}
	if kind != wire.KindWelcome {
		t.Fatalf("hello answered kind %d, want a welcome", kind)
	}
	var welcome wire.Welcome
	if _, err := wire.Decode(frame, &welcome); err != nil {
		t.Fatalf("decode welcome: %v", err)
	}
	return welcome
}

// assertVersionUnbumped pins that the verb surface was additive.
//
// The bump assertion is stated as its own fact rather than as a comparison
// against whatever the constant happens to be: a comparison tracks the constant
// and so can never catch it changing. S7 adds kinds and leaves the version at 1,
// and a change to that number is a protocol break every deployed client would
// refuse at the handshake.
func assertVersionUnbumped(t *testing.T, welcome wire.Welcome) {
	t.Helper()
	if wire.Version != 1 {
		t.Errorf("wire.Version = %d, want 1: the verb kinds are additive and must not bump the protocol version", wire.Version)
	}
	if welcome.Version != wire.Version {
		t.Errorf("welcome version = %d, want %d", welcome.Version, wire.Version)
	}
	// This owner is built over a split pair, so it does serve a local file and
	// the welcome says so. The field is what lets a dialled handle attach one;
	// an old client that never asks for it is unaffected either way, which is
	// the additive property this test is about.
	if !welcome.HasLocal {
		t.Error("the welcome denies the local file an owner built over a split pair serves")
	}
}

// oldClientQuery runs the query an old client streams and drains it to its done.
func oldClientQuery(t *testing.T, raw *rawInterop) {
	t.Helper()
	if err := raw.send(wire.KindQuery, &wire.Query{
		Header: wire.Header{Type: wire.TypeQuery, ID: 2},
		Query:  `SELECT count(*) FROM t`,
	}, nil); err != nil {
		t.Fatalf("send query: %v", err)
	}
	kind, frame, err := raw.read()
	if err != nil {
		t.Fatalf("read the query answer: %v", err)
	}
	if kind != wire.KindRows {
		t.Fatalf("query answered kind %d, want rows", kind)
	}
	var rows wire.Rows
	if _, err := wire.Decode(frame, &rows); err != nil {
		t.Fatalf("decode rows: %v", err)
	}
	// And the terminating done an old client expects to be able to drain.
	kind, _, err = raw.read()
	if err != nil {
		t.Fatalf("read the done: %v", err)
	}
	if kind != wire.KindDone {
		t.Errorf("the stream ended with kind %d, want a done", kind)
	}
}

// TestSyncNewClientOldOwner pins that a verb sent to an owner that predates it
// refuses by naming the upgrade, with no token in the answer.
//
// The old owner is modelled by refusing every kind it does not know, which is
// what the owner's own loop does for an unrecognised byte. A client that meets
// that must not report it as a lost connection and must not report it as success:
// either would leave a user believing their machine is syncing when it is not.
// The refusal has to be actionable, and it has to carry no credential.
func TestSyncNewClientOldOwner(t *testing.T) {
	sock, raw := interopOwner(t, nil)

	// The handshake succeeds: the two are the same protocol version, which is
	// what makes the verb's absence the only difference.
	if err := raw.send(wire.KindHello, &wire.Hello{
		Header:  wire.Header{Type: wire.TypeHello},
		Proto:   wire.Proto,
		Version: wire.Version,
	}, nil); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	if kind, _, err := raw.read(); err != nil || kind != wire.KindWelcome {
		t.Fatalf("handshake = kind %d, %v; want a welcome", kind, err)
	}

	// An old owner does not know the verb kind and drops the connection. Either
	// answer is a refusal; what must not happen is a silent success or an
	// unclassifiable error.
	err := sendVerbAndClassify(t, sock, verbInteropToken)
	if err == nil {
		t.Fatal("a verb against an owner that does not speak it reported success")
	}
	if strings.Contains(err.Error(), verbInteropToken) {
		t.Errorf("the refusal carries the token: %v", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "upgrade") &&
		!strings.Contains(strings.ToLower(err.Error()), "owner") {
		t.Errorf("refusal %q names neither the owner nor an upgrade, so a reader cannot act on it", err)
	}
}

// TestSyncVerbRefusedByAnOwnerWithoutAnExecutor pins the other way a verb can go
// unanswered: the owner speaks the kind but has no executor installed.
//
// That is a real state -- an owner built by a test, or a daemon whose handle
// carries no machine-local file -- and it must refuse rather than answer as
// though sync had run. A silent no-op would leave a machine that reads as
// syncing and is not.
func TestSyncVerbRefusedByAnOwnerWithoutAnExecutor(t *testing.T) {
	sock, _ := interopOwner(t, nil)

	err := sendVerbAndClassify(t, sock, verbInteropToken)
	if err == nil {
		t.Fatal("a verb against an owner with no executor reported success")
	}
	if strings.Contains(err.Error(), verbInteropToken) {
		t.Errorf("the refusal carries the token: %v", err)
	}
	var refusal *wire.Refusal
	if !errorsAs(err, &refusal) {
		t.Fatalf("refusal %v is not a wire refusal, so a caller cannot map it", err)
	}
	if refusal.Code != wire.RefuseNoSyncVerb {
		t.Errorf("code = %q, want %q", refusal.Code, wire.RefuseNoSyncVerb)
	}
	if !strings.Contains(refusal.Message, "upgrade") {
		t.Errorf("message %q does not name the upgrade", refusal.Message)
	}
}

// TestSyncVerbTokenAbsentFromTheWireTranscript pins that the token is in the
// frame and nowhere else.
//
// Every frame the owner reads for one verb is recorded and the fixture token
// must appear in exactly one of them: the request. A second appearance would
// mean the value was echoed, logged or put into a response -- the three ways a
// credential leaks on a surface like this one, and none of which a grep over
// log output alone would catch.
func TestSyncVerbTokenAbsentFromTheWireTranscript(t *testing.T) {
	var seen []string
	sock := interopEchoOwner(t, func(_ context.Context, _ *wire.SyncVerb, token []byte) *wire.SyncResult {
		seen = append(seen, string(token))
		return &wire.SyncResult{OK: true, TokenPresent: len(token) > 0}
	})

	res, err := client.SyncVerb(context.Background(), sock, &wire.SyncVerb{
		Header: wire.Header{Type: wire.TypeSyncVerb},
		Verb:   wire.SyncVerbPush,
	}, []byte(verbInteropToken))
	if err != nil {
		t.Fatalf("send the verb: %v", err)
	}
	if !res.OK {
		t.Fatalf("the verb refused: %s", res.Message)
	}

	if len(seen) != 1 || seen[0] != verbInteropToken {
		t.Fatalf("the hook received %q, want the one token the request carried", seen)
	}
	// The response carries a presence and not the value: this is the field a
	// future change is most likely to widen into the token itself.
	if !res.TokenPresent {
		t.Error("the result did not report the token's presence")
	}
	if strings.Contains(res.Message, verbInteropToken) ||
		strings.Contains(res.Code, verbInteropToken) {
		t.Errorf("the result carries the token: %+v", res)
	}
}

// errorsAs is errors.As, spelled out for the one call that needs it. It unwraps
// rather than type-asserting directly, because the client's transport path wraps
// whatever the owner refused.
func errorsAs(err error, target **wire.Refusal) bool {
	for err != nil {
		var r *wire.Refusal
		if errors.As(err, &r) {
			*target = r
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// interopOwner serves an owner over a socket and returns it with a raw
// connection helper. install may be nil, which leaves the executor unset.
func interopOwner(t *testing.T, install func(*owner.Server)) (string, *rawInterop) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relevo.db")
	handle, err := db.OpenSplit(path, db.Options{})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	ln, sock := interopListener(t)

	srv := db.NewOwner(handle)
	if install != nil {
		install(srv)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock, dialInterop(t, sock)
}

// interopEchoOwner serves an owner whose executor records what it was handed,
// and returns the socket to send to. It is separate from interopOwner because
// this one needs a hook installed, and the tests that model an old owner must
// leave the executor unset on purpose.
func interopEchoOwner(t *testing.T, hook func(context.Context, *wire.SyncVerb, []byte) *wire.SyncResult) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "echo.db")
	handle, err := db.OpenSplit(path, db.Options{})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	ln, sock := interopListener(t)
	srv := db.NewOwner(handle)
	srv.OnSyncVerb = hook
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// sendVerbAndClassify sends one verb and returns the refusal a caller would see.
func sendVerbAndClassify(t *testing.T, sock, token string) error {
	t.Helper()
	res, err := client.SyncVerb(context.Background(), sock, &wire.SyncVerb{
		Header: wire.Header{Type: wire.TypeSyncVerb},
		Verb:   wire.SyncVerbPush,
	}, []byte(token))
	if err != nil {
		return err
	}
	return client.VerbError(res)
}

// rawInterop is a bare framed connection, which is how a client that predates
// the verb surface is modelled: it writes the frames it knows and nothing else.
type rawInterop struct {
	c *wire.Conn
}

func dialInterop(t *testing.T, sock string) *rawInterop {
	t.Helper()
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial %s: %v", sock, err)
	}
	t.Cleanup(func() { _ = nc.Close() })
	return &rawInterop{c: wire.NewConn(nc)}
}

func (r *rawInterop) send(kind byte, v any, raw []byte) error {
	payload, err := wire.Encode(kind, v, raw)
	if err != nil {
		return err
	}
	return r.c.Write(payload)
}

func (r *rawInterop) read() (byte, []byte, error) {
	frame, err := r.c.Read()
	if err != nil {
		return 0, nil, err
	}
	kind, err := wire.Kind(frame)
	if err != nil {
		return 0, nil, err
	}
	return kind, frame, nil
}

// interopListener binds the owner's socket directly under /tmp: a socket path is
// limited to 104 bytes, and a t.TempDir() under macOS's long temp root is past
// it.
func interopListener(t *testing.T) (net.Listener, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rvo-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "o.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen %s: %v", sock, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln, sock
}
