package git

import (
	"context"
	"testing"
	"time"
)

// TestFetchMakesOriginRefResolvable pins the assumption `bind --branch`'s
// fetch fallback rests on: `git fetch origin <branch>` is enough to make
// refs/remotes/origin/<branch> resolvable afterwards, so a re-probe of that
// one ref can adopt a branch the client had never fetched. The remote is a
// local bare repository, so no test touches the network.
//
// It also pins the negative the fallback depends on the other way: a fetch of
// a branch the remote does not have fails rather than succeeding quietly,
// which is why the caller can treat "fetch failed" and "fetched but still
// absent" as two refusals rather than one.
func TestFetchMakesOriginRefResolvable(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 10*time.Second, DefaultMaxPatchBytes)

	origin := bareRepo(t)
	// The branch exists only in the remote: it is committed here and pushed
	// under an explicit head refspec, then the tracking ref that push wrote is
	// deleted, leaving a branch the client has neither locally nor in its
	// remote-tracking cache -- exactly the state `bind --branch` starts from.
	// The RefSHA below asserts that.
	work, sha := initRepoWithCommit(t, "f1.txt", "1\n")
	runGit(t, work, "remote", "add", "origin", origin)
	runGit(t, work, "push", "origin", "HEAD:refs/heads/feature/x")
	runGit(t, work, "update-ref", "-d", "refs/remotes/origin/feature/x")

	if _, ok, err := client.RefSHA(ctx, work, "refs/remotes/origin/feature/x"); err != nil {
		t.Fatalf("RefSHA before Fetch: %v", err)
	} else if ok {
		t.Fatal("fixture is wrong: origin/feature/x resolved before any fetch")
	}

	if err := client.Fetch(ctx, work, "origin", "feature/x"); err != nil {
		t.Fatalf("Fetch(origin, feature/x): %v", err)
	}

	got, ok, err := client.RefSHA(ctx, work, "refs/remotes/origin/feature/x")
	if err != nil {
		t.Fatalf("RefSHA after Fetch: %v", err)
	}
	if !ok {
		t.Fatal("after Fetch, refs/remotes/origin/feature/x does not resolve: " +
			"the Fetch refspec does not populate the remote-tracking ref")
	}
	if got != sha {
		t.Errorf("fetched origin/feature/x = %s, want the pushed tip %s", got, sha)
	}

	// A branch the remote does not have: the fetch fails, so the caller
	// refuses on the fetch error rather than waiting for a re-probe that
	// could only miss.
	if err := client.Fetch(ctx, work, "origin", "no-such-branch"); err == nil {
		t.Error("Fetch of a branch the remote lacks returned nil; the caller " +
			"cannot tell a miss from a real failure")
	}
}
