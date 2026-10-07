//go:build unix

package owner_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
)

// The tests here cover the owner's half of the sync verb surface: the dispatch,
// the refusals, the recovery, and the token rule. Each refusal the owner can
// send is produced deliberately, because a verb that is silently not run leaves
// a machine that reads as syncing and is not.

// ownerVerbToken is the token a verb request carries. Two of these tests assert
// the value never reaches a log or a message, so the fixture is long and
// unmistakable rather than something a fixed text might contain.
const ownerVerbToken = "FIXTURE-TOKEN-9c3f81b4d27e60a5c8b1f4d9037e2a6c"

// shortVerbSock is a socket path short enough for sun_path.
//
// A subtest's own t.TempDir() name carries the parent's name and the subtest's,
// which together run past the ~104-byte limit on a unix socket. The file is under
// the per-test temp root either way; only the name is shortened.
func shortVerbSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "vo")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "o.sock")
}

// servedVerbOwner is an owner over a split pair with a scripted executor, plus
// the socket a test dials to reach it.
type servedVerbOwner struct {
	// server is the owner being served, so a test can drain or close it.
	server *owner.Server
	srv    *db.DB
	sock   string
	ln     net.Listener
}

// newServedVerbOwner serves an owner whose executor is whatever the test
// installed. A nil hook leaves the executor unset, which is the state a verb has
// to be refused from.
func newServedVerbOwner(t *testing.T, hook func(context.Context, *wire.SyncVerb, []byte) *wire.SyncResult) *servedVerbOwner {
	t.Helper()
	path := t.TempDir() + "/relevo.db"
	handle, err := db.OpenSplit(path, db.Options{})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	sock := shortVerbSock(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen %s: %v", sock, err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	srv := db.NewOwner(handle)
	if hook != nil {
		srv.OnSyncVerb = hook
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return &servedVerbOwner{server: srv, srv: handle, sock: sock, ln: ln}
}

// send performs one verb against the served owner.
func (o *servedVerbOwner) send(t *testing.T, verb string, token []byte) (*wire.SyncResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return client.SyncVerb(ctx, o.sock, &wire.SyncVerb{
		Header: wire.Header{Type: wire.TypeSyncVerb},
		Verb:   verb,
	}, token)
}

// TestOwnerRunsAVerbThroughTheHook pins that a verb reaches the installed
// executor with its request and its token, and that its result is the answer.
//
// The hook is called on the request's own context and receives the token as raw
// bytes, so this is where the redaction rule starts: the value arrives here and
// the only thing that leaves is the result.
func TestOwnerRunsAVerbThroughTheHook(t *testing.T) {
	var gotVerb string
	var gotToken string
	var mu sync.Mutex
	o := newServedVerbOwner(t, func(_ context.Context, verb *wire.SyncVerb, token []byte) *wire.SyncResult {
		mu.Lock()
		gotVerb, gotToken = verb.Verb, string(token)
		mu.Unlock()
		return &wire.SyncResult{OK: true, Applied: true, TokenPresent: true}
	})

	res, err := o.send(t, wire.SyncVerbEnable, []byte(ownerVerbToken))
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !res.OK || !res.Applied || !res.TokenPresent {
		t.Errorf("result = %+v, want the hook's own", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotVerb != wire.SyncVerbEnable {
		t.Errorf("the hook saw verb %q, want %q", gotVerb, wire.SyncVerbEnable)
	}
	if gotToken != ownerVerbToken {
		t.Errorf("the hook saw token %q, want the one the request carried", gotToken)
	}
}

// TestOwnerRefusesAVerbWithoutAnExecutor pins that an owner with no executor
// refuses the verb rather than accepting it and doing nothing.
//
// A silent no-op would be the worst possible answer here: the machine would read
// as syncing and would not be. The refusal names the upgrade, because the owner
// that answered is new enough to have been asked.
func TestOwnerRefusesAVerbWithoutAnExecutor(t *testing.T) {
	o := newServedVerbOwner(t, nil)

	// The refusal ends the connection rather than being a result: the owner
	// refuses in its handshake shape, which is a frame the client turns into an
	// error. What matters is that the caller is told, and told without a token.
	res, err := o.send(t, wire.SyncVerbPush, []byte(ownerVerbToken))
	if err == nil && res.OK {
		t.Fatalf("an owner with no executor answered %+v, want a refusal", res)
	}
	if err == nil {
		t.Fatal("the refusal reported ok")
	}
	if !strings.Contains(err.Error(), "upgrade") {
		t.Errorf("refusal %q does not name the upgrade, so a reader cannot act on it", err)
	}
	if strings.Contains(err.Error(), ownerVerbToken) {
		t.Errorf("the refusal carries the token: %v", err)
	}
}

// TestOwnerRefusesAVerbWithATokenInNoAnswer pins that no answer path echoes the
// token, across every verb and both outcomes.
//
// The refusal and the result are the only two things a caller sees, so a token
// appearing in either is the leak. Every verb is walked because a dispatch that
// forgot to carry the tail for one of them would show up as an empty token on
// that one rather than as a failure anywhere else.
func TestOwnerRefusesAVerbWithATokenInNoAnswer(t *testing.T) {
	verbs := []string{
		wire.SyncVerbEnable, wire.SyncVerbDisable,
		wire.SyncVerbPush, wire.SyncVerbPull,
	}
	for _, verb := range verbs {
		t.Run(verb, func(t *testing.T) {
			o := newServedVerbOwner(t, func(_ context.Context, _ *wire.SyncVerb, _ []byte) *wire.SyncResult {
				return &wire.SyncResult{OK: true, TokenPresent: true}
			})
			res, err := o.send(t, verb, []byte(ownerVerbToken))
			if err != nil {
				t.Fatalf("%s: %v", verb, err)
			}
			if !res.TokenPresent {
				t.Errorf("%s did not reach the hook with its token", verb)
			}
			if res.Code == ownerVerbToken || res.Message == ownerVerbToken {
				t.Errorf("%s echoed the token in its answer", verb)
			}
		})
	}
}

// TestOwnerRefusesADrainingVerb pins that a draining owner refuses a verb, and
// says the request never ran.
//
// The guarantee is the one every other refusal makes: a refusal is an answer the
// caller may retry, because the owner knows the work did not happen. A verb
// inside an open transaction is let through, exactly as a statement is, so that
// transaction can commit.
func TestOwnerRefusesADrainingVerb(t *testing.T) {
	called := make(chan struct{}, 1)
	o := newServedVerbOwner(t, func(_ context.Context, _ *wire.SyncVerb, _ []byte) *wire.SyncResult {
		called <- struct{}{}
		return &wire.SyncResult{OK: true}
	})

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDrain()
	if err := o.server.Drain(drainCtx); err != nil {
		t.Fatalf("drain: %v", err)
	}

	// Drain quiets the listener and drops the clients, so a connection that
	// arrives now is answered with the restarting refusal rather than served.
	res, err := o.send(t, wire.SyncVerbPush, []byte(ownerVerbToken))
	if err == nil && res.OK {
		t.Fatal("a verb ran against a draining owner")
	}
	if err != nil {
		if strings.Contains(err.Error(), ownerVerbToken) {
			t.Errorf("the refusal carries the token: %v", err)
		}
		return
	}
	if res.Code == ownerVerbToken || res.Message == ownerVerbToken {
		t.Error("the refusal echoes the token")
	}
	select {
	case <-called:
		t.Error("the hook ran while the owner was draining")
	default:
	}
}

// TestOwnerTurnsAPanickingHookIntoARefusal pins that a hook which panics answers
// with fixed text rather than taking the daemon down with it.
//
// The panic value is whatever the runtime happened to be holding, and the token
// was on the stack while it happened -- so the message that goes back must be
// written here, not formatted from the panic. This is the case a deferred
// recover in the dispatch exists for.
func TestOwnerTurnsAPanickingHookIntoARefusal(t *testing.T) {
	o := newServedVerbOwner(t, func(_ context.Context, _ *wire.SyncVerb, token []byte) *wire.SyncResult {
		panic("boom: " + string(token))
	})

	res, err := o.send(t, wire.SyncVerbPush, []byte(ownerVerbToken))
	if err != nil {
		t.Fatalf("a panicking hook took the connection instead of answering: %v", err)
	}
	if res.OK {
		t.Fatal("a panicking hook reported success")
	}
	if res.Code != wire.SyncCodeInternal {
		t.Errorf("code = %q, want %q", res.Code, wire.SyncCodeInternal)
	}
	if strings.Contains(res.Message, ownerVerbToken) {
		t.Errorf("the refusal carries the token: %s", res.Message)
	}
	if strings.Contains(res.Message, "boom") {
		t.Errorf("the refusal quotes the panic value: %s", res.Message)
	}
}

// TestOwnerRefusesAHookThatAnswersNothing pins that a hook returning nil is a
// refusal rather than an empty success.
//
// A nil result would leave the caller with nothing to classify, and a caller
// that treated "no answer" as success would report a machine as synced when the
// verb never reported doing anything.
func TestOwnerRefusesAHookThatAnswersNothing(t *testing.T) {
	o := newServedVerbOwner(t, func(context.Context, *wire.SyncVerb, []byte) *wire.SyncResult {
		return nil
	})

	res, err := o.send(t, wire.SyncVerbPush, []byte(ownerVerbToken))
	if err != nil {
		t.Fatalf("a nil result took the connection: %v", err)
	}
	if res.OK {
		t.Fatal("a hook that answered nothing reported success")
	}
	if res.Code != wire.SyncCodeInternal {
		t.Errorf("code = %q, want %q", res.Code, wire.SyncCodeInternal)
	}
}

// TestOwnerStampsTheResultType pins that the answer is a typed result frame, so
// a captured stream reads on its own.
//
// The kind byte and the header type are two halves of one fact: a reader that saw
// the kind but no type would have nothing to name the frame by.
func TestOwnerStampsTheResultType(t *testing.T) {
	var seenType string
	o := newServedVerbOwner(t, func(_ context.Context, _ *wire.SyncVerb, _ []byte) *wire.SyncResult {
		// A result with no type set, which is what a hook that builds one by
		// hand leaves behind. The owner fills it in.
		return &wire.SyncResult{OK: true}
	})

	res, err := o.send(t, wire.SyncVerbPush, nil)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	seenType = res.Type
	if seenType != wire.TypeSyncResult {
		t.Errorf("the result's type = %q, want %q", seenType, wire.TypeSyncResult)
	}
}

// TestOwnerKeepsAConnUsableAfterAVerb pins that a verb does not wedge the
// connection it arrived on.
//
// A verb takes the connection's single request slot while it runs and releases it
// on the way out; a slot left taken would make the next request on that
// connection wait for a discard timeout and then be dropped, which a client
// would see as its second statement failing for no reason.
func TestOwnerKeepsAConnUsableAfterAVerb(t *testing.T) {
	path := t.TempDir() + "/relevo.db"
	handle, err := db.OpenSplit(path, db.Options{})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	sock := shortVerbSock(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := db.NewOwner(handle)
	srv.OnSyncVerb = func(context.Context, *wire.SyncVerb, []byte) *wire.SyncResult {
		return &wire.SyncResult{OK: true}
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dialed, err := db.DialContext(ctx, sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = dialed.Close() })

	// A statement on the same handle the verbs go through, after a verb ran.
	res, err := dialed.SyncVerb(ctx, &wire.SyncVerb{
		Header: wire.Header{Type: wire.TypeSyncVerb},
		Verb:   wire.SyncVerbPush,
	}, nil)
	if err != nil {
		t.Fatalf("verb: %v", err)
	}
	if !res.OK {
		t.Fatalf("verb refused: %s", res.Message)
	}
	if err := dialed.KVPut("after-a-verb", []byte(`"ok"`)); err != nil {
		t.Errorf("a statement after a verb failed: %v", err)
	}
}

// TestOwnerRefusesAVerbFromADirectHandle pins that the verb entry point needs an
// owner, so a handle that did not dial one refuses rather than reaching for a
// path.
//
// The verb exists so a client never opens the file; a fallback that opened one
// would put the file-lock conflict straight back, which is the whole thing the
// route removes.
func TestOwnerRefusesAVerbFromADirectHandle(t *testing.T) {
	path := t.TempDir() + "/relevo.db"
	handle, err := db.OpenSplit(path, db.Options{})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	if handle.Sock() != "" {
		t.Skip("this handle reports a socket, so it cannot model a direct open")
	}
	if _, err := handle.SyncVerb(context.Background(), &wire.SyncVerb{
		Header: wire.Header{Type: wire.TypeSyncVerb},
		Verb:   wire.SyncVerbPush,
	}, nil); err == nil {
		t.Error("a direct handle ran a verb, so the CLI would have an open again")
	} else if !errors.Is(err, db.ErrOpen) {
		t.Errorf("the refusal is %v, want it to name a failed open", err)
	}
}
