package serve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// setServedBindingBroken puts a served binding in the state a failed switch
// leaves it in: the round is still open, the builder process behind it is gone,
// and halt names the reason it recorded.
//
// An empty halt is what a binding broken before the reason was recorded looks
// like on disk, and it is what keeps the 500 this file pins reachable rather
// than theoretical: the server's own refusal asks whether a binding is halted
// or carries a reason, and a broken binding with neither answers no to both.
func setServedBindingBroken(t *testing.T, env *testEnv, name, halt string) {
	t.Helper()
	rt := env.runtime(t)
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}
		b.State = store.StateBroken
		b.Halt = halt
		// The process goes with the state word. A binding that keeps its pid and
		// its round clock is one the server's next tick will retry, so it goes
		// on the wire as running -- a break the daemon is about to fix, which
		// these tests are not about.
		b.Builder.PID = 0
		b.RoundStartedAt = time.Time{}
		return tx.Save(b)
	}); err != nil {
		t.Fatalf("break binding: %v", err)
	}
}

// breakServedBinding is setServedBindingBroken for the reasonless case.
func breakServedBinding(t *testing.T, env *testEnv, name string) {
	t.Helper()
	setServedBindingBroken(t, env, name, "")
}

// TestRoundStartBrokenSamePlanIs200 pins the retry half: a broken round is an
// open round, so an identical resend of its plan is the no-op it is for a
// running one -- the 200 view, nothing absorbed, nothing started.
//
// The open-round checks named only running and queued, so a broken binding fell
// through both of them and the retry went on to absorb its bundle and move the
// server worktree before Send refused it.
func TestRoundStartBrokenSamePlanIs200(t *testing.T) {
	env := setupTestEnv(t)

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, body, http.StatusCreated)

	breakServedBinding(t, env, "api")

	entriesBefore, err := env.runtime(t).Store.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	before := startedSpecs(env)

	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, body, http.StatusOK)

	got := decodeView(t, body)
	if got.RoundState != remote.RoundBroken {
		t.Errorf("round_state = %q, want %q", got.RoundState, remote.RoundBroken)
	}

	entriesAfter, err := env.runtime(t).Store.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog after: %v", err)
	}
	if len(entriesAfter) != len(entriesBefore) {
		t.Errorf("log entries = %d, want %d (an identical retry appends nothing)", len(entriesAfter), len(entriesBefore))
	}
	if after := startedSpecs(env); len(after) != len(before) {
		t.Errorf("runner starts = %d, want %d (an identical retry starts no builder)", len(after), len(before))
	}
}

// TestRoundStartBrokenDifferentPlanIs409 pins the refusal half: a different plan
// for a round that is still open is round_open, exactly as it is for a running
// one, and it is answered from the fast path -- nothing absorbed, no ref moved.
//
// This used to fall through to the second half of the start, absorb the bundle
// and move the server worktree, and only then be refused by Send.
func TestRoundStartBrokenDifferentPlanIs409(t *testing.T) {
	env := setupTestEnv(t)

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, body, http.StatusCreated)

	breakServedBinding(t, env, "api")

	otherBytes, otherCT := makeRoundForm(t, 1, "# A Different Plan", nil)
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", otherBytes, otherCT)
	requireStatus(t, resp, body, http.StatusConflict)
	var errBody remote.ErrorBody
	_ = json.Unmarshal(body, &errBody)
	if errBody.Code != remote.CodeRoundOpen {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeRoundOpen)
	}
}

// TestRoundStartBrokenPriorRoundSamePlanIs200 pins the prior round's half of
// the retry: a resend of the plan the server already accepted for round N-1 is
// the no-op whatever round N is doing now, broken included.
//
// The broken arm sat above the b.Round-1 retry arm, so this request -- one the
// server has already taken and answered 201 to -- was refused 409, which the
// client maps to a hard error and no retry. The client that lost the first
// response had no way back.
func TestRoundStartBrokenPriorRoundSamePlanIs200(t *testing.T) {
	env := setupTestEnv(t)

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, body, http.StatusCreated)

	// Close round 1 so the binding sits on round 2, which is the pair this
	// request names: an accepted resend of the round before the current one.
	finishRound(t, env, env.runtime(t), "api", 1)
	// Ack it too, so the view reads the break rather than the close the owner
	// has not settled yet: RoundStateOf ranks the close above the break.
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds/1/ack", nil, "")
	requireStatus(t, resp, body, http.StatusOK)

	setServedBindingBroken(t, env, "api", "builder claude; switching to codex failed: exit status 1")

	b, err := env.runtime(t).Store.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.Round != 2 {
		t.Fatalf("Round = %d, want 2: the resend names b.Round-1", b.Round)
	}

	entriesBefore, err := env.runtime(t).Store.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	before := startedSpecs(env)

	resendBytes, resendCT := makeRoundForm(t, 1, "# Plan 1", nil)
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", resendBytes, resendCT)
	requireStatus(t, resp, body, http.StatusOK)

	got := decodeView(t, body)
	if got.RoundState != remote.RoundBroken {
		t.Errorf("round_state = %q, want %q", got.RoundState, remote.RoundBroken)
	}
	if got.Round != b.Round {
		t.Errorf("view round = %d, want the binding's own round %d", got.Round, b.Round)
	}
	if got.Halt != b.Halt {
		t.Errorf("view halt = %q, want the binding's own reason %q", got.Halt, b.Halt)
	}

	entriesAfter, err := env.runtime(t).Store.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog after: %v", err)
	}
	if len(entriesAfter) != len(entriesBefore) {
		t.Errorf("log entries = %d, want %d (an identical retry appends nothing)", len(entriesAfter), len(entriesBefore))
	}
	if after := startedSpecs(env); len(after) != len(before) {
		t.Errorf("runner starts = %d, want %d (an identical retry starts no builder)", len(after), len(before))
	}
}

// TestWriteSendErrorBrokenIs409Not500 pins the mapping itself, which the
// open-round checks now make hard to reach over HTTP: a broken binding is
// refused before Send in every request shape they cover, but Send re-checks the
// state under its own lock, and a binding that breaks between the check and the
// send lands here.
//
// Send refuses a broken binding with a plain error, so the refusal fell to the
// 500 arm -- which asks whether the binding is halted or carries a reason, and a
// broken binding answers no to both. A broken round is a state a human resolves
// (rebind, stop, unbind); answering 500 says the server is broken, which is the
// one thing it is not.
func TestWriteSendErrorBrokenIs409Not500(t *testing.T) {
	for _, tc := range []struct {
		name string
		halt string
	}{
		{name: "no reason recorded", halt: ""},
		{name: "reason recorded", halt: "builder claude; switching to codex failed: exit status 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupTestEnv(t)
			rt := env.runtime(t)

			createAndAbsorb(t, env, "api")
			setServedBindingBroken(t, env, "api", tc.halt)

			b, err := rt.Store.Load("api")
			if err != nil {
				t.Fatalf("load: %v", err)
			}

			w := httptest.NewRecorder()
			writeSendError(w, rt, "api", b, fmt.Errorf("binding %q is broken; rebind before sending", "api"))

			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusConflict)
			}
			var errBody remote.ErrorBody
			if err := json.Unmarshal(w.Body.Bytes(), &errBody); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if errBody.Code != remote.CodeRoundHalted {
				t.Errorf("error code = %q, want %q", errBody.Code, remote.CodeRoundHalted)
			}
			if errBody.Message == "" {
				t.Error("message is empty, want the refusal a human acts on")
			}
			// A binding that records its reason answers with it: that text is the
			// only account of the break, and the client's error is what carries it.
			if tc.halt != "" && errBody.Message != tc.halt {
				t.Errorf("message = %q, want the binding's own reason %q", errBody.Message, tc.halt)
			}
		})
	}
}
