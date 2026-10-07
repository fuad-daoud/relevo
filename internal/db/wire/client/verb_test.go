//go:build unix

package client_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

// The tests here cover the client's half of the sync verb surface: the request
// it builds, the answers it accepts, and the token rule. They are written
// against a scripted owner rather than a real one, so each failure a client can
// meet is produced deliberately instead of waited for.

// verbToken is the token a verb request carries. It is long and unmistakable
// because two of these tests assert the value never appears in an answer or an
// error, and a short fixture could turn up in fixed text by accident.
const verbToken = "FIXTURE-TOKEN-b41d7e2c9a05f6381de4b7c2a90f5e61"

// scriptedVerbOwner answers one handshake and then one verb request with
// whatever reply and raw tail the test set.
type scriptedVerbOwner struct {
	// reply is the kind and message the verb gets. A zero kind means the owner
	// answers with a SyncResult, which is the normal path.
	replyKind byte
	reply     *wire.SyncResult
	// dropAfterHello closes the socket instead of answering, which is what an
	// owner that does not know the kind does.
	dropAfterHello bool
	// handed records the request header and raw tail the owner received.
	handedVerb  wire.SyncVerb
	handedToken string
	ready       chan struct{}
	release     chan struct{}
	once        sync.Once
}

// serve runs one connection to completion.
func (s *scriptedVerbOwner) serve(l net.Listener) {
	defer func() { s.once.Do(func() { close(s.ready) }) }()
	nc, err := l.Accept()
	if err != nil {
		return
	}
	defer func() { _ = nc.Close() }()
	w := wire.NewConn(nc)

	frame, err := w.Read()
	if err != nil {
		return
	}
	var hello wire.Hello
	if _, err := wire.Decode(frame, &hello); err != nil {
		return
	}
	welcome, err := wire.Encode(wire.KindWelcome, &wire.Welcome{
		Header: wire.Header{Type: wire.TypeWelcome},
		Proto:  wire.Proto,
	}, nil)
	if err != nil {
		return
	}
	if err := w.Write(welcome); err != nil {
		return
	}
	if s.dropAfterHello {
		return
	}

	frame, err = w.Read()
	if err != nil {
		return
	}
	raw, err := wire.Decode(frame, &s.handedVerb)
	if err != nil {
		return
	}
	s.handedToken = string(raw)
	s.once.Do(func() { close(s.ready) })

	// The answer goes out before any hold, so a test that only wants the reply
	// is not waiting on a channel that is closed at cleanup.
	if s.replyKind == wire.KindRefuse {
		out, _ := wire.Encode(wire.KindRefuse, &wire.Refusal{
			Header: wire.Header{Type: wire.TypeRefuse},
			Code:   wire.RefuseNoSyncVerb,
			// Fixed text with no credential in it, which is what the owner sends
			// and what this asserts the client passes through untouched.
			Message: "this owner does not run sync verbs; upgrade the daemon that is serving it",
		}, nil)
		_ = w.Write(out)
		<-s.release
		return
	}
	if s.reply != nil {
		out, err := wire.Encode(wire.KindSyncResult, s.reply, nil)
		if err != nil {
			return
		}
		_ = w.Write(out)
		<-s.release
		return
	}
	out, err := wire.Encode(wire.KindSyncResult, &wire.SyncResult{OK: true}, nil)
	if err != nil {
		return
	}
	_ = w.Write(out)
	<-s.release
}

// verbSocket serves one scripted owner and returns its socket.
func verbSocket(t *testing.T, s *scriptedVerbOwner) string {
	t.Helper()
	sock := t.TempDir() + "/owner.sock"
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen %s: %v", sock, err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go s.serve(l)
	t.Cleanup(func() { close(s.release) })
	return sock
}

// TestClientSyncVerbShipsTheRequest pins what the client puts on the wire: the
// verb name, the flags it parsed, and the token in the raw tail.
//
// The tail is the whole redaction rule on this surface, so this asserts where
// the value goes rather than only that it arrives: the header must not carry it
// under any field, and the raw bytes must be exactly what the caller passed.
func TestClientSyncVerbShipsTheRequest(t *testing.T) {
	s := &scriptedVerbOwner{ready: make(chan struct{}), release: make(chan struct{})}
	sock := verbSocket(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := client.SyncVerb(ctx, sock, &wire.SyncVerb{
		Header:    wire.Header{Type: wire.TypeSyncVerb},
		Verb:      wire.SyncVerbEnable,
		RemoteURL: "libsql://example.invalid",
		TimeoutMS: 1500,
	}, []byte(verbToken)); err != nil {
		t.Fatalf("SyncVerb: %v", err)
	}

	if s.handedVerb.Verb != wire.SyncVerbEnable {
		t.Errorf("verb = %q, want %q", s.handedVerb.Verb, wire.SyncVerbEnable)
	}
	if s.handedVerb.RemoteURL != "libsql://example.invalid" {
		t.Errorf("remote = %q, want the one the caller named", s.handedVerb.RemoteURL)
	}
	if s.handedVerb.TimeoutMS != 1500 {
		t.Errorf("timeout = %d, want 1500", s.handedVerb.TimeoutMS)
	}
	if s.handedToken != verbToken {
		t.Errorf("the raw tail = %q, want the token the caller passed", s.handedToken)
	}
	// The header is the part a captured stream, a log or an error shows. A
	// token field added here would be a token in all three.
	encoded, err := wire.Encode(wire.KindSyncVerb, &s.handedVerb, nil)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if strings.Contains(string(encoded), verbToken) {
		t.Error("the encoded header carries the token: the value must live only in the raw tail")
	}
}

// TestClientSyncVerbAcceptsTheOwnersResult pins that a result is a result, not
// an error: a failed verb carries a code the caller maps, and only a broken
// stream is an error.
func TestClientSyncVerbAcceptsTheOwnersResult(t *testing.T) {
	s := &scriptedVerbOwner{
		ready:   make(chan struct{}),
		release: make(chan struct{}),
		reply: &wire.SyncResult{
			OK:           true,
			Applied:      true,
			RemoteURL:    "libsql://example.invalid",
			TokenPresent: true,
		},
	}
	sock := verbSocket(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := client.SyncVerb(ctx, sock, &wire.SyncVerb{
		Header: wire.Header{Type: wire.TypeSyncVerb},
		Verb:   wire.SyncVerbPull,
	}, nil)
	if err != nil {
		t.Fatalf("a successful verb returned an error: %v", err)
	}
	if !res.OK || !res.Applied || res.RemoteURL != "libsql://example.invalid" {
		t.Errorf("result = %+v, want the owner's own fields", res)
	}
	if !res.TokenPresent {
		t.Error("the presence of the token did not survive the trip")
	}
	if client.VerbError(res) != nil {
		t.Error("VerbError reported a failure for a successful result")
	}
}

// TestClientSyncVerbClassifiesARefusal pins that a failed verb is classifiable
// by code and that no part of the answer carries the token.
//
// The result is an answer rather than an error, so a caller reads the code. That
// makes the code the contract, which is why these are the closed set the owner
// can send rather than free text.
func TestClientSyncVerbClassifiesARefusal(t *testing.T) {
	s := &scriptedVerbOwner{
		ready:   make(chan struct{}),
		release: make(chan struct{}),
		reply: &wire.SyncResult{
			OK:      false,
			Code:    wire.SyncCodeNoToken,
			Message: "sync: no turso.token: pass --token-stdin or set TURSO_TOKEN",
		},
	}
	sock := verbSocket(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := client.SyncVerb(ctx, sock, &wire.SyncVerb{
		Header: wire.Header{Type: wire.TypeSyncVerb},
		Verb:   wire.SyncVerbEnable,
	}, []byte(verbToken))
	if err != nil {
		t.Fatalf("a refusal returned a transport error: %v", err)
	}
	if res.OK {
		t.Fatal("a refusal reported ok")
	}

	verr := client.VerbError(res)
	if verr == nil {
		t.Fatal("VerbError returned nil for a refusal")
	}
	var refusal *client.VerbRefusal
	if !errors.As(verr, &refusal) {
		t.Fatalf("VerbError returned %T, want a *VerbRefusal", verr)
	}
	if refusal.Code != wire.SyncCodeNoToken {
		t.Errorf("code = %q, want %q", refusal.Code, wire.SyncCodeNoToken)
	}
	if refusal.Message != res.Message {
		t.Errorf("message = %q, want the owner's own %q", refusal.Message, res.Message)
	}
	if strings.Contains(verr.Error(), verbToken) {
		t.Errorf("the classified refusal carries the token: %v", verr)
	}
}

// TestClientSyncVerbSurfacesAnOwnerRefusal pins that a refusal frame mid-request
// is an error the caller sees, naming the upgrade and carrying no token.
//
// This is the new-client/old-owner shape as the client experiences it: the owner
// speaks the handshake and does not speak the verb, and says so in the refusal
// rather than by dropping the stream.
func TestClientSyncVerbSurfacesAnOwnerRefusal(t *testing.T) {
	s := &scriptedVerbOwner{
		ready:     make(chan struct{}),
		release:   make(chan struct{}),
		replyKind: wire.KindRefuse,
		reply:     nil,
	}
	sock := verbSocket(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.SyncVerb(ctx, sock, &wire.SyncVerb{
		Header: wire.Header{Type: wire.TypeSyncVerb},
		Verb:   wire.SyncVerbPush,
	}, []byte(verbToken))
	if err == nil {
		t.Fatal("an owner refusal reported success")
	}
	if strings.Contains(err.Error(), verbToken) {
		t.Errorf("the refusal carries the token: %v", err)
	}
	if !strings.Contains(err.Error(), "upgrade") {
		t.Errorf("refusal %q does not name the upgrade, so a reader cannot act on it", err)
	}
}

// TestClientSyncVerbReportsALostOwner pins that an owner that drops the stream
// after the request is an error rather than a silent success.
//
// The owner may have run the verb before it went away, so the client must not
// invent an answer either way. Reporting a lost connection is what lets a caller
// say the machine's state is unknown rather than that nothing happened.
func TestClientSyncVerbReportsALostOwner(t *testing.T) {
	s := &scriptedVerbOwner{dropAfterHello: true, ready: make(chan struct{}), release: make(chan struct{})}
	sock := verbSocket(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := client.SyncVerb(ctx, sock, &wire.SyncVerb{
		Header: wire.Header{Type: wire.TypeSyncVerb},
		Verb:   wire.SyncVerbPush,
	}, []byte(verbToken))
	if err == nil {
		t.Fatalf("a dropped owner reported %+v, want an error", res)
	}
	if res != nil {
		t.Errorf("a dropped owner produced a result too: %+v", res)
	}
	if strings.Contains(err.Error(), verbToken) {
		t.Errorf("the error carries the token: %v", err)
	}
}

// TestClientSyncVerbHonoursACancelledCaller pins that a caller that gives up
// gets its own context error rather than waiting on the owner.
//
// A verb's whole bound is the caller's, and a caller that has stopped caring
// must be released promptly: the daemon is doing the work either way.
func TestClientSyncVerbHonoursACancelledCaller(t *testing.T) {
	s := &scriptedVerbOwner{ready: make(chan struct{}), release: make(chan struct{})}
	sock := verbSocket(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	// The owner receives the request and then holds it without answering.
	go func() {
		<-s.ready
		cancel()
	}()

	res, err := client.SyncVerb(ctx, sock, &wire.SyncVerb{
		Header: wire.Header{Type: wire.TypeSyncVerb},
		Verb:   wire.SyncVerbPush,
	}, []byte(verbToken))
	if err == nil {
		t.Fatalf("a cancelled verb reported %+v, want an error", res)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want the caller's own context error", err)
	}
}

// TestClientVerbErrorIsTotal pins the classification helper's edges, since every
// caller goes through it.
//
// A nil result and an ok one are both "no failure", which is what lets a caller
// classify unconditionally on the line after the call rather than branching
// first. Anything else becomes a refusal carrying the code it maps.
func TestClientVerbErrorIsTotal(t *testing.T) {
	if err := client.VerbError(nil); err != nil {
		t.Errorf("VerbError(nil) = %v, want nil", err)
	}
	if err := client.VerbError(&wire.SyncResult{OK: true}); err != nil {
		t.Errorf("VerbError(ok) = %v, want nil", err)
	}
	err := client.VerbError(&wire.SyncResult{OK: false, Code: wire.SyncCodeInternal, Message: "boom"})
	var refusal *client.VerbRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("VerbError returned %T, want a *VerbRefusal", err)
	}
	if refusal.Code != wire.SyncCodeInternal || refusal.Message != "boom" {
		t.Errorf("refusal = %+v, want the result's own code and message", refusal)
	}
	if refusal.Error() != "boom" {
		t.Errorf("Error() = %q, want the message", refusal.Error())
	}
}

// TestClientSyncVerbRefusesAnUndialledSocket pins that a verb to a socket nobody
// is serving is an error rather than a result.
//
// It is the shape a user meets with the daemon stopped, and it must be a
// transport error the caller maps, not an empty result that reads as success.
func TestClientSyncVerbRefusesAnUndialledSocket(t *testing.T) {
	sock := t.TempDir() + "/nothing.sock"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := client.SyncVerb(ctx, sock, &wire.SyncVerb{
		Header: wire.Header{Type: wire.TypeSyncVerb},
		Verb:   wire.SyncVerbPush,
	}, nil)
	if err == nil {
		t.Fatalf("an unserved socket reported %+v, want an error", res)
	}
}
