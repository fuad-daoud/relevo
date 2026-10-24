package relevo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// The tests here drive the check half of the prepare seam through the gate
// runner's two functions: every gate and check process starts with its own
// temp directory under checks/<unit>, and that directory ends with the run
// -- pass, fail, timeout, error, or a start that never happened.

// checkTmpPathFor is the directory prepareProc makes for one check unit.
func checkTmpPathFor(rt Runtime, unit string) string {
	return filepath.Join(rt.TmpRoot, checkTmpRel(unit))
}

// gateProcFixture is a runtime that names a tmp root and starts through fr,
// plus one check spec carrying a gate scope unit: the pieces every test here
// drives the gate runner's seams with.
func gateProcFixture(t *testing.T, fr *fakeRunner) (Runtime, gateProcSpec) {
	t.Helper()
	rt := newRuntime(t)
	rt.Runner = fr
	rt.TmpRoot = t.TempDir()
	return rt, gateProcSpec{
		Dir:      t.TempDir(),
		Command:  "make check",
		LogPath:  filepath.Join(t.TempDir(), "001-gate.log"),
		UnitName: scopeUnitNameFor(scopeGate, "", "webshop", 1, ""),
		Timeout:  10 * time.Minute,
	}
}

// startedCheckRun is gateProcFixture plus one cleanly started run, with the
// clock one second into the run so no advance below reads it as timed out.
func startedCheckRun(t *testing.T, fr *fakeRunner) (Runtime, gateProcSpec, store.GateRun) {
	t.Helper()
	rt, spec := gateProcFixture(t, fr)
	run, rec, err := startGateProc(context.Background(), rt, spec, 0)
	if rec != nil || err != nil {
		t.Fatalf("startGateProc: rec = %+v, err = %v, want a clean start", rec, err)
	}
	rt.Now = func() time.Time { return time.Unix(run.StartedAt, 0).Add(time.Second) }
	return rt, spec, run
}

func TestStartGateProcSetsCheckTmpDir(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, spec := gateProcFixture(t, fr)

	run, rec, err := startGateProc(context.Background(), rt, spec, 0)
	if rec != nil || err != nil {
		t.Fatalf("startGateProc: rec = %+v, err = %v, want a clean start", rec, err)
	}
	if run.PID == 0 {
		t.Fatal("run.PID = 0, want the started handle")
	}
	want := checkTmpPathFor(rt, spec.UnitName)
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want the one started process", len(fr.specs))
	}
	if fr.specs[0].TmpDir != want {
		t.Errorf("spec TmpDir = %q, want %q", fr.specs[0].TmpDir, want)
	}
	mustExist(t, want)
}

// TestStartGateProcFailedStartRemovesTmp pins the failed start's own undoing:
// no advance will ever run for a process that never existed, so the directory
// the preparation made goes back before the error record comes back.
func TestStartGateProcFailedStartRemovesTmp(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, spec := gateProcFixture(t, fr)
	fr.startErr = errors.New("injected start failure")

	_, rec, err := startGateProc(context.Background(), rt, spec, 0)
	if err != nil {
		t.Errorf("err = %v, want nil: the record carries the failure", err)
	}
	if rec == nil || rec.Result != "error" {
		t.Fatalf("rec = %+v, want an error record from the failed start", rec)
	}
	if len(fr.specs) != 0 {
		t.Errorf("specs = %d, want none: Start errored", len(fr.specs))
	}
	mustBeGone(t, checkTmpPathFor(rt, spec.UnitName))
}

func TestAdvanceGateProcRemovesTmpOnPass(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, spec, run := startedCheckRun(t, fr)
	fr.script(run.PID, false)
	fr.exit(run.PID, 0)

	next, done, rec, err := advanceGateProc(context.Background(), rt, run, spec)
	if err != nil {
		t.Fatalf("advanceGateProc: %v", err)
	}
	if next != nil || !done {
		t.Fatalf("next = %+v, done = %v, want the pass to end the run", next, done)
	}
	if rec == nil || rec.Result != "pass" {
		t.Fatalf("rec = %+v, want Result \"pass\"", rec)
	}
	mustBeGone(t, checkTmpPathFor(rt, spec.UnitName))
}

func TestAdvanceGateProcRemovesTmpOnFail(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, spec, run := startedCheckRun(t, fr)
	fr.script(run.PID, false)
	fr.exit(run.PID, 1)

	next, done, rec, err := advanceGateProc(context.Background(), rt, run, spec)
	if err != nil {
		t.Fatalf("advanceGateProc: %v", err)
	}
	if next != nil || !done {
		t.Fatalf("next = %+v, done = %v, want the fail to end the run", next, done)
	}
	if rec == nil || rec.Result != "fail" || rec.ExitCode != 1 {
		t.Fatalf("rec = %+v, want Result \"fail\" at exit 1", rec)
	}
	mustBeGone(t, checkTmpPathFor(rt, spec.UnitName))
}

// TestAdvanceGateProcRemovesTmpOnTimeout pins the kill branch: the run that
// outlived its timeout ends on this tick and no other route will ever settle
// it, so the directory goes with the kill -- the fake's kill lands, and the
// removal must not wait on a close that never comes.
func TestAdvanceGateProcRemovesTmpOnTimeout(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, spec := gateProcFixture(t, fr)
	run, rec, err := startGateProc(context.Background(), rt, spec, 0)
	if rec != nil || err != nil {
		t.Fatalf("startGateProc: rec = %+v, err = %v, want a clean start", rec, err)
	}
	rt.Now = func() time.Time { return time.Unix(run.StartedAt, 0).Add(spec.Timeout) }

	next, done, rec, err := advanceGateProc(context.Background(), rt, run, spec)
	if err != nil {
		t.Fatalf("advanceGateProc: %v", err)
	}
	if next != nil || !done {
		t.Fatalf("next = %+v, done = %v, want the timeout to end the run", next, done)
	}
	if rec == nil || rec.Result != "timeout" {
		t.Fatalf("rec = %+v, want Result \"timeout\"", rec)
	}
	if len(fr.kills) != 1 {
		t.Fatalf("kills = %d, want the timeout kill", len(fr.kills))
	}
	mustBeGone(t, checkTmpPathFor(rt, spec.UnitName))
}

// TestAdvanceGateProcRemovesTmpOnError pins the error branch: a process this
// daemon cannot read an exit code from -- a loss it is not allowed to re-run
// -- still ends the run, and its directory ends with it.
func TestAdvanceGateProcRemovesTmpOnError(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, spec, run := startedCheckRun(t, fr)
	fr.script(run.PID, false) // exited with no trailer; exit() unset: !ok

	next, done, rec, err := advanceGateProc(context.Background(), rt, run, spec)
	if err != nil {
		t.Fatalf("advanceGateProc: %v", err)
	}
	if next != nil || !done {
		t.Fatalf("next = %+v, done = %v, want the error to end the run", next, done)
	}
	if rec == nil || rec.Result != "error" {
		t.Fatalf("rec = %+v, want Result \"error\"", rec)
	}
	mustBeGone(t, checkTmpPathFor(rt, spec.UnitName))
}

// TestAdvanceGateProcKeepsTmpAcrossRestartRerun pins the re-run's reuse: the
// directory is keyed on the unit name, not the process, so the restart-loss
// re-run starts on the same path and finds what the first run left there.
func TestAdvanceGateProcKeepsTmpAcrossRestartRerun(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, spec := gateProcFixture(t, fr)
	rt.StartedAt = baseTime
	rt.Watched = NewWatched()

	// The first run's start made the directory through this same seam, and
	// the command had begun writing in it before the daemon restart took the
	// process down without a trailer.
	first, _, _, err := prepareProc(context.Background(), rt, procRequest{
		kind: procKindCheck, key: spec.UnitName, tree: spec.Dir,
	})
	if err != nil {
		t.Fatalf("prepareProc: %v", err)
	}
	plant := filepath.Join(first, "partial.out")
	if err := os.WriteFile(plant, []byte("first run\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	lost := store.GateRun{
		PID:       9001,
		StartedAt: baseTime.Add(-time.Minute).Unix(),
		Command:   spec.Command,
		Attempt:   0,
	}
	fr.script(9001, false) // exited with no trailer

	next, done, rec, err := advanceGateProc(context.Background(), rt, lost, spec)
	if err != nil {
		t.Fatalf("advanceGateProc: %v", err)
	}
	if next == nil || done || rec != nil {
		t.Fatalf("next = %+v, done = %v, rec = %+v, want the re-run", next, done, rec)
	}
	if next.Attempt != 1 {
		t.Errorf("re-run Attempt = %d, want 1", next.Attempt)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want the re-run's Start", len(fr.specs))
	}
	if fr.specs[0].TmpDir != first {
		t.Errorf("re-run TmpDir = %q, want the first run's dir %q", fr.specs[0].TmpDir, first)
	}
	if _, serr := os.Stat(plant); serr != nil {
		t.Errorf("the restart re-run disturbed the check dir: %v", serr)
	}
}

// TestGateStepCheckTmpDirReachesSpec pins the binding's round gate on its
// real route: the spec gateStep hands the runner carries the checks/<unit>
// directory, which is what the proc layer turns into the child's TMPDIR and
// GOTMPDIR.
func TestGateStepCheckTmpDirReachesSpec(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	rt.TmpRoot = t.TempDir()
	b.Gate = "make check"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		_, _, _, err := gateStep(context.Background(), rt, tx, b)
		return err
	}); err != nil {
		t.Fatalf("gateStep: %v", err)
	}

	unit := scopeUnitNameFor(scopeGate, b.Owner, b.Name, b.Round, "")
	want := checkTmpPathFor(rt, unit)
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want the one gate Start", len(fr.specs))
	}
	if fr.specs[0].TmpDir != want {
		t.Errorf("gate spec TmpDir = %q, want %q", fr.specs[0].TmpDir, want)
	}
	mustExist(t, want)
}

// TestServedCheckTmpDirReachesSpec pins a served check on its real route:
// the process the server starts for the client's acceptance command carries
// its own temp dir, named by the check's gate scope unit.
func TestServedCheckTmpDirReachesSpec(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	rt.TmpRoot = t.TempDir()

	run, err := startServedCheck(context.Background(), rt, b, remote.CreateCheckRequest{ID: "c1", Command: "make check"})
	if err != nil {
		t.Fatalf("startServedCheck: %v", err)
	}
	if run == nil || run.PID == 0 {
		t.Fatalf("run = %+v, want the started check", run)
	}

	unit := scopeUnitNameFor(scopeGate, b.Owner, b.Name+"-check", b.Round, "")
	want := checkTmpPathFor(rt, unit)
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want the one check Start", len(fr.specs))
	}
	if fr.specs[0].TmpDir != want {
		t.Errorf("check spec TmpDir = %q, want %q", fr.specs[0].TmpDir, want)
	}
	mustExist(t, want)
}

// TestChainCheckTmpDirReachesSpec pins a chain check's own spec route: the
// unit chainCheckSpec builds for the run names the temp dir the run starts
// with, so two runs of one chain's check never share a directory.
func TestChainCheckTmpDirReachesSpec(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	rt.TmpRoot = t.TempDir()
	c := db.ChainRow{Name: "shop", Worktree: t.TempDir()}
	spec := chainCheckSpec(rt, c, 1, "make check", filepath.Join(t.TempDir(), "check-001.log"))

	if _, rec, err := startGateProc(context.Background(), rt, spec, 0); rec != nil || err != nil {
		t.Fatalf("startGateProc: rec = %+v, err = %v, want a clean start", rec, err)
	}

	unit := scopeUnitNameFor(scopeGate, c.Owner, c.Name+"-check", 1, "")
	want := checkTmpPathFor(rt, unit)
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want the one check Start", len(fr.specs))
	}
	if fr.specs[0].TmpDir != want {
		t.Errorf("check spec TmpDir = %q, want %q", fr.specs[0].TmpDir, want)
	}
	mustExist(t, want)
}
