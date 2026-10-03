package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// signedGetAsync issues a signed GET from the test goroutine itself. It exists
// because doSigned calls t.Fatalf, which is only legal on the test goroutine,
// while the contention assertions below need the request to run concurrently
// with a blocked tick. The client carries a timeout so that a regression fails
// the assertion instead of hanging until the test binary's own deadline.
func signedGetAsync(t *testing.T, ts *httptest.Server, kp remote.Keypair, path string) (*http.Response, error) {
	t.Helper()
	req := signedRequest(t, kp, "GET", path, nil)
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(ts.URL, "http://")
	client := &http.Client{Timeout: 10 * time.Second}
	return client.Do(req)
}

// holdOwnerStoreLock takes the given owner's store lock in a background
// goroutine and parks there until the returned release function is called.
// Nothing else can, because no other path needs that owner's lock to serve a
// different owner. The release is registered with t.Cleanup, so it runs on
// failure too: a regression fails the assertions that follow and then drains
// rather than leaving a blocked handler to stall httptest's Close. The returned
// held channel closes once the lock has actually been released.
func holdOwnerStoreLock(t *testing.T, env *testEnv, id remote.ClientID) (<-chan struct{}, func()) {
	t.Helper()
	rt := testRuntime(t, env.srv, id)
	held := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseLock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseLock)
	lockHeld := make(chan struct{})
	go func() {
		_ = rt.Store.WithLock(func(*store.Tx) error {
			close(lockHeld)
			<-release
			return nil
		})
		close(held)
	}()
	<-lockHeld
	return held, releaseLock
}

// startBlockedTick launches one Tick on its own goroutine and returns the
// channel its result arrives on. The returned channel is buffered so the tick
// always finishes, whichever way the caller stops reading.
func startBlockedTick(env *testEnv) <-chan error {
	tickDone := make(chan error, 1)
	go func() { tickDone <- env.srv.Tick(context.Background()) }()
	return tickDone
}

// requirePollCompletesDuringBlockedTick asserts that another owner's binding
// poll completes while the first owner's tick is still stuck. The client's own
// timeout bounds this, so the old whole-tick lock fails the assertion rather
// than hanging the suite.
func requirePollCompletesDuringBlockedTick(t *testing.T, env *testEnv, ownerB ownerEnv) {
	t.Helper()
	pollDone := make(chan *http.Response, 1)
	pollErr := make(chan error, 1)
	go func() {
		resp, err := signedGetAsync(t, env.ts, ownerB.kp, "/v1/bindings/api")
		if err != nil {
			pollErr <- err
			return
		}
		pollDone <- resp
	}()

	select {
	case resp := <-pollDone:
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("B poll status = %d, want 200", resp.StatusCode)
		}
	case err := <-pollErr:
		t.Fatalf("B's poll did not complete while owner A's tick was blocked: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("B's poll was blocked by owner A's slow tick")
	}
}

// requireTickStillBlocked asserts the tick has not returned: the poll cannot
// have raced a finished tick, so it really did run alongside a blocked
// reconcile.
func requireTickStillBlocked(t *testing.T, tickDone <-chan error) {
	t.Helper()
	select {
	case err := <-tickDone:
		t.Fatalf("tick returned (%v) while A's lock was held; it never reached A's reconcile", err)
	default:
	}
}

// requireAckAndRoundStartComplete exercises the same mechanism as the poll: ack
// and round start are neither serialized behind an unrelated owner's reconcile.
// Both run on B, which the test never locks. The round-2 admit takes admitMu,
// which A's blocked reconcile never holds, so the start completes too.
func requireAckAndRoundStartComplete(t *testing.T, env *testEnv, ownerB ownerEnv) {
	t.Helper()
	resp, body := doSigned(t, env.ts, ownerB.kp, "GET", "/v1/bindings/api", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("B poll status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}

	formBytes, ct := makeRoundForm(t, 2, "# Plan B2", nil)
	resp, body = doSigned(t, env.ts, ownerB.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	if resp.StatusCode == http.StatusInternalServerError {
		t.Errorf("B round 2 status = %d; body: %s", resp.StatusCode, string(body))
	}
}

// releaseOwnerLockAndDrain releases the held store lock and waits for the lock
// holder and then the tick to finish, failing the test if either stalls.
func releaseOwnerLockAndDrain(t *testing.T, held <-chan struct{}, releaseLock func(), tickDone <-chan error) {
	t.Helper()
	releaseLock()
	select {
	case <-held:
	case <-time.After(20 * time.Second):
		t.Fatal("A's store lock was never released")
	}
	select {
	case err := <-tickDone:
		if err != nil {
			t.Errorf("tick after release: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("tick did not finish after A's lock was released")
	}
}

// TestSlowOwnerTickDoesNotBlockAnotherOwner pins the split of serve.Server.Tick's
// global lock: while owner A's reconcile is held open, owner B's poll, ack and
// round start all complete.
//
// The stall is A's own store lock, held by this test for the whole body. A tick
// that held s.mu across its per-owner reconcile would keep s.mu for as long as A
// is blocked, and every one of B's requests would queue behind it; the
// per-owner split means B only ever waits on B's own lock, which is free.
func TestSlowOwnerTickDoesNotBlockAnotherOwner(t *testing.T) {
	env := setupTestEnv(t)
	ownerB := addOwner(t, env, "bob")

	// A gets a running round so its tick reaches a per-binding reconcile and
	// therefore blocks on the lock this test holds.
	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")

	// B gets its own running round, so the poll below reads a live binding.
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if view := decodeView(t, bodyB); view.RoundState != remote.RoundRunning {
		t.Fatalf("B round_state = %q, want running", view.RoundState)
	}

	// A's tick cannot get past the lock held for it here.
	held, releaseLock := holdOwnerStoreLock(t, env, env.id)
	tickDone := startBlockedTick(env)

	requirePollCompletesDuringBlockedTick(t, env, ownerB)
	requireTickStillBlocked(t, tickDone)
	requireAckAndRoundStartComplete(t, env, ownerB)

	releaseOwnerLockAndDrain(t, held, releaseLock, tickDone)
}

// TestFailingOwnerDoesNotStopOtherOwnersReconcile pins the per-owner error
// isolation the tick keeps: one owner's reconcile returning an error is logged
// and the walk goes on to the next owner in the same tick.
func TestFailingOwnerDoesNotStopOtherOwnersReconcile(t *testing.T) {
	env := setupTestEnv(t)
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")

	// Break A's reconcile by removing the bare repo it works in, so its store
	// rows still list but its git reads fail. B's round must still reconcile.
	rtA := testRuntime(t, env.srv, env.id)
	bA, err := rtA.Store.Load("api")
	if err != nil {
		t.Fatalf("load A: %v", err)
	}
	if bA.Serve == nil || bA.Serve.BareRepo == "" {
		t.Fatal("A has no bare repo facts to remove")
	}
	if err := os.RemoveAll(bA.Serve.BareRepo); err != nil {
		t.Fatalf("remove A bare repo: %v", err)
	}

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick returned %v; a failing owner must not fail the tick", err)
	}

	// B's binding is still readable and its builder was reconciled, which means
	// the walk reached B after A failed.
	rtB := testRuntime(t, env.srv, ownerB.id)
	if _, err := rtB.Store.Load("api"); err != nil {
		t.Fatalf("load B after A failed: %v", err)
	}
	if specs := startedSpecs(env); len(specs) < 2 {
		t.Errorf("runner specs = %d, want at least 2 (both owners reconciled)", len(specs))
	}
}
