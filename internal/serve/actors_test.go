package serve

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// testBuilderToken is the one candidate every setupTestEnv server is configured
// with, and so the pick an enrolled builder must resolve to.
const testBuilderToken = "claude/anthropic/haiku"

// getActor signs and sends GET target, decoding a 200 as an actor view. The
// body is returned even on a refusal, so a test can name the code it got.
func getActor(t *testing.T, env *testEnv, target string) (*http.Response, remote.ActorView, []byte) {
	t.Helper()
	resp, body := doSigned(t, env.ts, env.kp, "GET", target, nil, "")
	var view remote.ActorView
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &view); err != nil {
			t.Fatalf("unmarshal actor view: %v; body: %s", err, string(body))
		}
	}
	return resp, view, body
}

// TestGetActorBuilder: the enrolled builder's own request answers the pick and
// the ranked list a create would use.
func TestGetActorBuilder(t *testing.T) {
	env := setupTestEnv(t)

	resp, view, body := getActor(t, env, "/v1/actors/builder")
	requireStatus(t, resp, body, http.StatusOK)
	t.Logf("builder response: %s", string(body))

	if view.Actor != "builder" {
		t.Errorf("Actor = %q, want builder", view.Actor)
	}
	if view.Shape != store.ShapeWriter {
		t.Errorf("Shape = %q, want %q", view.Shape, store.ShapeWriter)
	}
	if !view.Accepted {
		t.Errorf("Accepted = false, want true")
	}
	if view.Pick != testBuilderToken {
		t.Errorf("Pick = %q, want %q", view.Pick, testBuilderToken)
	}
	if view.Reason != "" {
		t.Errorf("Reason = %q, want empty", view.Reason)
	}
	if len(view.Candidates) != 1 {
		t.Fatalf("Candidates = %+v, want the one ranked candidate", view.Candidates)
	}
	c := view.Candidates[0]
	if c.Token != testBuilderToken || c.Kind != "claude" {
		t.Errorf("Candidates[0] = %+v, want token %q kind claude", c, testBuilderToken)
	}
	if c.Name == "" {
		t.Errorf("Candidates[0].Name = %q, want the configured candidate's name", c.Name)
	}
	if c.Gated {
		t.Errorf("Candidates[0].Gated = true, want false on a free ledger")
	}

	picked := 0
	for _, entry := range view.Candidates {
		if !entry.Pick {
			continue
		}
		picked++
		if entry.Token != view.Pick {
			t.Errorf("candidate marked pick = %q, want the top-level pick %q", entry.Token, view.Pick)
		}
	}
	if picked != 1 {
		t.Errorf("candidates marked pick = %d, want exactly 1", picked)
	}
}

// TestGetActorUnknown: an actor no role defines is 404 not_found, the code
// every other missing-thing route answers.
func TestGetActorUnknown(t *testing.T) {
	env := setupTestEnv(t)

	resp, _, body := getActor(t, env, "/v1/actors/ghost")
	requireStatus(t, resp, body, http.StatusNotFound)

	var errBody remote.ErrorBody
	if err := json.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("unmarshal error body: %v; body: %s", err, string(body))
	}
	if errBody.Code != remote.CodeNotFound {
		t.Errorf("error code = %q, want %q", errBody.Code, remote.CodeNotFound)
	}
}

// TestGetActorPin: a pin the actor serves is accepted as itself; a pin it does
// not know is refused with the resolver's own text, never swapped for another
// candidate.
func TestGetActorPin(t *testing.T) {
	env := setupTestEnv(t)

	resp, view, body := getActor(t, env, "/v1/actors/builder?candidate="+testBuilderToken)
	requireStatus(t, resp, body, http.StatusOK)
	if !view.Accepted || view.Pick != testBuilderToken {
		t.Errorf("accepted/pick = %v/%q, want true/%q", view.Accepted, view.Pick, testBuilderToken)
	}
	if view.Reason != "" {
		t.Errorf("Reason = %q, want empty for an accepted pin", view.Reason)
	}

	resp, view, body = getActor(t, env, "/v1/actors/builder?candidate=claude/anthropic/nope")
	requireStatus(t, resp, body, http.StatusOK)
	t.Logf("refusal response: %s", string(body))
	if view.Accepted {
		t.Errorf("Accepted = true for a refused pin, want false")
	}
	if view.Pick != "" {
		t.Errorf("Pick = %q for a refused pin, want empty", view.Pick)
	}
	if view.Reason == "" {
		t.Errorf("Reason is empty for a refused pin, want the resolver's refusal")
	}
	for _, entry := range view.Candidates {
		if entry.Pick {
			t.Errorf("refused pin marks candidate %q as pick, want none", entry.Token)
		}
	}
}

// TestGetActorGated: a ledger gate on the only ranked candidate shows on its
// view, and the pick with no pin then refuses with a reason instead of serving
// a gated candidate.
func TestGetActorGated(t *testing.T) {
	env := setupTestEnv(t)

	rt := env.runtime(t)
	if _, err := availability.Unavailable(relevo.AvailabilityDeps(rt), testBuilderToken, time.Now().Add(time.Hour), "quota"); err != nil {
		t.Fatalf("record gate: %v", err)
	}

	resp, view, body := getActor(t, env, "/v1/actors/builder")
	requireStatus(t, resp, body, http.StatusOK)

	if len(view.Candidates) != 1 {
		t.Fatalf("Candidates = %+v, want the one ranked candidate", view.Candidates)
	}
	if !view.Candidates[0].Gated {
		t.Errorf("Candidates[0].Gated = false, want true for a gated token")
	}
	if view.Accepted {
		t.Errorf("Accepted = true with every ranked candidate gated, want false")
	}
	if view.Reason == "" {
		t.Errorf("Reason is empty with every ranked candidate gated, want a reason")
	}
}

// TestGetActorReader: a reader actor answers its own shape, and the builder's
// pick never leaks into it.
func TestGetActorReader(t *testing.T) {
	env := setupTestEnv(t)

	resp, view, body := getActor(t, env, "/v1/actors/reviewer")
	requireStatus(t, resp, body, http.StatusOK)

	if view.Actor != "reviewer" {
		t.Errorf("Actor = %q, want reviewer", view.Actor)
	}
	if view.Shape != store.ShapeReader {
		t.Errorf("Shape = %q, want %q", view.Shape, store.ShapeReader)
	}
	if view.Accepted || view.Pick != "" {
		t.Errorf("accepted/pick = %v/%q for a reader, want false/empty", view.Accepted, view.Pick)
	}
	if view.Reason == "" {
		t.Errorf("Reason is empty for an actor nothing serves, want a reason")
	}
}

// TestCreateRefusalLeavesNoBareRepo: a create refused because of its actor or
// its candidate answers 4xx and leaves nothing behind -- no bare repo under the
// owner's repo root, which is what the refused-create path promises. A refusal
// that only the candidate pick produces is the arm the pick's position decides,
// so both are pinned here.
func TestCreateRefusalLeavesNoBareRepo(t *testing.T) {
	env := setupTestEnv(t)

	ownerDir, ok := env.id.Dir()
	if !ok {
		t.Fatal("client id has no owner dir")
	}
	bare := filepath.Join(env.srv.cfg.Root, "repos", ownerDir, env.repoID+".git")

	for _, tc := range []struct{ name, role, candidate string }{
		{name: "ghost-actor", role: "ghost"},
		{name: "ghost-candidate", role: "builder", candidate: "claude/anthropic/nope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(remote.CreateBindingRequest{
				Name:       tc.name,
				RepoID:     env.repoID,
				BaseCommit: env.headSHA,
				Role:       tc.role,
				Candidate:  tc.candidate,
			})
			if err != nil {
				t.Fatal(err)
			}
			resp, respBody := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", body, "application/json")
			if resp.StatusCode < 400 || resp.StatusCode >= 500 {
				t.Fatalf("status = %d, want 4xx; body: %s", resp.StatusCode, string(respBody))
			}
			if _, err := os.Stat(bare); !os.IsNotExist(err) {
				t.Fatalf("bare repo %s exists after a refused create (stat err = %v)", bare, err)
			}
		})
	}
}
