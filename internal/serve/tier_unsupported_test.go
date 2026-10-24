package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestServedCreateRefusesUnsupportedTier pins the create-time launch render:
// a claude builder at tier edit cannot run the gate, so the create is refused
// 422 invalid with the harness message before InitBare runs -- no bare repo
// and no binding row.
func TestServedCreateRefusesUnsupportedTier(t *testing.T) {
	env := setupTestEnv(t)
	rt := env.runtime(t)

	req := remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
		Role:       "builder",
		Tier:       "edit",
	}
	rec := httptest.NewRecorder()
	_, release, ok := env.srv.buildServedBinding(rec, context.Background(), rt, env.id, req)
	if ok {
		t.Fatal("buildServedBinding = ok, want the tier refusal")
	}
	if release != nil {
		release()
		t.Fatal("refused create holds a bare pin, want none")
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}
	var errBody remote.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if errBody.Code != remote.CodeInvalid {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeInvalid)
	}
	if !strings.Contains(errBody.Message, harness.ErrTierUnsupported.Error()) {
		t.Fatalf("message = %q, want it to carry %q", errBody.Message, harness.ErrTierUnsupported.Error())
	}

	root, err := env.srv.repoRoot(env.id)
	if err != nil {
		t.Fatalf("repoRoot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, env.repoID+".git")); !os.IsNotExist(err) {
		t.Fatalf("bare repo stat = %v, want not-exist after the refusal", err)
	}
	if _, err := rt.Store.Load("api"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("binding load = %v, want not-found after the refusal", err)
	}
}

// TestRoundStartUnsupportedTierIs422 pins the send-time mapping: a per-round
// --tier override the harness cannot run is refused 422 invalid, not 500,
// before a process starts.
func TestRoundStartUnsupportedTierIs422(t *testing.T) {
	env := setupTestEnv(t)

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundFormWithTier(t, 1, "# Round 1 Plan\nDo stuff", "edit", bundleBytes)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, body, http.StatusUnprocessableEntity)

	var errBody remote.ErrorBody
	_ = json.Unmarshal(body, &errBody)
	if errBody.Code != remote.CodeInvalid {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeInvalid)
	}
	if !strings.Contains(errBody.Message, harness.ErrTierUnsupported.Error()) {
		t.Fatalf("message = %q, want it to carry %q", errBody.Message, harness.ErrTierUnsupported.Error())
	}

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if relevo.RoundStateOf(b, entries) != remote.RoundIdle {
		t.Fatalf("round state = %v, want idle", relevo.RoundStateOf(b, entries))
	}
	if n := len(startedSpecs(env)); n != 0 {
		t.Fatalf("runner specs count = %d, want 0", n)
	}
}
