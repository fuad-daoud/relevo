package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// bundleSource is a repo whose refs/heads/relevo/api points at the head after
// one commit per body; it returns the repo, the ref and the commit shas.
func bundleSource(t *testing.T, bodies ...string) (dir, ref string, shas []string) {
	t.Helper()
	dir, shas = repoWithCommits(t, bodies...)
	ref = "refs/heads/relevo/api"
	runGit(t, dir, "update-ref", ref, shas[len(shas)-1])
	return dir, ref, shas
}

func TestCommitTreeFromLinkedWorktree(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	bare := bareRepo(t)
	seedDir := t.TempDir()
	runGit(t, seedDir, "clone", bare, ".")
	writeGitFile(t, seedDir, "file.txt", "seed\n")
	runGit(t, seedDir, "add", "file.txt")
	runGit(t, seedDir, "commit", "-m", "init")
	runGit(t, seedDir, "push", "origin", "HEAD:refs/heads/relevo/api")

	wt := t.TempDir()
	runGit(t, bare, "worktree", "add", wt, "refs/heads/relevo/api")
	writeGitFile(t, wt, "untracked.txt", "dirty work\n")

	tree, err := client.SnapshotTree(ctx, wt)
	if err != nil {
		t.Fatalf("SnapshotTree: %v", err)
	}
	head, ok, err := client.RefSHA(ctx, bare, "refs/heads/relevo/api")
	if err != nil || !ok {
		t.Fatalf("RefSHA(bare): %v, ok=%v", err, ok)
	}

	sha, err := client.CommitTree(ctx, bare, tree, head, "[relevo] api: round 1, uncommitted work")
	if err != nil {
		t.Fatalf("CommitTree: %v", err)
	}
	if err := client.UpdateRef(ctx, bare, "refs/relevo/api/round-1", sha, ""); err != nil {
		t.Fatalf("UpdateRef: %v", err)
	}

	catOut := runGit(t, bare, "cat-file", "-p", sha)
	for _, want := range []string{
		"tree " + tree,
		"parent " + head,
		"author relevo <relevo@localhost>",
		"committer relevo <relevo@localhost>",
	} {
		if !strings.Contains(catOut, want) {
			t.Errorf("cat-file missing %q in:\n%s", want, catOut)
		}
	}

	if headAfter, ok, err := client.RefSHA(ctx, bare, "refs/heads/relevo/api"); err != nil || !ok || headAfter != head {
		t.Fatalf("refs/heads/relevo/api changed: got %q, want %q", headAfter, head)
	}
}

func TestBundleCreateFullThenIncremental(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	// A large first commit makes the incremental bundle measurably smaller.
	payload := make([]byte, 50000)
	for i := range payload {
		payload[i] = byte(i*31 + 7)
	}
	repo := initRepo(t)
	writeGitFile(t, repo, "large.txt", string(payload))
	runGit(t, repo, "add", "large.txt")
	runGit(t, repo, "commit", "-m", "first")
	c1 := mustHead(t, repo)
	ref := "refs/heads/master"
	runGit(t, repo, "update-ref", ref, c1)

	bFull := filepath.Join(t.TempDir(), "full.bundle")
	heads, empty, err := client.BundleCreate(ctx, repo, bFull, []string{ref}, "")
	if err != nil || empty {
		t.Fatalf("BundleCreate full: empty=%v, err=%v", empty, err)
	}
	if heads[ref] != c1 {
		t.Fatalf("heads[%s] = %q, want %q", ref, heads[ref], c1)
	}
	bHeads, err := client.BundleHeads(ctx, repo, bFull)
	if err != nil {
		t.Fatalf("BundleHeads full: %v", err)
	}
	if bHeads[ref] != c1 {
		t.Fatalf("bHeads[%s] = %q, want %q", ref, bHeads[ref], c1)
	}

	writeGitFile(t, repo, "small.txt", "small\n")
	runGit(t, repo, "add", "small.txt")
	runGit(t, repo, "commit", "-m", "second")
	c2 := mustHead(t, repo)
	runGit(t, repo, "update-ref", ref, c2)

	bIncr := filepath.Join(t.TempDir(), "incr.bundle")
	heads2, empty, err := client.BundleCreate(ctx, repo, bIncr, []string{ref}, c1)
	if err != nil || empty {
		t.Fatalf("BundleCreate incr: empty=%v, err=%v", empty, err)
	}
	if heads2[ref] != c2 {
		t.Fatalf("heads2[%s] = %q, want %q", ref, heads2[ref], c2)
	}
	bHeads2, err := client.BundleHeads(ctx, repo, bIncr)
	if err != nil {
		t.Fatalf("BundleHeads incr: %v", err)
	}
	if bHeads2[ref] != c2 {
		t.Fatalf("bHeads2[%s] = %q, want %q", ref, bHeads2[ref], c2)
	}

	fiFull, err := os.Stat(bFull)
	if err != nil {
		t.Fatal(err)
	}
	fiIncr, err := os.Stat(bIncr)
	if err != nil {
		t.Fatal(err)
	}
	if fiIncr.Size() >= fiFull.Size() {
		t.Fatalf("incremental bundle size (%d) >= full bundle size (%d)", fiIncr.Size(), fiFull.Size())
	}
}

func TestBundleHeadsMalformed(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repo, _ := repoWithCommits(t, "hello\n")

	badDir := t.TempDir()
	writeGitFile(t, badDir, "bad.bundle", "bad bundle content\n")
	if _, err := client.BundleHeads(ctx, repo, filepath.Join(badDir, "bad.bundle")); !errors.Is(err, ErrBadBundle) {
		t.Fatalf("BundleHeads on a malformed bundle: got %v, want ErrBadBundle", err)
	}
}

func TestBundleCreateEmptyWhenNothingNew(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repo, shas := repoWithCommits(t, "hello\n")
	c1 := shas[0]
	ref := "refs/heads/master"
	runGit(t, repo, "update-ref", ref, c1)

	bPath := filepath.Join(t.TempDir(), "empty.bundle")
	heads, empty, err := client.BundleCreate(ctx, repo, bPath, []string{ref}, c1)
	if err != nil {
		t.Fatalf("BundleCreate: %v", err)
	}
	if !empty {
		t.Fatal("BundleCreate returned empty=false, want true")
	}
	if heads[ref] != c1 {
		t.Fatalf("heads[%s] = %q, want %q", ref, heads[ref], c1)
	}
	if _, err := os.Stat(bPath); !os.IsNotExist(err) {
		t.Fatalf("expected bundle file not to exist, got err: %v", err)
	}
}

func TestBundleCreateSinceNotAncestor(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repo, _ := repoWithCommits(t, "root\n")

	runGit(t, repo, "checkout", "-b", "branchA")
	writeGitFile(t, repo, "a.txt", "a\n")
	runGit(t, repo, "add", "a.txt")
	runGit(t, repo, "commit", "-m", "a")
	refA := "refs/heads/branchA"

	runGit(t, repo, "checkout", "master")
	runGit(t, repo, "checkout", "-b", "branchB")
	writeGitFile(t, repo, "b.txt", "b\n")
	runGit(t, repo, "add", "b.txt")
	runGit(t, repo, "commit", "-m", "b")

	bPath := filepath.Join(t.TempDir(), "unrelated.bundle")
	if _, _, err := client.BundleCreate(ctx, repo, bPath, []string{refA}, mustHead(t, repo)); !errors.Is(err, ErrRefMissing) {
		t.Fatalf("BundleCreate with unrelated since: got %v, want ErrRefMissing", err)
	}
}

func TestBundleCreateTwoRefs(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repo, shas := repoWithCommits(t, "1\n", "2\n")
	c1, c2 := shas[0], shas[1]

	ref1 := "refs/heads/relevo/api"
	ref2 := "refs/relevo/api/round-1"
	runGit(t, repo, "update-ref", ref1, c2)
	runGit(t, repo, "update-ref", ref2, c1)

	bPath := filepath.Join(t.TempDir(), "two_refs.bundle")
	heads, empty, err := client.BundleCreate(ctx, repo, bPath, []string{ref1, ref2}, "")
	if err != nil || empty {
		t.Fatalf("BundleCreate two refs: empty=%v, err=%v", empty, err)
	}
	if heads[ref1] != c2 || heads[ref2] != c1 {
		t.Fatalf("unexpected heads: %v", heads)
	}

	bHeads, err := client.BundleHeads(ctx, repo, bPath)
	if err != nil {
		t.Fatalf("BundleHeads: %v", err)
	}
	if bHeads[ref1] != c2 || bHeads[ref2] != c1 {
		t.Fatalf("unexpected bHeads: %v", bHeads)
	}
}

func TestFetchBundleFastForwards(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repoA, ref, shas := bundleSource(t, "1\n")
	c1 := shas[0]
	bareB := bareRepo(t)

	b1 := filepath.Join(t.TempDir(), "b1.bundle")
	if _, _, err := client.BundleCreate(ctx, repoA, b1, []string{ref}, ""); err != nil {
		t.Fatalf("BundleCreate b1: %v", err)
	}
	fetched, err := client.FetchBundle(ctx, bareB, b1, []string{ref})
	if err != nil {
		t.Fatalf("FetchBundle b1: %v", err)
	}
	if fetched[ref] != c1 {
		t.Fatalf("fetched[%s] = %q, want %q", ref, fetched[ref], c1)
	}
	if shaB, ok, err := client.RefSHA(ctx, bareB, ref); err != nil || !ok || shaB != c1 {
		t.Fatalf("bareB ref = (%q, %v, %v); want (%q, true, nil)", shaB, ok, err, c1)
	}

	writeGitFile(t, repoA, "f2.txt", "2\n")
	runGit(t, repoA, "add", "f2.txt")
	runGit(t, repoA, "commit", "-m", "c2")
	c2 := mustHead(t, repoA)
	runGit(t, repoA, "update-ref", ref, c2)

	b2 := filepath.Join(t.TempDir(), "b2.bundle")
	if _, _, err := client.BundleCreate(ctx, repoA, b2, []string{ref}, c1); err != nil {
		t.Fatalf("BundleCreate b2: %v", err)
	}
	fetched2, err := client.FetchBundle(ctx, bareB, b2, []string{ref})
	if err != nil {
		t.Fatalf("FetchBundle b2: %v", err)
	}
	if fetched2[ref] != c2 {
		t.Fatalf("fetched2[%s] = %q, want %q", ref, fetched2[ref], c2)
	}
	if shaB2, ok, err := client.RefSHA(ctx, bareB, ref); err != nil || !ok || shaB2 != c2 {
		t.Fatalf("bareB ref = (%q, %v, %v); want (%q, true, nil)", shaB2, ok, err, c2)
	}
}

func TestFetchBundleRefusesNonFastForward(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repoA, ref, shas := bundleSource(t, "1\n")
	c1 := shas[0]
	bareB := bareRepo(t)

	b1 := filepath.Join(t.TempDir(), "b1.bundle")
	if _, _, err := client.BundleCreate(ctx, repoA, b1, []string{ref}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := client.FetchBundle(ctx, bareB, b1, []string{ref}); err != nil {
		t.Fatal(err)
	}

	// B's ref moves ahead with a local commit.
	treeB := strings.TrimSpace(runGit(t, bareB, "rev-parse", c1+"^{tree}"))
	localCommit, err := client.CommitTree(ctx, bareB, treeB, c1, "local divergence")
	if err != nil {
		t.Fatalf("CommitTree: %v", err)
	}
	if err := client.UpdateRef(ctx, bareB, ref, localCommit, c1); err != nil {
		t.Fatalf("UpdateRef: %v", err)
	}

	// A produces a commit B has not seen, so the fetch must refuse it.
	writeGitFile(t, repoA, "f2.txt", "2\n")
	runGit(t, repoA, "add", "f2.txt")
	runGit(t, repoA, "commit", "-m", "c2")
	runGit(t, repoA, "update-ref", ref, mustHead(t, repoA))

	b2 := filepath.Join(t.TempDir(), "b2.bundle")
	if _, _, err := client.BundleCreate(ctx, repoA, b2, []string{ref}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := client.FetchBundle(ctx, bareB, b2, []string{ref}); !errors.Is(err, ErrNotFastForward) {
		t.Fatalf("FetchBundle non-fast-forward: got %v, want ErrNotFastForward", err)
	}
	if got, ok, err := client.RefSHA(ctx, bareB, ref); err != nil || !ok || got != localCommit {
		t.Fatalf("bareB ref = %q, want %q", got, localCommit)
	}
}

func TestFetchBundleMissingPrereq(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repoA, ref, shas := bundleSource(t, "1\n", "2\n")
	bIncr := filepath.Join(t.TempDir(), "incr.bundle")
	if _, _, err := client.BundleCreate(ctx, repoA, bIncr, []string{ref}, shas[0]); err != nil {
		t.Fatal(err)
	}

	// B has never seen c1, so the bundle's prerequisite is missing.
	if _, err := client.FetchBundle(ctx, bareRepo(t), bIncr, []string{ref}); !errors.Is(err, ErrBadBundle) {
		t.Fatalf("FetchBundle missing prereq: got %v, want ErrBadBundle", err)
	}
}

func TestFetchBundleIgnoresRefsNotAsked(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repoA, shas := repoWithCommits(t, "1\n", "2\n")
	c1, c2 := shas[0], shas[1]

	ref1 := "refs/heads/relevo/api"
	ref2 := "refs/relevo/api/round-1"
	runGit(t, repoA, "update-ref", ref1, c1)
	runGit(t, repoA, "update-ref", ref2, c2)

	bPath := filepath.Join(t.TempDir(), "two.bundle")
	if _, _, err := client.BundleCreate(ctx, repoA, bPath, []string{ref1, ref2}, ""); err != nil {
		t.Fatal(err)
	}

	bareB := bareRepo(t)
	fetched, err := client.FetchBundle(ctx, bareB, bPath, []string{ref1})
	if err != nil {
		t.Fatalf("FetchBundle: %v", err)
	}
	if len(fetched) != 1 || fetched[ref1] != c1 {
		t.Fatalf("unexpected fetched map: %v", fetched)
	}
	if sha1, ok1, err := client.RefSHA(ctx, bareB, ref1); err != nil || !ok1 || sha1 != c1 {
		t.Fatalf("bareB ref1: got (%q, %v, %v), want (%q, true, nil)", sha1, ok1, err, c1)
	}
	if _, ok2, err := client.RefSHA(ctx, bareB, ref2); err != nil || ok2 {
		t.Fatalf("bareB ref2 unexpectedly present: ok=%v, err=%v", ok2, err)
	}
}
