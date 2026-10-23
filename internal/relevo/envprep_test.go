package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// lowDisk reports 1 GiB free everywhere, under the 5 GB default minimum.
func lowDisk(string) (uint64, error) { return 1 << 30, nil }

func diskFullText(path string) string {
	return "environment: disk full: " + path + " has 1.0 GB free (< 5 GB)"
}

func goTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func checkReq(tree string) procRequest {
	return procRequest{kind: procKindCheck, key: "unit-1", tree: tree}
}

func TestPrepareProcRefusesLowDisk(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.TmpRoot = t.TempDir()
	rt.DiskFree = lowDisk
	b := store.Binding{Name: "webshop", Round: 1}

	dir, _, _, err := prepareProc(context.Background(), rt, roundReq(&b))
	var envErr *EnvironmentError
	if !errors.As(err, &envErr) {
		t.Fatalf("err = %v, want an *EnvironmentError", err)
	}
	if dir != "" {
		t.Errorf("tmpDir = %q, want none", dir)
	}
	if _, serr := os.Stat(filepath.Join(rt.TmpRoot, "rounds")); !os.IsNotExist(serr) {
		t.Errorf("a refused start made a temp dir: %v", serr)
	}
	fr := rt.Runner.(*fakeRunner)
	if len(fr.specs) != 0 {
		t.Errorf("%d specs started, want none", len(fr.specs))
	}
}

func TestPrepareProcChecksWorktreeAndTmpRoot(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.TmpRoot = t.TempDir()
	var seen []string
	rt.DiskFree = func(p string) (uint64, error) {
		seen = append(seen, p)
		return 100 << 30, nil
	}
	b := store.Binding{Name: "webshop", Round: 1}

	if _, _, _, err := prepareProc(context.Background(), rt, roundReq(&b)); err != nil {
		t.Fatalf("prepareProc: %v", err)
	}
	if len(seen) != 2 || seen[0] != "/repo" || seen[1] != rt.TmpRoot {
		t.Errorf("checked %v, want the worktree then the tmp root", seen)
	}
}

func TestPrepareProcAddsTrimpathForGoTree(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	rt := newRuntime(t)
	tree := goTree(t)

	_, env, _, err := prepareProc(context.Background(), rt, checkReq(tree))
	if err != nil {
		t.Fatalf("prepareProc: %v", err)
	}
	if len(env) != 1 || env[0] != "GOFLAGS=-trimpath" {
		t.Errorf("env = %v, want GOFLAGS=-trimpath", env)
	}
	b := store.Binding{Name: "webshop", Round: 1}
	req := roundReq(&b)
	req.tree = tree
	_, env, _, err = prepareProc(context.Background(), rt, req)
	if err != nil || len(env) != 1 || env[0] != "GOFLAGS=-trimpath" {
		t.Errorf("round env = %v, err %v, want GOFLAGS=-trimpath", env, err)
	}
}

func TestPrepareProcNoTrimpathForNonGoTree(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	rt := newRuntime(t)

	_, env, _, err := prepareProc(context.Background(), rt, checkReq(t.TempDir()))
	if err != nil || env != nil {
		t.Errorf("env = %v, err %v, want none", env, err)
	}
}

func TestPrepareProcNoTrimpathWhenRepositoryOptedOut(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	rt := newRuntime(t)
	tree := goTree(t)
	rt.Git = &fakeGit{rootCommitSHA: reposRootCommit}
	st := auditStore(t)
	seedOneRepos(t, st, fmt.Sprintf(`{%q:{"name":"shop","trimpath":"off"}}`, reposKeyOf(t)))
	rt.Config = st

	_, env, _, err := prepareProc(context.Background(), rt, checkReq(tree))
	if err != nil || env != nil {
		t.Errorf("env = %v, err %v, want none for an opted-out repository", env, err)
	}
}

func TestPrepareProcAppendsToExistingGoFlags(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=vendor")
	rt := newRuntime(t)

	_, env, _, err := prepareProc(context.Background(), rt, checkReq(goTree(t)))
	if err != nil || len(env) != 1 || env[0] != "GOFLAGS=-mod=vendor -trimpath" {
		t.Errorf("env = %v, err %v, want the existing flags extended", env, err)
	}
}

func TestPrepareProcLeavesGoFlagsWithTrimpathAlone(t *testing.T) {
	t.Setenv("GOFLAGS", "-trimpath -mod=vendor")
	rt := newRuntime(t)

	_, env, _, err := prepareProc(context.Background(), rt, checkReq(goTree(t)))
	if err != nil || env != nil {
		t.Errorf("env = %v, err %v, want none", env, err)
	}
}

func TestSendHaltsWithEnvironmentReason(t *testing.T) {
	t.Parallel()

	rt, _ := seedHeadless(t, newFakeRunner())
	rt.DiskFree = lowDisk

	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"), SendOptions{})
	if err == nil {
		t.Fatal("Send succeeded on a full disk")
	}
	b, _ := rt.Store.Load("webshop")
	if want := diskFullText(roundTree(rt, b)); b.Halt != want {
		t.Errorf("Halt = %q, want %q", b.Halt, want)
	}
	if b.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", b.State)
	}
}

func TestLowDiskDoesNotRecordSpawnFailure(t *testing.T) {
	t.Parallel()

	rt, _ := seedHeadless(t, newFakeRunner())
	rt.DiskFree = lowDisk

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"), SendOptions{}); err == nil {
		t.Fatal("Send succeeded on a full disk")
	}
	for _, g := range availability.Gates(AvailabilityDeps(rt)) {
		if g.Token == testAgyRef {
			t.Errorf("candidate gated after a preflight refusal: %+v", g)
		}
	}
}

func TestChainHaltsWithEnvironmentReasonNotRedCheck(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startNoRegateFlowChain(t, rt)
	rt.DiskFree = lowDisk
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want a halt", c.Status, c.Reason)
	}
	if !strings.HasPrefix(c.Reason, "environment: disk full: ") || !strings.HasSuffix(c.Reason, " has 1.0 GB free (< 5 GB)") {
		t.Errorf("reason = %q, want the environment text", c.Reason)
	}
	if strings.Contains(c.Reason, "could not start") || strings.Contains(c.Reason, "last check") {
		t.Errorf("reason = %q reads as a red check", c.Reason)
	}
}

func TestServedCheckRecordsEnvironmentNote(t *testing.T) {
	t.Parallel()

	rt, b, fr := servedCheckEnv(t)
	rt.DiskFree = lowDisk

	_, _, err := ServedCheckStart(context.Background(), rt, "webshop", remote.CreateCheckRequest{ID: "c1", Command: "true"})
	if err != nil {
		t.Fatalf("ServedCheckStart: %v", err)
	}
	got, _ := rt.Store.Load("webshop")
	if got.CheckRun == nil || got.CheckRun.Result != "error" {
		t.Fatalf("CheckRun = %+v, want an errored run", got.CheckRun)
	}
	if want := diskFullText(roundTree(rt, b)); got.CheckRun.Note != want {
		t.Errorf("Note = %q, want %q", got.CheckRun.Note, want)
	}
	if len(fr.specs) != 0 {
		t.Errorf("%d specs started, want none", len(fr.specs))
	}
}

func TestSpawnHaltTextPlainErrorKeepsPrefix(t *testing.T) {
	t.Parallel()

	if got := spawnHaltText(errors.New("boom")); got != "builder spawn failed: boom" {
		t.Errorf("got %q", got)
	}
	env := &EnvironmentError{Reason: "disk full: /x has 1.0 GB free (< 5 GB)"}
	if got := spawnHaltText(env); got != env.Error() {
		t.Errorf("got %q, want %q", got, env.Error())
	}
}
