package relevo

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// The `bind --branch` fetch fallback: when the named branch is on neither the
// local repo nor the origin cache, bind fetches exactly that one ref and
// adopts it if it arrives, rather than refusing outright. Both refusals that
// remain -- a fetch that failed, and a fetch that left the branch absent --
// are refusals (ErrRefused), not internal errors, so the CLI renders
// `refused` and exits 2 instead of reporting a crash for a caller mistake.

// TestAddBranchFetchesMissingBranch pins case 3 on the local path: one
// bounded fetch of exactly the named branch, then the ordinary cache-hit path
// adopts it -- tracking branch, checkout, ExistingBranch.
func TestAddBranchFetchesMissingBranch(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{
		branchExists: false,
		refSHA: map[string]string{
			"refs/heads/feature/x": "o1",
		},
		fetchPopulates: map[string]string{"origin/feature/x": "o1"},
	}
	rt := newTestRuntime(t, fg)
	repo := addRepo(t)

	got, err := Add(context.Background(), rt, AddOptions{
		Name: "x", Branch: "feature/x", Candidate: testAgyRef,
		MasterMindID: testMasterMindName, Repo: repo,
	})
	if err != nil {
		t.Fatalf("Add --branch on a fetchable branch: %v", err)
	}

	want := []fetchCall{{Dir: repo, Remote: "origin", Ref: "feature/x"}}
	if !reflect.DeepEqual(fg.fetchCalls, want) {
		t.Errorf("fetchCalls = %+v, want exactly %+v", fg.fetchCalls, want)
	}
	wantTracking := []createTrackingBranchCall{{Dir: repo, Branch: "feature/x", Upstream: "origin/feature/x"}}
	if !reflect.DeepEqual(fg.createTrackingBranchCalls, wantTracking) {
		t.Errorf("createTrackingBranchCalls = %+v, want %+v", fg.createTrackingBranchCalls, wantTracking)
	}
	if len(fg.checkoutWorktreeCalls) != 1 || fg.checkoutWorktreeCalls[0].Branch != "feature/x" {
		t.Errorf("checkoutWorktreeCalls = %+v, want one checkout of feature/x", fg.checkoutWorktreeCalls)
	}
	if got.Base != "o1" || !got.Binding.ExistingBranch {
		t.Errorf("Base = %q ExistingBranch = %v, want o1 and true", got.Base, got.Binding.ExistingBranch)
	}
}

// TestAddBranchNoFetchWhenResolvable pins cases 1 and 2: a branch already
// local or already cached resolves without any fetch.
func TestAddBranchNoFetchWhenResolvable(t *testing.T) {
	t.Parallel()

	t.Run("branch is local", func(t *testing.T) {
		t.Parallel()
		fg := &fakeGit{branchExists: true, refSHA: map[string]string{"refs/heads/feature/x": "t"}}
		rt := newTestRuntime(t, fg)
		if _, err := Add(context.Background(), rt, AddOptions{
			Name: "x", Branch: "feature/x", Candidate: testAgyRef,
			MasterMindID: testMasterMindName, Repo: addRepo(t),
		}); err != nil {
			t.Fatalf("Add --branch: %v", err)
		}
		if len(fg.fetchCalls) != 0 {
			t.Errorf("a resolvable branch must reach no fetch: %+v", fg.fetchCalls)
		}
	})

	t.Run("branch is in the origin cache", func(t *testing.T) {
		t.Parallel()
		fg := &fakeGit{
			branchExists: false,
			refSHA: map[string]string{
				"refs/remotes/origin/feature/x": "o1",
				"refs/heads/feature/x":          "o1",
			},
		}
		rt := newTestRuntime(t, fg)
		if _, err := Add(context.Background(), rt, AddOptions{
			Name: "x", Branch: "feature/x", Candidate: testAgyRef,
			MasterMindID: testMasterMindName, Repo: addRepo(t),
		}); err != nil {
			t.Fatalf("Add --branch: %v", err)
		}
		if len(fg.fetchCalls) != 0 {
			t.Errorf("a cached branch must reach no fetch: %+v", fg.fetchCalls)
		}
	})
}

// TestAddBranchFetchFailureRefuses pins case 4 on the local path: the fetch
// fails, so bind refuses -- naming the manual recovery and keeping the
// existing wording -- reaches no git write, and is classed as a refusal.
func TestAddBranchFetchFailureRefuses(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{
		branchExists: false,
		refSHA:       map[string]string{},
		fetchErr:     errors.New("fatal: 'origin' does not appear to be a git repository"),
	}
	rt := newTestRuntime(t, fg)
	repo := addRepo(t)

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "x", Branch: "feature/x", Candidate: testAgyRef,
		MasterMindID: testMasterMindName, Repo: repo,
	})
	if err == nil {
		t.Fatal("Add --branch: got nil, want a refusal after a failed fetch")
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want ErrRefused so the CLI renders `refused`", err)
	}
	if !strings.Contains(err.Error(), "not found locally or on origin") {
		t.Errorf("err = %q, want the existing 'not found locally or on origin' wording kept", err)
	}
	if !strings.Contains(err.Error(), "git fetch origin feature/x") {
		t.Errorf("err = %q, want the manual recovery `git fetch origin feature/x` named", err)
	}
	if len(fg.addWorktreeCalls)+len(fg.checkoutWorktreeCalls)+len(fg.createTrackingBranchCalls)+
		len(fg.createBranchCalls)+len(fg.deleteBranchCalls) != 0 {
		t.Errorf("a failed fetch must reach no git write: add=%+v checkout=%+v tracking=%+v create=%+v delete=%+v",
			fg.addWorktreeCalls, fg.checkoutWorktreeCalls, fg.createTrackingBranchCalls,
			fg.createBranchCalls, fg.deleteBranchCalls)
	}
}

// TestAddBranchFetchedButStillAbsentRefuses pins case 5 on the local path:
// the fetch succeeded and the branch is still nowhere, so the current wording
// refuses, classed as a refusal and with no git write.
func TestAddBranchFetchedButStillAbsentRefuses(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{
		branchExists:   false,
		refSHA:         map[string]string{},
		fetchPopulates: map[string]string{}, // the fetch lands, brings no branch
	}
	rt := newTestRuntime(t, fg)
	repo := addRepo(t)

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "x", Branch: "feature/x", Candidate: testAgyRef,
		MasterMindID: testMasterMindName, Repo: repo,
	})
	if err == nil {
		t.Fatal("Add --branch: got nil, want a refusal when the fetch brings no branch")
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want ErrRefused so the CLI renders `refused`", err)
	}
	if !strings.Contains(err.Error(), "not found locally or on origin") {
		t.Errorf("err = %q, want the existing wording kept", err)
	}
	if len(fg.fetchCalls) != 1 {
		t.Errorf("exactly one bounded fetch expected, got %+v", fg.fetchCalls)
	}
	if len(fg.addWorktreeCalls)+len(fg.checkoutWorktreeCalls)+len(fg.createTrackingBranchCalls)+
		len(fg.createBranchCalls)+len(fg.deleteBranchCalls) != 0 {
		t.Errorf("an absent branch must reach no git write: add=%+v checkout=%+v tracking=%+v create=%+v delete=%+v",
			fg.addWorktreeCalls, fg.checkoutWorktreeCalls, fg.createTrackingBranchCalls,
			fg.createBranchCalls, fg.deleteBranchCalls)
	}
}

// TestAddRemoteBranchFetchesMissingBranch pins case 3 on the --server path:
// the fetch happens locally before any server traffic, the tip is sent as
// BaseCommit, and no branch is created.
func TestAddRemoteBranchFetchesMissingBranch(t *testing.T) {
	ctx := context.Background()

	st := store.New(t.TempDir())
	fg := &fakeGit{
		branchExists:   false,
		refSHA:         map[string]string{"refs/heads/feature/x": "tip"},
		rootCommitSHA:  "2222222222222222222222222222222222222222",
		fetchPopulates: map[string]string{"origin/feature/x": "tip"},
	}
	fr := &fakeRemote{createBindingResp: remote.BindingView{Name: "x"}}
	rt := Runtime{Store: st, Git: fg, Remote: fr, Now: time.Now, MasterMinds: addRemoteMasterMind(t)}

	got, err := Add(ctx, rt, AddOptions{
		Name: "x", Branch: "feature/x", Server: "zen", Repo: "/fake/repo",
	})
	if err != nil {
		t.Fatalf("Add --server --branch on a fetchable branch: %v", err)
	}

	want := []fetchCall{{Dir: "/fake/repo", Remote: "origin", Ref: "feature/x"}}
	if !reflect.DeepEqual(fg.fetchCalls, want) {
		t.Errorf("fetchCalls = %+v, want exactly %+v", fg.fetchCalls, want)
	}
	if fr.createBindingReq.BaseCommit != "tip" {
		t.Errorf("BaseCommit = %q, want the branch tip tip", fr.createBindingReq.BaseCommit)
	}
	if len(fg.createBranchCalls) != 0 {
		t.Errorf("an adopted branch must not be created: %+v", fg.createBranchCalls)
	}
	if !got.Binding.ExistingBranch || got.Binding.Branch != "feature/x" {
		t.Errorf("binding = branch %q existing %v, want feature/x and true",
			got.Binding.Branch, got.Binding.ExistingBranch)
	}
}

// TestAddRemoteBranchFetchFailureRefuses pins cases 4 and 5 on the --server
// path: the refusal happens before any server call, so CreateBinding is never
// reached, and the error is classed as a refusal.
func TestAddRemoteBranchFetchFailureRefuses(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		fetch error
		pop   map[string]string
	}{
		{name: "fetch fails", fetch: errors.New("fatal: could not read from remote")},
		{name: "fetched but still absent", pop: map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := store.New(t.TempDir())
			fg := &fakeGit{
				branchExists:   false,
				refSHA:         map[string]string{},
				rootCommitSHA:  "2222222222222222222222222222222222222222",
				fetchErr:       tc.fetch,
				fetchPopulates: tc.pop,
			}
			fr := &fakeRemote{createBindingResp: remote.BindingView{Name: "x"}}
			rt := Runtime{Store: st, Git: fg, Remote: fr, Now: time.Now, MasterMinds: addRemoteMasterMind(t)}

			_, err := Add(ctx, rt, AddOptions{
				Name: "x", Branch: "feature/x", Server: "zen", Repo: "/fake/repo",
			})
			if err == nil {
				t.Fatal("Add --server --branch: got nil, want a refusal")
			}
			if !errors.Is(err, ErrRefused) {
				t.Errorf("err = %v, want ErrRefused so the CLI renders `refused`", err)
			}
			if !strings.Contains(err.Error(), "not found locally or on origin") {
				t.Errorf("err = %q, want the existing wording kept", err)
			}
			if len(fr.calls) != 0 {
				t.Errorf("no server traffic may precede the refusal, got %+v", fr.calls)
			}
			if len(fg.createTrackingBranchCalls)+len(fg.createBranchCalls)+len(fg.addWorktreeCalls) != 0 {
				t.Errorf("a refused branch must reach no git write: tracking=%+v create=%+v add=%+v",
					fg.createTrackingBranchCalls, fg.createBranchCalls, fg.addWorktreeCalls)
			}
		})
	}
}
