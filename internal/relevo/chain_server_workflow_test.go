package relevo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// TestChainServerTakesWorkflow pins which --workflow values may be combined
// with --server: empty and shipped names ride the workflow wire path, so the
// client can post them; anything else still needs a proven workflow server.
func TestChainServerTakesWorkflow(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "default", "direct-fix"} {
		if !ChainServerTakesWorkflow(name) {
			t.Errorf("ChainServerTakesWorkflow(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"custom", "custom.yaml", "/tmp/custom.yaml"} {
		if ChainServerTakesWorkflow(name) {
			t.Errorf("ChainServerTakesWorkflow(%q) = true, want false", name)
		}
	}
}

// TestServerChainShippedDirectFixNeedsWorkflowFeature pins the silent-default
// close: a direct-fix chain against a server without the workflow feature is
// refused naming the server, the workflow and the missing feature before
// anything exists, while a server with the feature proceeds on the workflow
// wire path.
func TestServerChainShippedDirectFixNeedsWorkflowFeature(t *testing.T) {
	t.Parallel()

	t.Run("old server refuses", func(t *testing.T) {
		t.Parallel()

		fr := chainServerFake()
		rt, fg, _ := chainServerRuntime(t, fr)
		opts := chainServerOpts(t)
		opts.Workflow = "direct-fix"

		_, err := ChainStart(context.Background(), rt, opts)
		if err == nil {
			t.Fatal("ChainStart = nil, want the workflow-feature refusal")
		}
		want := "server zen cannot run workflow direct-fix (missing the \"workflow\" feature)"
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err.Error(), want)
		}
		for _, c := range fr.calls {
			if strings.HasPrefix(c, "CreateChain") {
				t.Errorf("CreateChain called: %v, want it refused beforehand", fr.calls)
			}
		}
		assertNothingCreated(t, rt, fg, "shop")
	})

	t.Run("new server proceeds", func(t *testing.T) {
		t.Parallel()

		fr := chainServerFake()
		fr.whoAmIResp.Features = append(fr.whoAmIResp.Features, remote.FeatureWorkflow)
		rt, _, ft := chainServerRuntime(t, fr)
		ft.snapshotResp = remote.Snapshot{
			Heads: map[string]string{"refs/relevo/shop/out": seedRemoteBase},
			Body:  io.NopCloser(strings.NewReader("bundle-bytes")),
		}

		opts := chainServerOpts(t)
		opts.Workflow = "direct-fix"
		res, err := ChainStart(context.Background(), rt, opts)
		if err != nil {
			t.Fatalf("ChainStart: %v", err)
		}
		def, err := workflow.Parse(fr.createChainReq.Workflow)
		if err != nil {
			t.Fatalf("parse posted Workflow: %v", err)
		}
		if def.Name != "direct-fix" {
			t.Errorf("posted Workflow name = %q, want direct-fix, not the default", def.Name)
		}
		if res.Chain.Name != "shop" {
			t.Errorf("Chain.Name = %q, want shop", res.Chain.Name)
		}
	})
}

// TestServerChainMixedVersionRefusesDirectFix pins that a mixed-version server
// never quietly runs the default: a server with the workflow feature but
// older than the fix-direct seed refuses the posted definition in its
// preflight, and that refusal reaches the client as a clear error with
// nothing created.
func TestServerChainMixedVersionRefusesDirectFix(t *testing.T) {
	t.Parallel()

	direct, ok := workflow.Shipped("direct-fix")
	if !ok {
		t.Fatal("Shipped(direct-fix): want true")
	}

	t.Run("new server preflights direct-fix", func(t *testing.T) {
		t.Parallel()

		rt, _, _ := servedChainRuntime(t)
		raw, err := json.Marshal(direct)
		if err != nil {
			t.Fatalf("marshal direct-fix: %v", err)
		}
		req := servedChainRequest("shop")
		req.Workflow = raw
		plan, err := ServedChainPreflight(rt, req)
		if err != nil {
			t.Fatalf("ServedChainPreflight(direct-fix): %v, want the new server to take it", err)
		}
		if plan.def.Name != "direct-fix" {
			t.Errorf("plan definition = %q, want direct-fix, not the default", plan.def.Name)
		}
	})

	t.Run("old seeds refuse the fix-direct step", func(t *testing.T) {
		t.Parallel()

		rt, _, _ := servedChainRuntime(t)
		oldSeeds := make([]string, 0, len(workflow.ShippedSeeds()))
		for _, name := range workflow.ShippedSeeds() {
			if name != "fix-direct" {
				oldSeeds = append(oldSeeds, name)
			}
		}
		env := workflow.Env{
			Actors: rt.RoleRegistry().WorkflowActors(),
			Given:  &workflow.Given{Plans: true},
			Seeds:  oldSeeds,
		}
		problems := workflow.Validate(direct, env)
		joined := make([]string, 0, len(problems))
		for _, p := range problems {
			joined = append(joined, p.String())
		}
		if !strings.Contains(strings.Join(joined, "\n"), "shipped:fix-direct") {
			t.Fatalf("problems = %v, want the old server to refuse the fix-direct seed", joined)
		}
	})

	t.Run("preflight refusal reaches the client", func(t *testing.T) {
		t.Parallel()

		fr := chainServerFake()
		fr.whoAmIResp.Features = append(fr.whoAmIResp.Features, remote.FeatureWorkflow)
		fr.createChainErr = &client.HTTPError{
			Status: http.StatusBadRequest,
			Body:   remote.ErrorBody{Code: remote.CodeInvalid, Message: "step fix-direct: rule 2: seed shipped:fix-direct is not a shipped seed"},
		}
		rt, fg, _ := chainServerRuntime(t, fr)
		opts := chainServerOpts(t)
		opts.Workflow = "direct-fix"

		_, err := ChainStart(context.Background(), rt, opts)
		if err == nil {
			t.Fatal("ChainStart = nil, want the old server's preflight refusal")
		}
		if !strings.Contains(err.Error(), "fix-direct") {
			t.Errorf("err = %q, want it to name the refused fix-direct seed", err.Error())
		}
		called := false
		for _, c := range fr.calls {
			if strings.HasPrefix(c, "CreateChain") {
				called = true
			}
		}
		if !called {
			t.Errorf("CreateChain not called: %v, want the posted definition refused", fr.calls)
		}
		if len(fg.createBranchCalls) != 0 {
			t.Errorf("a refused create must cut no local branch: %v", fg.createBranchCalls)
		}
		assertNothingCreated(t, rt, fg, "shop")
	})
}
