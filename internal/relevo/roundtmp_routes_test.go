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
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// The tests here drive every start and removal route of a round's private
// temp directory through fakeRunner: the spec it captures is the fact a
// start made, and the directory's presence on disk is the fact a removal
// route was supposed to undo.

// sentWithTmpRoot is seedHeadless with the tmp root named before the Send, so
// the round's start prepares its directory: the runtime, and the binding one
// round into, live on fakeRunner.
func sentWithTmpRoot(t *testing.T, fr *fakeRunner) (Runtime, store.Binding) {
	t.Helper()
	rt, _ := seedHeadless(t, fr)
	rt.TmpRoot = t.TempDir()
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return rt, b
}

// roundTmpPath is the directory prepareProc made for one round.
func roundTmpPath(rt Runtime, name string, round int) string {
	return filepath.Join(rt.TmpRoot, roundTmpRel(name, round))
}

func mustExist(t *testing.T, dir string) {
	t.Helper()
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("round tmp dir %s does not exist: %v", dir, err)
	}
}

func mustBeGone(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("round tmp dir %s is still there (stat err = %v), want it removed", dir, err)
	}
}

// TestStartProcessSetsSpecTmpDir pins the start seam: the captured spec
// carries the round's directory on ProcSpec.TmpDir, and the directory exists
// on disk before the process starts -- the proc layer then points the
// child's TMPDIR and GOTMPDIR at it.
func TestStartProcessSetsSpecTmpDir(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := sentWithTmpRoot(t, fr)
	want := roundTmpPath(rt, "webshop", 1)

	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want the one started process", len(fr.specs))
	}
	if fr.specs[0].TmpDir != want {
		t.Errorf("spec TmpDir = %q, want %q", fr.specs[0].TmpDir, want)
	}
	mustExist(t, want)
}

// TestRoundTmpDirReaderSameAsWriter pins that the rule is shape-blind: a
// reader round's process carries a temp dir named exactly like a writer's,
// its scratch tree in Dir and the round dir in TmpDir.
func TestRoundTmpDirReaderSameAsWriter(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt := newRuntime(t)
	rt.Git = git.NewClient("git", 0, 0)
	rt.TmpRoot = t.TempDir()
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

	want := filepath.Join(rt.TmpRoot, "rounds", "reader-bind-001")
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want the one started process", len(fr.specs))
	}
	if fr.specs[0].TmpDir != want {
		t.Errorf("reader spec TmpDir = %q, want the writer-rule name %q", fr.specs[0].TmpDir, want)
	}
	mustExist(t, want)
}

// TestRoundCloseRemovesTmp pins the close funnel: the marker close runs the
// removal hook in queueReport, and every other close route reaches that same
// funnel.
func TestRoundCloseRemovesTmp(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentWithTmpRoot(t, fr)
	dir := roundTmpPath(rt, "webshop", 1)
	mustExist(t, dir)

	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.DonePath("webshop", 1), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("round = %d, want the marker close to have advanced it", got.Round)
	}
	mustBeGone(t, dir)
}

// TestDoneRemovesTmp pins the done hook. The process is cleared first: a
// live pid would let stopProcess's kill hook take the directory, and this
// test must fail when only the done hook is deleted.
func TestDoneRemovesTmp(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentWithTmpRoot(t, fr)
	dir := roundTmpPath(rt, "webshop", 1)
	mustExist(t, dir)

	b.Builder = clearProcess(b.Builder)
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	if _, err := Done(context.Background(), rt, "webshop"); err != nil {
		t.Fatalf("Done: %v", err)
	}
	mustBeGone(t, dir)
}

// TestUnbindRemovesTmp pins the unbind hook, with the same no-process
// isolation as the done test: without one, stopProcess already removed the
// directory before this route's hook ran.
func TestUnbindRemovesTmp(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentWithTmpRoot(t, fr)
	dir := roundTmpPath(rt, "webshop", 1)
	mustExist(t, dir)

	b.Builder = clearProcess(b.Builder)
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	if _, err := Unbind(context.Background(), rt, "webshop", false); err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	mustBeGone(t, dir)
}

// TestStopProcessRemovesTmp pins the kill hook: stop, timeout, silence and
// stall all stop a round's process through this one function, and the
// directory goes with the kill.
func TestStopProcessRemovesTmp(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentWithTmpRoot(t, fr)
	dir := roundTmpPath(rt, "webshop", 1)
	mustExist(t, dir)

	pid, err := stopProcess(context.Background(), rt, b, "test")
	if err != nil {
		t.Fatalf("stopProcess: %v", err)
	}
	if pid == 0 {
		t.Fatal("stopProcess addressed no process: the round must have one")
	}
	if len(fr.kills) != 1 {
		t.Fatalf("kills = %d, want exactly 1", len(fr.kills))
	}
	mustBeGone(t, dir)
}

// TestSwitchEmptiesTmp pins the mid-round switch's own kill route: it bypasses
// stopProcess, so it carries the removal itself, and the replacement's start
// recreates the directory under the same name -- empty.
func TestSwitchEmptiesTmp(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	rt.TmpRoot = t.TempDir()
	rt.Candidates = candidateSet(t, testTwoProviderJSON)
	rt.Policy = orderOf("builder", "agy/other/m", testClaudeRef, testOpencodeRef)
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "agy/other/m", MasterMindID: testMasterMindName, CWD: "/repo", Headless: true,
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	dir := roundTmpPath(rt, "webshop", 1)
	mustExist(t, dir)
	plant := filepath.Join(dir, "leftover")
	if err := os.WriteFile(plant, []byte("old builder\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := availability.Unavailable(AvailabilityDeps(rt), "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.BuilderCandidate != testClaudeRef {
		t.Fatalf("candidate = %q, want the gated switch to claude", got.BuilderCandidate)
	}
	if len(fr.kills) != 1 {
		t.Fatalf("kills = %d, want the switch's one kill", len(fr.kills))
	}

	if _, err := os.Stat(plant); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the switched-away builder's file %s survived the switch (stat err = %v)", plant, err)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("specs = %d, want the replacement's Start", len(fr.specs))
	}
	if fr.specs[1].TmpDir != dir {
		t.Errorf("replacement TmpDir = %q, want the recreated %q", fr.specs[1].TmpDir, dir)
	}
	entries, lerr := os.ReadDir(dir)
	if lerr != nil {
		t.Fatalf("read the recreated round dir: %v", lerr)
	}
	if len(entries) != 0 {
		t.Errorf("the switch left %d entries in %v, want an emptied dir", len(entries), entries)
	}
}

// TestRemoveRoundTmpFailureDoesNotFailClose pins the swallow: rounds/ is
// swapped for a file so the removal fails on every route, and the close still
// completes exactly as it would have.
func TestRemoveRoundTmpFailureDoesNotFailClose(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentWithTmpRoot(t, fr)

	rounds := filepath.Join(rt.TmpRoot, "rounds")
	if err := os.Rename(rounds, filepath.Join(rt.TmpRoot, "rounds-plant")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rounds, []byte("planted"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.DonePath("webshop", 1), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v, want the removal failure swallowed and the close done", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2: a swallowed cleanup still closes the round", got.Round)
	}
}

// TestSendRollbackRemovesTmp pins the rollback kill route: a send that
// started the round's process but could not record it stops the process and
// takes away the temp dir the start prepared. Deliberately not parallel,
// like the other test that injects through sendAfterSpawn.
func TestSendRollbackRemovesTmp(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)
	rt.TmpRoot = t.TempDir()

	old := sendAfterSpawn
	sendAfterSpawn = func(name string) error { return errors.New("injected") }
	t.Cleanup(func() { sendAfterSpawn = old })

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err == nil {
		t.Fatal("Send returned nil, want the injected failure")
	}
	if len(fr.kills) != 1 {
		t.Fatalf("kills = %d, want the rollback's kill", len(fr.kills))
	}
	mustBeGone(t, roundTmpPath(rt, "webshop", 1))
}

// TestRoundTmpDirReachesRealChild is the composed confirmation: the real
// runner starts a process with the directory prepareProc made as its TmpDir,
// and the child sees TMPDIR and GOTMPDIR equal to it -- sh prints its own
// environment, so no harness is involved.
func TestRoundTmpDirReachesRealChild(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.TmpRoot = t.TempDir()
	b := store.Binding{Name: "webshop", Round: 1}
	dir, _, _, err := prepareProc(context.Background(), rt, roundReq(&b))
	if err != nil {
		t.Fatalf("prepareProc: %v", err)
	}

	runner := proc.New()
	stream := filepath.Join(t.TempDir(), "real-child.jsonl")
	h, err := runner.Start(context.Background(), spawn.ProcSpec{
		Dir:        dir,
		Argv:       []string{"sh", "-c", "env"},
		TmpDir:     dir,
		LogPath:    stream,
		StreamPath: stream,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = runner.Kill(context.Background(), h, stream) })

	deadline := time.Now().Add(10 * time.Second)
	for {
		alive, aerr := runner.Alive(context.Background(), h)
		if aerr != nil {
			t.Fatalf("Alive: %v", aerr)
		}
		if !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the real child (pid %d) never exited", h.PID)
		}
		time.Sleep(5 * time.Millisecond)
	}
	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read the child's environment: %v", err)
	}
	child := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, name := range proc.TmpDirEnv {
		got, ok := lastEnvValue(child, name)
		if !ok || got != dir {
			t.Errorf("child %s = %q (found %v), want the round dir %q", name, got, ok, dir)
		}
	}
}

// TestPrepareErrorDoesNotGateCandidate pins the error's class: a tmp dir that
// cannot be made comes back plain -- nothing started, no spawnFailure wrapper
// for the relaunch path to answer, and the availability ledger untouched so
// the candidate is not gated for its host's fault.
func TestPrepareErrorDoesNotGateCandidate(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr)
	notADir := filepath.Join(t.TempDir(), "tmp-root")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rt.TmpRoot = notADir

	var got store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		got, err = startRound(context.Background(), rt, tx, b, "p", false)
		return err
	})
	if err == nil {
		t.Fatal("startRound = nil, want the prepare error")
	}
	var sf spawnFailure
	if errors.As(err, &sf) {
		t.Errorf("err = %v, want it not to be a spawnFailure", err)
	}
	if len(fr.specs) != 0 {
		t.Errorf("specs = %d, want no process started", len(fr.specs))
	}
	if got.Builder.PID != 0 {
		t.Errorf("a failed prepare left a pid: %+v", got.Builder)
	}
	for _, g := range availability.Gates(AvailabilityDeps(rt)) {
		if g.Token == testAgyRef && g.Kind == availability.SpawnFailed {
			t.Errorf("gate = %+v, want no spawn_failed for a host-side prepare error", g)
		}
	}
}
