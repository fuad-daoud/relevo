package relevo

import (
	"context"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// chainSubdirFake is chainRemoteFake with the feature the prefix needs, so a
// placed builder can send it.
func chainSubdirFake() *fakeRemote {
	fr := chainRemoteFake()
	fr.whoAmIResp.Features = append(fr.whoAmIResp.Features, remote.FeatureSubdir)
	return fr
}

// TestChainMembersCarryTheSubdirectory pins the local start: a chain started
// below the root carries the prefix on every member -- builder and readers
// share the cut worktree, so each one's round runs in its subdirectory.
// Leaving the members at the tree root is the mutation this pins against.
func TestChainMembersCarryTheSubdirectory(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	fg.showPrefixResult = "src/api/"
	res := startedChain(t, rt, ChainOptions{})

	for _, m := range res.Members {
		if m.Subdir != "src/api" {
			t.Errorf("member %s Subdir = %q, want src/api", m.Name, m.Subdir)
		}
	}
}

// TestAPlacedChainBuilderCarriesTheSubdirectoryAndItsLocalReadersDoNot pins
// the placed start: the create the server receives carries the prefix, while
// every local reader -- and the mirror row of the builder itself -- carries
// none: their CWD is already the caller's checkout, and putting the prefix on
// it would double-join.
func TestAPlacedChainBuilderCarriesTheSubdirectoryAndItsLocalReadersDoNot(t *testing.T) {
	t.Parallel()

	fr := chainSubdirFake()
	rt, fg, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	fg.showPrefixResult = "src/api/"
	res := startedChain(t, rt, ChainOptions{})

	if got := fr.createBindingReq.Subdir; got != "src/api" {
		t.Errorf("create Subdir = %q, want src/api", got)
	}
	builder := memberByName(t, res.Members, "shop")
	if builder.Subdir != "" {
		t.Errorf("mirror builder Subdir = %q, want none", builder.Subdir)
	}
	for _, name := range []string{"shop-rev", "shop-plan"} {
		m := memberByName(t, res.Members, name)
		if m.Subdir != "" {
			t.Errorf("%s Subdir = %q, want none", name, m.Subdir)
		}
	}
}

// TestAServedChainMemberCarriesTheSubdirectory pins the served create: the
// subdirectory the client sent is stored on every member, writer and reader
// alike, so every round the server runs starts inside the worktree's
// subdirectory.
func TestAServedChainMemberCarriesTheSubdirectory(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedChainRuntime(t)
	req := servedChainRequest("shop")
	req.Subdir = "src/api"
	res := servedChainStart(t, rt, req)

	for _, m := range res.Members {
		if m.Subdir != "src/api" {
			t.Errorf("member %s Subdir = %q, want src/api", m.Name, m.Subdir)
		}
	}
}

// TestResumedChainMembersInheritTheBuildersSubdirectory pins the resume: a
// member the resume brings in is built beside the chain's builder, so it
// carries the builder's own subdirectory.
func TestResumedChainMembersInheritTheBuildersSubdirectory(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowBranchWorkflow)
	if _, err := rt.Store.Load("shop-researcher"); err == nil {
		t.Fatal("premise: the researcher member exists before the scan branch is on")
	}
	b, err := rt.Store.Load("shop")
	if err != nil {
		t.Fatalf("Load builder: %v", err)
	}
	b.Subdir = "src/api"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if _, err := ChainStop(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainStop: %v", err)
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", Params: map[string]string{"scan": "true"}}); err != nil {
		t.Fatalf("ChainResume --param scan=true: %v", err)
	}

	added, err := rt.Store.Load("shop-researcher")
	if err != nil {
		t.Fatalf("Load shop-researcher: %v", err)
	}
	if added.Subdir != "src/api" {
		t.Errorf("added member Subdir = %q, want the builder's src/api", added.Subdir)
	}
}

// TestAForkChildInheritsItsParentsSubdirectory pins the fork: a child cut
// from the parent's root checkout carries the parent builder member's own
// subdirectory, so its members' rounds land beside the parent's.
func TestAForkChildInheritsItsParentsSubdirectory(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	childFile := writeWorkflowFile(t, childWorkflowBody)
	parentFile := writeWorkflowFile(t, forkFixedWorkflow(childFile))
	fg.refSHA = map[string]string{"relevo/shop": "parent-tip-commit-789"}
	// The parent itself was started below the root, so its builder member
	// carries the prefix the fork has to hand on.
	fg.showPrefixResult = "src/api/"

	startedChain(t, rt, ChainOptions{Name: "shop", Workflow: parentFile})

	builder, err := rt.Store.Load("shop")
	if err != nil {
		t.Fatalf("Load parent builder: %v", err)
	}
	if builder.Subdir != "src/api" {
		t.Errorf("parent builder Subdir = %q, want src/api", builder.Subdir)
	}
	child, err := rt.Store.Load("shop.1")
	if err != nil {
		t.Fatalf("Load child member: %v", err)
	}
	if child.Subdir != "src/api" {
		t.Errorf("child member Subdir = %q, want the parent builder's src/api", child.Subdir)
	}
}
