package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// closeChainRoundWith closes a chain member's round n with the given report
// body: the report and its marker are written, the fake builder is marked
// exited, and one tick closes the round through the chain's own close path.
func closeChainRoundWith(t *testing.T, env *testEnv, rt relevo.Runtime, name string, n int, report string) {
	t.Helper()
	if err := os.WriteFile(rt.Store.ReportPath(name, n), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.DonePath(name, n), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	env.runner.setAlive(false)
	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

// TestManualRoundToAHaltedChainMemberIsAcceptedThenReviewed runs the halted
// script over the wire: a halted chain's member takes a manual round through
// the round start, and the resume reviews that round instead of re-running
// the halted step. A refusal at the round start would be the round_open or
// round_halted seam; a re-run at the resume would be the resume missing the
// newer closed round.
func TestManualRoundToAHaltedChainMemberIsAcceptedThenReviewed(t *testing.T) {
	env := setupTestEnv(t)

	resp, body := createChain(t, env, "shop")
	requireStatus(t, resp, body, http.StatusCreated)

	rt := env.runtime(t)
	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if builder, err := rt.Store.Load("shop"); err != nil {
		t.Fatal(err)
	} else if builder.RoundStartedAt.IsZero() {
		t.Fatal("plan 1 did not start: RoundStartedAt is zero")
	}

	// Halt the chain through the builder's own close.
	closeChainRoundWith(t, env, rt, "shop", 1,
		"stopped before finishing.\n\n```relevo\nstatus: halted\nhalted_at: \"stuck\"\nchanged_paths: []\ncommands_run: []\nnot_done: []\n```\n")
	if c, err := rt.Store.Chain("shop"); err != nil {
		t.Fatal(err)
	} else if c.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want halted", c.Status)
	}
	if builder, err := rt.Store.Load("shop"); err != nil {
		t.Fatal(err)
	} else if builder.Round != 2 {
		t.Fatalf("builder round = %d, want 2 after the halted close", builder.Round)
	}

	// A manual round to the halted member: the round start must take it.
	outRef := "refs/relevo/shop/out"
	if err := env.gitClient.UpdateRef(context.Background(), env.clientDir, outRef, env.headSHA, ""); err != nil {
		t.Fatalf("updateRef out: %v", err)
	}
	form, ct := makeRoundForm(t, 2, "# Manual\ncarry on", snapshotRef(t, env, env.clientDir, outRef))
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/shop/rounds", form, ct)
	requireStatus(t, resp, body, http.StatusCreated)
	if view := decodeView(t, body); view.Round != 2 {
		t.Fatalf("round view = %+v, want the manual round 2 taken", view)
	}

	// Close the manual round done: the chain stays halted.
	closeChainRoundWith(t, env, rt, "shop", 2, "# Manual\nDone.\n\n```relevo\nstatus: done\n```\n")
	if c, err := rt.Store.Chain("shop"); err != nil {
		t.Fatal(err)
	} else if c.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want it still halted after the manual round", c.Status)
	}
	builderBefore, err := rt.Store.Load("shop")
	if err != nil {
		t.Fatal(err)
	}

	// Resume over the wire: the manual round is reviewed.
	resumeBody, _ := json.Marshal(remote.ChainResumeRequest{})
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/chains/shop/resume", resumeBody, "application/json")
	requireStatus(t, resp, body, http.StatusOK)
	view := decodeChainView(t, body)
	if view.Status != string(chain.StatusRunning) || view.Step != string(chain.StepReviewing) {
		t.Errorf("chain view = status %q step %q, want running/reviewing: the resume reviews the manual round",
			view.Status, view.Step)
	}
	if view.AwaitingMember != chain.MemberReviewer || view.AwaitingRound != 1 {
		t.Errorf("awaiting = (%s, %d), want the reviewer's round (reviewer, 1)",
			view.AwaitingMember, view.AwaitingRound)
	}

	// The reviewer is seeded for the manual round, and the builder is not
	// re-sent: its round is the manual close's advance, still without a
	// prompt of its own.
	rev, err := rt.Store.Load("shop-rev")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := rt.Store.ReadLog("shop-rev")
	if err != nil {
		t.Fatal(err)
	}
	seeded := false
	for _, e := range entries {
		if e.Direction == store.DirToBuilder && e.Kind == store.KindPrompt && e.Round == rev.Round {
			seeded = true
		}
	}
	if !seeded {
		t.Errorf("reviewer log = %+v, want a prompt entry for round %d", entries, rev.Round)
	}
	builder, err := rt.Store.Load("shop")
	if err != nil {
		t.Fatal(err)
	}
	if builder.Round != builderBefore.Round {
		t.Errorf("builder round = %d, want %d kept: the resume reviews, it does not re-send",
			builder.Round, builderBefore.Round)
	}
}
