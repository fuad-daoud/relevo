package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// plannerCandidateSet is the haiku candidate a served planner binding runs. It
// lists only roles harness knows -- candidate.Load drops a candidate whose role
// is unknown -- while the planner row in plannerRegistry assigns it, which is
// the only place file-mode roles.json assigns candidates (#374).
func plannerCandidateSet(t *testing.T) *candidate.Set {
	t.Helper()
	body := `[{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder"]}]`
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := candidate.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// plannerRegistry is the server's own roles.json for the force test: a builder
// row and a planner reader row whose claude definition agent is the
// mastermind's architect, which is what turns the seed cap on.
func plannerRegistry(t *testing.T, set *candidate.Set) *roles.Registry {
	t.Helper()
	reader := "reader"
	reg, err := roles.Build(&roles.File{Rows: map[string]roles.Row{
		"builder": {Candidates: []string{"claude/anthropic/haiku"}},
		"planner": {
			Shape:       &reader,
			Candidates:  []string{"claude/anthropic/haiku"},
			Definitions: map[string]roles.DefRow{"claude": {Agent: "architect"}},
		},
	}}, set, policy.Policy{})
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}
	return reg
}

// TestStartRoundForceSendsAnOverCapPlannerSeed pins #702's server half: the
// round request's force field is applied to the same seed-cap check a local
// send uses, so an over-cap planner seed is admitted (201) with force and
// refused (not 201) without it.
func TestStartRoundForceSendsAnOverCapPlannerSeed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		force bool
	}{
		{"forced", true},
		{"unforced", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := plannerCandidateSet(t)
			env := setupTestEnv(t, func(cfg *Config) {
				cfg.Candidates = set
				cfg.Registry = plannerRegistry(t, set)
			})

			createBody, _ := json.Marshal(remote.CreateBindingRequest{
				Name:       "seed",
				RepoID:     env.repoID,
				BaseCommit: env.headSHA,
				Role:       "planner",
			})
			resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")
			requireStatus(t, resp, body, http.StatusCreated)

			outRef := "refs/relevo/seed/out"
			if err := env.gitClient.UpdateRef(context.Background(), env.clientDir, outRef, env.headSHA, ""); err != nil {
				t.Fatalf("updateRef out: %v", err)
			}
			plan := strings.Repeat("x", 4097)
			formBytes, ct := makeRoundFormForce(t, 1, plan, snapshotRef(t, env, env.clientDir, outRef), tc.force)
			resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/seed/rounds", formBytes, ct)

			if tc.force {
				requireCreated(t, resp, body, "forced planner seed")
				view := decodeView(t, body)
				if view.Round != 1 {
					t.Fatalf("view.Round = %d, want 1", view.Round)
				}
				if view.RoundState == remote.RoundIdle {
					t.Fatalf("RoundState = %q, want a non-idle round", view.RoundState)
				}
				return
			}
			if resp.StatusCode == http.StatusCreated {
				t.Fatalf("unforced over-cap send started a round: status = %d; body: %s", resp.StatusCode, string(body))
			}
			if !strings.Contains(string(body), "4096") {
				t.Fatalf("unforced refusal body = %q, want it to name the 4096-byte cap", string(body))
			}
		})
	}
}
