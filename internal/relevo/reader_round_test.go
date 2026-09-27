package relevo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/store"
)

// readerRepo builds a real git repository for a reader round's scratch
// worktree, with one committed file, so the scratch has something to hold.
func readerRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "a.txt")
	runGit(t, repo, "commit", "-m", "first")
	return repo
}

// TestReaderRoundRunsInItsScratch (A5 R4a): sending to a reader binding
// creates ScratchWorktreePath(name, 1) and runs the process there, and the
// prompt names the scratch tree, the artifact dir and the reviewer's output.
func TestReaderRoundRunsInItsScratch(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt := newRuntime(t)
	rt.Git = git.NewClient("git", 0, 0)
	fr := newFakeRunner()
	rt.Runner = fr

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "reader-bind", Role: "reviewer", Candidate: testClaudeRef,
		MasterMindID: testMasterMindName, CWD: repo,
	}); err != nil {
		t.Fatalf("Bind(reader): %v", err)
	}
	if _, err := Send(context.Background(), rt, "reader-bind", writePlan(t, "review it"), SendOptions{}); err != nil {
		t.Fatalf("Send(reader): %v", err)
	}

	scratch := rt.Store.ScratchWorktreePath("reader-bind", 1)
	fi, err := os.Stat(scratch)
	if err != nil || !fi.IsDir() {
		t.Fatalf("scratch %s was not created: %v", scratch, err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want one process", len(fr.specs))
	}
	spec := fr.specs[0]
	if spec.Dir != scratch {
		t.Errorf("process Dir = %q, want the scratch %q, not b.CWD %q", spec.Dir, scratch, repo)
	}
	prompt := strings.Join(spec.Argv, " ")
	if !strings.Contains(prompt, "Your working tree is: "+scratch) {
		t.Errorf("prompt does not name the scratch tree first:\n%s", prompt)
	}
	artifact := rt.Store.ArtifactDir("reader-bind", 1, "reviewer")
	if !strings.Contains(prompt, artifact) {
		t.Errorf("prompt does not name the artifact dir %q:\n%s", artifact, prompt)
	}
	if !strings.Contains(prompt, "your findings") {
		t.Errorf("prompt does not name the reviewer's output findings:\n%s", prompt)
	}
}

// TestReaderScratchFailureDoesNotStartTheRound (A5 R4a): when the scratch
// cannot be created, send wraps ErrScratch with its step, starts nothing, and
// leaves the binding's own tree alone -- there is no fallback to b.CWD.
func TestReaderScratchFailureDoesNotStartTheRound(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Git = &fakeGit{
		headCommitID:           "head1",
		snapshotTreeID:         "tree1",
		addDetachedWorktreeErr: errors.New("no worktree here"),
	}
	fr := newFakeRunner()
	rt.Runner = fr

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "reader-bind", Role: "reviewer", Candidate: testClaudeRef,
		MasterMindID: testMasterMindName, CWD: "/reader-repo",
	}); err != nil {
		t.Fatalf("Bind(reader): %v", err)
	}

	_, err := Send(context.Background(), rt, "reader-bind", writePlan(t, "review it"), SendOptions{})
	if err == nil {
		t.Fatal("Send = nil, want an ErrScratch error")
	}
	if !errors.Is(err, ErrScratch) {
		t.Fatalf("Send err = %v, want it to wrap ErrScratch", err)
	}
	if !strings.Contains(err.Error(), "add") {
		t.Errorf("Send err = %q, want it to name the failed add step", err)
	}
	if len(fr.specs) != 0 {
		t.Errorf("specs = %d, want no process started", len(fr.specs))
	}
	b, err := rt.Store.Load("reader-bind")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.CWD != "/reader-repo" {
		t.Errorf("CWD = %q, want the binding's own tree untouched", b.CWD)
	}
	if b.State == store.StateNeedsYou {
		t.Errorf("State = %q, want the binding untouched by a failed send", b.State)
	}
}

// TestReaderRelaunchReusesTheScratch (A5 R4a): a mid-round switch starts
// the replacement in the round's existing scratch. A marker file written into
// the scratch before the switch survives it, which is what tells reuse apart
// from recreation.
func TestReaderRelaunchReusesTheScratch(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt := newRuntime(t)
	rt.Git = git.NewClient("git", 0, 0)
	rt.Candidates = candidateSet(t, `[
	  {"harness":"claude","provider":"p1","model":"b","roles":["builder","reviewer"]},
	  {"harness":"claude","provider":"p2","model":"c","roles":["builder","reviewer"]}
	]`)
	rt.Policy = orderOf("reviewer", "claude/p1/b", "claude/p2/c")
	fr := newFakeRunner()
	rt.Runner = fr

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "reader-bind", Role: "reviewer", Candidate: "claude/p1/b",
		MasterMindID: testMasterMindName, CWD: repo,
	}); err != nil {
		t.Fatalf("Bind(reader): %v", err)
	}
	if _, err := Send(context.Background(), rt, "reader-bind", writePlan(t, "review it"), SendOptions{}); err != nil {
		t.Fatalf("Send(reader): %v", err)
	}

	scratch := rt.Store.ScratchWorktreePath("reader-bind", 1)
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want 1 before the switch", len(fr.specs))
	}
	if fr.specs[0].Dir != scratch {
		t.Fatalf("first process Dir = %q, want the scratch %q", fr.specs[0].Dir, scratch)
	}
	marker := filepath.Join(scratch, "marker.txt")
	if err := os.WriteFile(marker, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := availability.Unavailable(AvailabilityDeps(rt), "claude/p1/b", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	b, err := rt.Store.Load("reader-bind")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.BuilderCandidate != "claude/p2/c" {
		t.Fatalf("BuilderCandidate = %q, want the switch to claude/p2/c", got.BuilderCandidate)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("specs = %d, want a second process after the switch", len(fr.specs))
	}
	if fr.specs[1].Dir != scratch {
		t.Errorf("relaunch Dir = %q, want the same scratch %q", fr.specs[1].Dir, scratch)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("marker inside the scratch is gone after the switch (%v): the switch recreated the scratch instead of reusing it", err)
	}
}

// TestWriterRoundStillRunsInCWD (A5 R4a): a writer round is unchanged --
// it runs in b.CWD and creates no scratch.
func TestWriterRoundStillRunsInCWD(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Git = &fakeGit{headCommitID: "head1", snapshotTreeID: "tree1"}
	fr := newFakeRunner()
	rt.Runner = fr

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo",
	}); err != nil {
		t.Fatalf("Bind(writer): %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send(writer): %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(fr.specs))
	}
	if fr.specs[0].Dir != "/repo" {
		t.Errorf("writer process Dir = %q, want b.CWD /repo", fr.specs[0].Dir)
	}
	if _, err := os.Stat(rt.Store.ScratchWorktreePath("webshop", 1)); !os.IsNotExist(err) {
		t.Errorf("a writer round created a scratch: %v", err)
	}
}

// TestReaderTierAtLeastEdit (A5 R4a): a reader binds at edit or higher,
// a max_tier below edit refuses the bind by name, and a harness that refuses
// edit (opencode) gets its lowest writing tier, yolo.
func TestReaderTierAtLeastEdit(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "reader-tier", Role: "reviewer", Candidate: testClaudeRef,
		MasterMindID: testMasterMindName, CWD: "/reader-tier",
	})
	if err != nil {
		t.Fatalf("Bind(reader): %v", err)
	}
	if b.Tier != string(harness.TierEdit) {
		t.Errorf("reader Tier = %q, want edit (never harness or read)", b.Tier)
	}

	low := newRuntime(t)
	low.Policy.MaxTier = string(harness.TierRead)
	_, err = Bind(context.Background(), low, BindOptions{
		Name: "reader-low", Role: "reviewer", Candidate: testClaudeRef,
		MasterMindID: testMasterMindName, CWD: "/reader-low",
	})
	want := "a reader actor needs tier edit; max_tier is read"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Bind with max_tier read = %v, want %q", err, want)
	}

	openReader := func(t *testing.T) Runtime {
		t.Helper()
		rt := newRuntime(t)
		rt.Candidates = candidateSet(t, `[{"harness":"opencode","provider":"test","model":"m","roles":["builder","reviewer"]}]`)
		return rt
	}

	oc, err := Bind(context.Background(), openReader(t), BindOptions{
		Name: "reader-oc", Role: "reviewer", Candidate: testOpencodeRef,
		MasterMindID: testMasterMindName, CWD: "/reader-oc", AllowYolo: true,
	})
	if err != nil {
		t.Fatalf("Bind(opencode reader, --allow-yolo): %v", err)
	}
	if oc.Tier != string(harness.TierYolo) {
		t.Errorf("opencode reader Tier = %q, want yolo (its lowest writing tier)", oc.Tier)
	}

	_, err = Bind(context.Background(), openReader(t), BindOptions{
		Name: "reader-oc2", Role: "reviewer", Candidate: testOpencodeRef,
		MasterMindID: testMasterMindName, CWD: "/reader-oc2",
	})
	if !errors.Is(err, ErrTierAboveMax) {
		t.Errorf("opencode reader without --allow-yolo err = %v, want ErrTierAboveMax", err)
	}
}
