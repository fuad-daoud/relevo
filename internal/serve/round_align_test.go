package serve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// alignEnv is a server holding binding api at round 1, nothing started.
func alignEnv(t *testing.T) (*testEnv, []byte) {
	t.Helper()
	env := setupTestEnv(t)
	ctx := context.Background()
	createBody, err := json.Marshal(remote.CreateBindingRequest{
		Name: "api", RepoID: env.repoID, BaseCommit: env.headSHA, Role: "builder",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")
	requireStatus(t, resp, body, http.StatusCreated)
	outRef := "refs/relevo/api/out"
	if err := env.gitClient.UpdateRef(ctx, env.clientDir, outRef, env.headSHA, ""); err != nil {
		t.Fatal(err)
	}
	snap, err := env.transport.Snapshot(ctx, env.clientDir, []string{outRef}, "")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := io.ReadAll(snap.Body)
	_ = snap.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return env, bundle
}

func alignRoundOf(t *testing.T, env *testEnv) int {
	t.Helper()
	b, err := env.runtime(t).Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	return b.Round
}

func TestAlignRoundLetsAStartSkipAhead(t *testing.T) {
	env, bundle := alignEnv(t)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds/3/align", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	if got := alignRoundOf(t, env); got != 3 {
		t.Fatalf("round = %d, want 3", got)
	}
	form, ct := makeRoundForm(t, 3, "# Plan 3", bundle)
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", form, ct)
	requireStatus(t, resp, body, http.StatusCreated)
	var view remote.BindingView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view.Round != 3 {
		t.Errorf("started round = %d, want 3", view.Round)
	}
}

func TestAlignRoundNeverMovesDown(t *testing.T) {
	env, _ := alignEnv(t)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds/3/align", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds/2/align", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	if got := alignRoundOf(t, env); got != 3 {
		t.Errorf("round = %d, want 3", got)
	}
}

func TestAlignRoundRefusesPastAnOpenRound(t *testing.T) {
	env, bundle := alignEnv(t)
	form, ct := makeRoundForm(t, 1, "# Plan 1", bundle)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", form, ct)
	requireStatus(t, resp, body, http.StatusCreated)

	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds/3/align", nil, "")
	requireStatus(t, resp, body, http.StatusConflict)
	if got := alignRoundOf(t, env); got != 1 {
		t.Errorf("round = %d, want 1", got)
	}
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds/1/align", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
}

// The closed round would otherwise keep answering for the binding over the
// round the caller starts next.
func TestAlignRoundSettlesAnUnackedClose(t *testing.T) {
	env, bundle := alignEnv(t)
	ctx := context.Background()
	form, ct := makeRoundForm(t, 1, "# Plan 1", bundle)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", form, ct)
	requireStatus(t, resp, body, http.StatusCreated)
	rt := env.runtime(t)
	report := "# Report 1\nDone.\n\n```relevo\nstatus: done\n```\n"
	if err := os.WriteFile(rt.Store.ReportPath("api", 1), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.DonePath("api", 1), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	env.runner.setAlive(false)
	if err := env.srv.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, err := rt.Store.PendingForMasterMind("api"); err != nil || !found {
		t.Fatalf("pending before align: found=%v err=%v", found, err)
	}

	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds/5/align", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	var view remote.BindingView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view.RoundState != remote.RoundIdle {
		t.Errorf("round state = %v, want idle: the close is settled", view.RoundState)
	}
	if _, found, err := rt.Store.PendingForMasterMind("api"); err != nil || found {
		t.Errorf("pending after align: found=%v err=%v, want the close settled", found, err)
	}
}
