package remote

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/git"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\nOutput: %s", strings.Join(args, " "), dir, err, string(out))
	}

	// A repo these tests create gets auto-maintenance off. Every `git commit`
	// otherwise spawns `git maintenance run --auto --quiet --detach`, which
	// outlives the command and writes under .git/objects while t.TempDir()'s
	// RemoveAll is removing the tree -- and that cleanup failure fails the
	// test, not just the teardown (#304). Repo-local config, so every later
	// git command on it inherits it, including ones the code under test runs.
	if len(args) > 0 && args[0] == "init" {
		runGit(t, dir, "config", "maintenance.auto", "false")
		runGit(t, dir, "config", "gc.auto", "0")
	}
	return string(out)
}

func TestBundleTransportOutboundThenInbound(t *testing.T) {
	ctx := context.Background()
	g := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)
	transport := NewBundleTransport(g, t.TempDir())

	// Client repo
	clientRepo := t.TempDir()
	runGit(t, clientRepo, "init")
	if err := os.WriteFile(filepath.Join(clientRepo, "f1.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, clientRepo, "add", "f1.txt")
	runGit(t, clientRepo, "commit", "-m", "c1")
	c1 := strings.TrimSpace(runGit(t, clientRepo, "rev-parse", "HEAD"))
	branch := "refs/heads/relevo/api"
	runGit(t, clientRepo, "update-ref", branch, c1)

	// Server bare repo
	bareServer := t.TempDir()
	runGit(t, bareServer, "init", "--bare")

	// Round 1 OUTBOUND (client -> server)
	lastShipped := ""
	snap1, err := transport.Snapshot(ctx, clientRepo, []string{branch}, lastShipped)
	if err != nil {
		t.Fatalf("Snapshot round 1: %v", err)
	}
	if snap1.Empty || snap1.Body == nil {
		t.Fatal("snap1 should not be empty")
	}
	moved, err := transport.Absorb(ctx, bareServer, snap1.ContentType, snap1.Body, []string{branch})
	_ = snap1.Body.Close()
	if err != nil {
		t.Fatalf("Absorb round 1: %v", err)
	}
	if moved[branch] != c1 {
		t.Fatalf("moved[%s] = %q, want %q", branch, moved[branch], c1)
	}
	lastShipped = snap1.Heads[branch]

	// Round 1 INBOUND (server -> client): clean, nothing new
	lastKnown := lastShipped
	snapIn1, err := transport.Snapshot(ctx, bareServer, []string{branch}, lastKnown)
	if err != nil {
		t.Fatalf("Snapshot inbound round 1: %v", err)
	}
	if !snapIn1.Empty {
		t.Fatal("expected empty snapshot for clean inbound round 1")
	}

	// Round 2 OUTBOUND: client adds c2
	if err := os.WriteFile(filepath.Join(clientRepo, "f2.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, clientRepo, "add", "f2.txt")
	runGit(t, clientRepo, "commit", "-m", "c2")
	c2 := strings.TrimSpace(runGit(t, clientRepo, "rev-parse", "HEAD"))
	runGit(t, clientRepo, "update-ref", branch, c2)

	snap2, err := transport.Snapshot(ctx, clientRepo, []string{branch}, lastShipped)
	if err != nil {
		t.Fatalf("Snapshot round 2: %v", err)
	}
	if snap2.Empty || snap2.Body == nil {
		t.Fatal("snap2 should not be empty")
	}
	moved2, err := transport.Absorb(ctx, bareServer, snap2.ContentType, snap2.Body, []string{branch})
	_ = snap2.Body.Close()
	if err != nil {
		t.Fatalf("Absorb round 2: %v", err)
	}
	if moved2[branch] != c2 {
		t.Fatalf("moved2[%s] = %q, want %q", branch, moved2[branch], c2)
	}
	lastShipped = snap2.Heads[branch]

	// Server round 2 execution: produces commit c3
	treeServer := strings.TrimSpace(runGit(t, bareServer, "rev-parse", c2+"^{tree}"))
	c3, err := g.CommitTree(ctx, bareServer, treeServer, c2, "server commit c3")
	if err != nil {
		t.Fatalf("CommitTree c3: %v", err)
	}
	if err := g.UpdateRef(ctx, bareServer, branch, c3, c2); err != nil {
		t.Fatalf("UpdateRef branch c3: %v", err)
	}

	// Server round 2 closed dirty: side ref refs/relevo/api/round-2
	sideRef := "refs/relevo/api/round-2"
	sideCommit, err := g.CommitTree(ctx, bareServer, treeServer, c3, "[relevo] api: round 2, uncommitted work")
	if err != nil {
		t.Fatalf("CommitTree side: %v", err)
	}
	if err := g.UpdateRef(ctx, bareServer, sideRef, sideCommit, ""); err != nil {
		t.Fatalf("UpdateRef side: %v", err)
	}

	// Round 2 INBOUND (server -> client)
	inboundRefs := []string{branch, sideRef}
	snapIn2, err := transport.Snapshot(ctx, bareServer, inboundRefs, lastKnown)
	if err != nil {
		t.Fatalf("Snapshot inbound round 2: %v", err)
	}
	if snapIn2.Empty || snapIn2.Body == nil {
		t.Fatal("snapIn2 should not be empty")
	}
	movedIn2, err := transport.Absorb(ctx, clientRepo, snapIn2.ContentType, snapIn2.Body, inboundRefs)
	_ = snapIn2.Body.Close()
	if err != nil {
		t.Fatalf("Absorb inbound round 2: %v", err)
	}
	if movedIn2[branch] != c3 || movedIn2[sideRef] != sideCommit {
		t.Fatalf("unexpected movedIn2: %v", movedIn2)
	}

	// Assert client's refs/relevo/api/round-2 exists and its parent is client's refs/heads/relevo/api
	clientSideSHA, ok, err := g.RefSHA(ctx, clientRepo, sideRef)
	if err != nil || !ok || clientSideSHA != sideCommit {
		t.Fatalf("client sideRef: got (%q, %v, %v), want (%q, true, nil)", clientSideSHA, ok, err, sideCommit)
	}
	clientHeadSHA, ok, err := g.RefSHA(ctx, clientRepo, branch)
	if err != nil || !ok || clientHeadSHA != c3 {
		t.Fatalf("client branch: got (%q, %v, %v), want (%q, true, nil)", clientHeadSHA, ok, err, c3)
	}

	catOut := runGit(t, clientRepo, "cat-file", "-p", clientSideSHA)
	if !strings.Contains(catOut, "parent "+clientHeadSHA) {
		t.Fatalf("sideRef commit does not have branch head %q as parent:\n%s", clientHeadSHA, catOut)
	}
}

func TestBundleTransportEmptySnapshot(t *testing.T) {
	ctx := context.Background()
	g := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)
	transport := NewBundleTransport(g, t.TempDir())

	repo := t.TempDir()
	runGit(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "f.txt")
	runGit(t, repo, "commit", "-m", "init")
	c1 := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	ref := "refs/heads/master"
	runGit(t, repo, "update-ref", ref, c1)

	snap, err := transport.Snapshot(ctx, repo, []string{ref}, c1)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !snap.Empty {
		t.Fatal("expected Empty == true")
	}
	if snap.Body != nil {
		t.Fatal("expected Body == nil")
	}

	// Absorb of a zero-length reader returns an empty map
	moved, err := transport.Absorb(ctx, repo, ContentTypeGitBundle, bytes.NewReader(nil), []string{ref})
	if err != nil {
		t.Fatalf("Absorb empty: %v", err)
	}
	if len(moved) != 0 {
		t.Fatalf("expected empty map from zero-length absorb, got: %v", moved)
	}
}

func TestBundleTransportUnexpectedRef(t *testing.T) {
	ctx := context.Background()
	g := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)
	transport := NewBundleTransport(g, t.TempDir())

	repoA := t.TempDir()
	runGit(t, repoA, "init")
	if err := os.WriteFile(filepath.Join(repoA, "f.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoA, "add", "f.txt")
	runGit(t, repoA, "commit", "-m", "c1")
	c1 := strings.TrimSpace(runGit(t, repoA, "rev-parse", "HEAD"))

	refMain := "refs/heads/main"
	refAPI := "refs/heads/relevo/api"
	runGit(t, repoA, "update-ref", refMain, c1)
	runGit(t, repoA, "update-ref", refAPI, c1)

	// Snapshot carrying both refs
	snap, err := transport.Snapshot(ctx, repoA, []string{refMain, refAPI}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Body.Close()

	bareB := t.TempDir()
	runGit(t, bareB, "init", "--bare")

	// Absorb only names refAPI, but bundle carries refMain too
	_, err = transport.Absorb(ctx, bareB, snap.ContentType, snap.Body, []string{refAPI})
	if !errors.Is(err, ErrUnexpectedRef) {
		t.Fatalf("Absorb unexpected ref: got %v, want ErrUnexpectedRef", err)
	}

	// Assert nothing moved in bareB
	_, ok, _ := g.RefSHA(ctx, bareB, refAPI)
	if ok {
		t.Fatal("bareB refAPI unexpectedly moved")
	}
}

func TestBundleTransportSinceUnknown(t *testing.T) {
	ctx := context.Background()
	g := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)
	transport := NewBundleTransport(g, t.TempDir())

	repo := t.TempDir()
	runGit(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "f.txt")
	runGit(t, repo, "commit", "-m", "c1")
	ref := "refs/heads/master"
	runGit(t, repo, "update-ref", ref, strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")))

	_, err := transport.Snapshot(ctx, repo, []string{ref}, "0123456789abcdef0123456789abcdef01234567")
	if !errors.Is(err, ErrSinceUnknown) {
		t.Fatalf("Snapshot since unknown: got %v, want ErrSinceUnknown", err)
	}
}

func TestBundleTransportUnsupportedType(t *testing.T) {
	ctx := context.Background()
	g := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)
	transport := NewBundleTransport(g, t.TempDir())

	repo := t.TempDir()
	runGit(t, repo, "init")

	_, err := transport.Absorb(ctx, repo, "application/json", strings.NewReader("{}"), []string{"refs/heads/master"})
	if !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("Absorb unsupported type: got %v, want ErrUnsupportedType", err)
	}
}

func TestBundleTransportLeavesNoTempFiles(t *testing.T) {
	ctx := context.Background()
	g := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)
	tmpDir := t.TempDir()
	transport := NewBundleTransport(g, tmpDir)

	repo := t.TempDir()
	runGit(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "f.txt")
	runGit(t, repo, "commit", "-m", "init")
	c1 := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	ref := "refs/heads/master"
	runGit(t, repo, "update-ref", ref, c1)

	// 1. Snapshot and close
	snap, err := transport.Snapshot(ctx, repo, []string{ref}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := snap.Body.Close(); err != nil {
		t.Fatal(err)
	}

	// 2. Empty snapshot
	snapEmpty, err := transport.Snapshot(ctx, repo, []string{ref}, c1)
	if err != nil {
		t.Fatal(err)
	}
	if !snapEmpty.Empty {
		t.Fatal("expected empty")
	}

	// 3. Snapshot with error
	_, _ = transport.Snapshot(ctx, repo, []string{ref}, "0123456789abcdef0123456789abcdef01234567")

	// 4. Absorb error paths
	_, _ = transport.Absorb(ctx, repo, "unsupported", strings.NewReader("bad"), []string{ref})
	_, _ = transport.Absorb(ctx, repo, ContentTypeGitBundle, strings.NewReader("not a bundle"), []string{ref})
	_, _ = transport.Absorb(ctx, repo, ContentTypeGitBundle, bytes.NewReader(nil), []string{ref})

	// Check that tmpDir is completely empty
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("temp directory not empty: %v", names)
	}
}

// TestSnapshotRefusesSwappedSymlink pins the re-open rule: a regular temp
// bundle swapped for a symlink between git writing it and relevo re-opening it
// is refused (O_NOFOLLOW plus a handle Stat), and its target is never read.
func TestSnapshotRefusesSwappedSymlink(t *testing.T) {
	ctx := context.Background()
	swapDir := t.TempDir()
	stubDir := t.TempDir()
	target := filepath.Join(t.TempDir(), "secret")
	const planted = "secret\n"
	if err := os.WriteFile(target, []byte(planted), 0o600); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(stubDir, "git")
	script := `#!/bin/sh
if [ "$1" = "rev-parse" ]; then
  echo 1111111111111111111111111111111111111111
  exit 0
fi
if [ "$1" = "bundle" ] && [ "$2" = "create" ]; then
  rm -f "$3"
  ln -s "$RELEVO_SWAP_TARGET" "$3"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RELEVO_SWAP_TARGET", target)

	g := git.NewClient(stub, 5*time.Second, git.DefaultMaxPatchBytes)
	transport := NewBundleTransport(g, swapDir)

	snap, err := transport.Snapshot(ctx, t.TempDir(), []string{"refs/heads/x"}, "")
	if err == nil {
		if snap.Body != nil {
			_ = snap.Body.Close()
		}
		t.Fatal("Snapshot followed a swapped symlink; want a refusal")
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("read the target: %v", readErr)
	}
	if string(got) != planted {
		t.Errorf("target = %q, want byte-unchanged %q: the symlink must not be read", got, planted)
	}
}

// TestAbsorbChownsTempToOwner pins that a user-mode transport hands its temp
// bundle to the owner before git touches it: the first git call sees the temp
// file owned by the configured uid:gid.
func TestAbsorbChownsTempToOwner(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	stubDir := t.TempDir()
	logPath := filepath.Join(stubDir, "owner.log")
	stub := filepath.Join(stubDir, "git")
	script := `#!/bin/sh
for a in "$@"; do
  case "$a" in
    *relevo-bundle-*) if [ -f "$a" ]; then ls -ln "$a" | awk 'NR==1{print $3":"$4}' >> "$RELEVO_OWNER_LOG"; fi ;;
  esac
done
if [ "$1" = "bundle" ] && [ "$2" = "list-heads" ]; then
  echo "0000000000000000000000000000000000000000 refs/heads/x"
fi
exit 0
`
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RELEVO_OWNER_LOG", logPath)

	g := git.NewClient(stub, 5*time.Second, git.DefaultMaxPatchBytes)
	transport := NewBundleTransport(g, tmpDir, WithOwnerTmp(tmpDir, uint32(os.Getuid()), uint32(os.Getgid())))

	moved, err := transport.Absorb(ctx, tmpDir, ContentTypeGitBundle, strings.NewReader("BUNDLE"), []string{"refs/heads/x"})
	if err != nil {
		t.Fatalf("Absorb: %v", err)
	}
	if moved["refs/heads/x"] != "0000000000000000000000000000000000000000" {
		t.Fatalf("moved = %v, want the head the stub printed", moved)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("the stub never saw the temp bundle: %v", err)
	}
	want := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	lines := strings.Fields(string(data))
	if len(lines) == 0 {
		t.Fatal("the stub recorded no temp-file owner")
	}
	for _, line := range lines {
		if line != want {
			t.Errorf("temp bundle owner = %q, want %q", line, want)
		}
	}
}

func TestBundlePathUnderStateRoot(t *testing.T) {
	ctx := context.Background()
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	g := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)
	transport := NewBundleTransport(g, "")

	repo := t.TempDir()
	runGit(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "f.txt")
	runGit(t, repo, "commit", "-m", "init")
	head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "update-ref", "refs/heads/main", head)

	snap, err := transport.Snapshot(ctx, repo, []string{"refs/heads/main"}, "")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer snap.Body.Close()

	remover, ok := snap.Body.(*fileRemover)
	if !ok {
		t.Fatalf("snap.Body is %T, want *fileRemover", snap.Body)
	}

	wantPrefix := filepath.Join(stateHome, "relevo", "tmp")
	if !strings.HasPrefix(remover.path, wantPrefix) {
		t.Errorf("bundle path = %q, want prefix %q", remover.path, wantPrefix)
	}
}

// TestSnapshotTimeoutSurfacesDeadlineExceeded pins #959's premise at the
// transport half: git.Client bounds every git call by its own timeout, and a
// blown budget keeps errors.Is(err, context.DeadlineExceeded) true all the way
// out of Snapshot. The verb half above it can therefore probe for it, which is
// what remoteShip's one retry-on-deadline does.
func TestSnapshotTimeoutSurfacesDeadlineExceeded(t *testing.T) {
	ctx := context.Background()
	g := git.NewClient("git", time.Nanosecond, git.DefaultMaxPatchBytes)
	transport := NewBundleTransport(g, t.TempDir())

	repo := t.TempDir()
	runGit(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "f.txt")
	runGit(t, repo, "commit", "-m", "init")
	head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "update-ref", "refs/heads/main", head)

	_, err := transport.Snapshot(ctx, repo, []string{"refs/heads/main"}, "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Snapshot err = %v, want context.DeadlineExceeded to survive the timeout", err)
	}
}
