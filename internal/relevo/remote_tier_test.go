package relevo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// tierUnsupportedWire is the 422 invalid answer a server's launch render
// produces for a harness-tier refusal.
func tierUnsupportedWire() *client.HTTPError {
	return &client.HTTPError{
		Status: 422,
		Body: remote.ErrorBody{
			Code:    remote.CodeInvalid,
			Message: "tier not supported by harness: claude cannot run a builder at tier edit: headless acceptEdits denies go, make and git commit, so the builder could not run the gate or commit; use --tier yolo (--dangerously-skip-permissions) or --tier harness (claude's settings.json decides)",
		},
	}
}

// TestAddRemoteTierUnsupportedWraps pins that a 422 invalid carrying the
// harness-tier refusal maps back to ErrTierUnsupported client-side, matching
// the local refusal's wording.
func TestAddRemoteTierUnsupportedWraps(t *testing.T) {
	// No t.Parallel: addRemoteMasterMind sets env, like TestAddRemoteServerConflict.
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{createBindingErr: tierUnsupportedWire()}
	rt := Runtime{
		Store:       st,
		MasterMinds: addRemoteMasterMind(t),
		Git:         fg,
		Remote:      fr,
		Now:         time.Now,
	}

	_, err := Add(ctx, rt, AddOptions{Name: "api", Server: "zen", Repo: "/fake/repo"})
	if !errors.Is(err, harness.ErrTierUnsupported) {
		t.Fatalf("err = %v, want errors.Is(err, harness.ErrTierUnsupported)", err)
	}
	if n := strings.Count(err.Error(), harness.ErrTierUnsupported.Error()); n != 1 {
		t.Errorf("err = %q names the refusal %d times, want once", err, n)
	}
	if len(fg.createBranchCalls) != 0 {
		t.Fatalf("createBranch called %d times, want 0", len(fg.createBranchCalls))
	}
}

// TestChainStartRemoteTierUnsupportedWraps pins the same mapping on the chain
// member's create path.
func TestChainStartRemoteTierUnsupportedWraps(t *testing.T) {
	t.Parallel()

	fr := chainRemoteFake()
	fr.createBindingErr = tierUnsupportedWire()
	rt, fg, _ := chainRemoteRuntime(t, fr)

	_, err := ChainStart(context.Background(), rt, ChainOptions{
		Name: "shop", Plans: []string{writePlan(t, "p")}, Feature: "auth", MasterMindID: testMasterMindName,
	})
	if !errors.Is(err, harness.ErrTierUnsupported) {
		t.Fatalf("err = %v, want errors.Is(err, harness.ErrTierUnsupported)", err)
	}
	if len(fg.createBranchCalls) != 0 {
		t.Fatalf("createBranch called %d times, want 0", len(fg.createBranchCalls))
	}
	assertNothingCreated(t, rt, fg, "shop")
}
