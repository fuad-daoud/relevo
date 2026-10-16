package serve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// backgroundResult is one in-flight request's outcome from a goroutine, so
// every assertion stays on the test goroutine where t.Fatalf is legal.
type backgroundResult struct {
	status int
	body   []byte
	err    error
}

// fireInBackground issues req on its own goroutine and returns the channel its
// outcome lands on. The request was built with signedPost or signedRequest, on
// the test goroutine.
func fireInBackground(req *http.Request) <-chan backgroundResult {
	done := make(chan backgroundResult, 1)
	go func() {
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			done <- backgroundResult{err: err}
			return
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		done <- backgroundResult{status: resp.StatusCode, body: body, err: readErr}
	}()
	return done
}

// seamParker builds a seam body that closes parked and blocks until release.
// release is once-guarded so a panicked test cannot close the channel twice.
func seamParker() (seam func(), parked chan struct{}, release func()) {
	parked = make(chan struct{})
	releaseCh := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(releaseCh) }) }
	seam = func() { close(parked); <-releaseCh }
	return seam, parked, release
}

// awaitPark waits for the seam to fire, failing with the request's outcome if
// it never does, so a pre-seam refusal fails the test instead of hanging it.
func awaitPark(t *testing.T, parked chan struct{}, done <-chan backgroundResult) {
	t.Helper()
	select {
	case <-parked:
		return
	case <-time.After(20 * time.Second):
		select {
		case p := <-done:
			t.Fatalf("request never reached the seam: status %d, err %v, body %s", p.status, p.err, string(p.body))
		case <-time.After(2 * time.Second):
			t.Fatal("request never reached the seam and never answered")
		}
	}
}

// TestTickPruneSparesAChainCreateBetweenWorktreeAndRow parks a chain create on
// the seam between the worktree cut and the chain row write, runs the tick's
// prune inside that window, and pins that the bare repo survives: the worktree
// opens in git and the create still answers 201 with its rows. Without the
// pin, the prune's list-and-delete finds no row naming the repo and deletes it
// while the create sits between the checkout and the write.
func TestTickPruneSparesAChainCreateBetweenWorktreeAndRow(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	seam, parked, release := seamParker()
	chainCreateSeam = seam
	defer release()
	defer func() { chainCreateSeam = nil }()

	req := chainWireRequest("shop", env.repoID, env.headSHA)
	bundle := chainBundle(t, env, env.clientDir, env.headSHA, "shop")
	form, ct := makeChainForm(t, req, bundle)
	httpReq := signedPost(t, env.ts, env.kp, "/v1/chains", form, ct)
	done := fireInBackground(httpReq)

	awaitPark(t, parked, done)
	bare := chainRepoPath(t, env)
	rt := env.runtime(t)
	worktree := rt.Store.WorktreePath("shop")
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("worktree mid-park: %v", err)
	}
	if _, err := rt.Store.Chain("shop"); err == nil {
		t.Fatal("chain row exists mid-park; the seam is misplaced")
	}
	env.srv.pruneUnusedRepos(ctx)
	release()

	p := <-done
	if p.err != nil {
		t.Fatalf("chain create: %v", p.err)
	}
	if p.status != http.StatusCreated {
		t.Fatalf("chain create status = %d, want 201; body: %s", p.status, string(p.body))
	}
	if _, err := os.Stat(bare); err != nil {
		t.Fatalf("bare repo pruned mid-create: %v", err)
	}
	runGit(t, worktree, "status", "--short")
	if _, err := rt.Store.Chain("shop"); err != nil {
		t.Fatalf("chain row after create: %v", err)
	}
}

// TestTickPruneSparesABindBetweenInitBareAndSave parks a plain bind between
// InitBare and the row save, runs the tick's prune inside that window, and
// pins that the bare repo survives: the create answers 201 with a working
// repo, and the saved row names it.
func TestTickPruneSparesABindBetweenInitBareAndSave(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	seam, parked, release := seamParker()
	bindCreateSeam = seam
	defer release()
	defer func() { bindCreateSeam = nil }()

	createBody, err := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
		Role:       "builder",
	})
	if err != nil {
		t.Fatal(err)
	}
	httpReq := signedPost(t, env.ts, env.kp, "/v1/bindings", createBody, "application/json")
	done := fireInBackground(httpReq)

	awaitPark(t, parked, done)
	bare := chainRepoPath(t, env)
	if _, err := os.Stat(bare); err != nil {
		t.Fatalf("bare repo mid-park: %v", err)
	}
	env.srv.pruneUnusedRepos(ctx)
	release()

	p := <-done
	if p.err != nil {
		t.Fatalf("bind create: %v", p.err)
	}
	if p.status != http.StatusCreated {
		t.Fatalf("bind create status = %d, want 201; body: %s", p.status, string(p.body))
	}
	if _, err := os.Stat(bare); err != nil {
		t.Fatalf("bare repo pruned mid-bind: %v", err)
	}
	runGit(t, bare, "rev-parse", "--is-bare-repository")
	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("binding after create: %v", err)
	}
	if b.Serve == nil || filepath.Clean(b.Serve.BareRepo) != filepath.Clean(bare) {
		t.Fatalf("binding names bare %v, want %s", b.Serve, bare)
	}
}

// TestTickPruneSparesAResumeBetweenCheckoutAndSave parks a resume between the
// worktree checkout and the re-activation save, archives the row while it is
// parked, and runs the tick's prune. An archived row drops out of the prune's
// live set, so without the pin the bare repo goes away while the checkout that
// just created the worktree is still parked; with it the repo survives until
// the save lands.
func TestTickPruneSparesAResumeBetweenCheckoutAndSave(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	// A DONE served binding whose worktree was released: the bare repo, the
	// branch and the row survive, the worktree is gone -- removed the way the
	// server's own teardown removes it, so no stale git worktree entry is left
	// behind for the resume's checkout to trip on.
	bare, worktree := seedServedBinding(t, env, "resumed", store.ServeFacts{RepoID: env.repoID})
	if err := env.gitClient.RemoveWorktree(ctx, bare, worktree, true); err != nil {
		t.Fatalf("release worktree: %v", err)
	}

	seam, parked, release := seamParker()
	resumeSeam = seam
	defer release()
	defer func() { resumeSeam = nil }()

	httpReq := signedRequest(t, env.kp, "POST", "/v1/bindings/resumed/resume", nil)
	httpReq.URL.Scheme = "http"
	httpReq.URL.Host = env.ts.Listener.Addr().String()
	done := fireInBackground(httpReq)

	awaitPark(t, parked, done)
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("worktree mid-park: %v", err)
	}
	rt := env.runtime(t)
	if _, err := relevo.Unbind(ctx, rt, "resumed", true); err != nil {
		t.Fatalf("unbind mid-park: %v", err)
	}
	env.srv.pruneUnusedRepos(ctx)
	release()

	p := <-done
	if p.err != nil {
		t.Fatalf("resume: %v", p.err)
	}
	if p.status != http.StatusOK {
		t.Fatalf("resume status = %d, want 200; body: %s", p.status, string(p.body))
	}
	if _, err := os.Stat(bare); err != nil {
		t.Fatalf("bare repo pruned mid-resume: %v", err)
	}
	// The unbind that archived the row also removed the freshly checked-out
	// worktree, as a real GC would; the pin's job ends with the bare repo.
	runGit(t, bare, "rev-parse", "--is-bare-repository")
}
