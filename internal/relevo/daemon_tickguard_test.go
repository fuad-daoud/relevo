package relevo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// quietDone seeds a DONE binding with no rounds on disk, no log and nothing
// owed: the shape an idle daemon leaves behind once a binding is finished.
func quietDone(t *testing.T, rt Runtime, name string) store.Binding {
	t.Helper()
	b := store.Binding{
		Name: name, CWD: "/repo", Round: 1, State: store.StateDone, RoundCap: 20,
		MasterMind: store.Endpoint{Kind: "claude"},
		Builder:    store.Endpoint{Kind: "agy", Mode: store.ModeHeadless},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
	return b
}

// TestIdleTickSkipsQuietDoneBindings pins the plan's read count: after a
// binding's first full pass, an idle tick decodes a quiet done binding's log
// no at all, while an active binding -- never guarded -- still reads its own.
// Remove the skip and the done bindings' log reads return, one each, and this
// test fails.
func TestIdleTickSkipsQuietDoneBindings(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	quietDone(t, rt, "donea")
	quietDone(t, rt, "doneb")
	beforeA, err := rt.Store.Load("donea")
	if err != nil {
		t.Fatalf("Load donea: %v", err)
	}
	actv := store.Binding{
		Name: "actv", CWD: "/repo", Round: 1, State: store.StateActive, RoundCap: 20,
		MasterMind: store.Endpoint{Kind: "claude"},
		Builder:    store.Endpoint{Kind: "agy", Mode: store.ModeHeadless},
	}
	if err := rt.Store.Save(actv); err != nil {
		t.Fatalf("save actv: %v", err)
	}

	d := NewDaemon(rt, time.Second)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("arm Tick: %v", err)
	}
	if d.tickPassed["donea"] == "" || d.tickPassed["doneb"] == "" {
		t.Fatal("the first tick must pin each done binding's revision")
	}
	if _, ok := d.tickPassed["actv"]; ok {
		t.Error("an active binding is never guarded: it must not enter the map")
	}

	// The first pass of a fresh binding converges once: it removes the empty
	// binding directory a DONE binding leaves behind, which moves the
	// revision the pin sits at. The pin stands from the pass that finds
	// nothing left to change.
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("converge Tick: %v", err)
	}
	rev, err := rt.Store.Revision("donea")
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	if d.tickPassed["donea"] != rev {
		t.Fatalf("the pin after convergence = %q, revision %q, want the pass to pin the current revision", d.tickPassed["donea"], rev)
	}

	reads := map[string]int{}
	rt.Store.SetReadLogHook(func(name string) { reads[name]++ })
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("idle Tick: %v", err)
	}
	if reads["donea"] != 0 || reads["doneb"] != 0 {
		t.Errorf("quiet done bindings decoded logs %d and %d times, want no read at all", reads["donea"], reads["doneb"])
	}
	if reads["actv"] == 0 {
		t.Error("the active binding must still run its full path every tick")
	}

	afterA, err := rt.Store.Load("donea")
	if err != nil {
		t.Fatalf("Load donea after: %v", err)
	}
	if !store.SameBinding(beforeA, afterA) {
		t.Error("a skipped binding must be left byte-identical")
	}
}

// TestIdleTickSettlesAnAdmittedPayload pins the amendment's first case: a done
// binding with an admitted, unconfirmed payload still reaches ConfirmAdmitted
// on an otherwise idle tick. The admit patches the log entry in place, so the
// revision the skip rests on does not move under it; drop the AdmitsOpen
// check and the tick skips, the orphaned admit never clears, and this test
// fails.
func TestIdleTickSettlesAnAdmittedPayload(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	quietDone(t, rt, "admitd")
	if err := rt.Store.AppendLog("admitd", store.LogEntry{
		TS: baseTime, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Payload: "the report",
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	d := NewDaemon(rt, time.Second)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("arm Tick: %v", err)
	}
	// Converge before admitting: the first pass removes the empty binding
	// directory a DONE binding leaves behind, which moves the revision the
	// pin sits at. The pin must already stand at the current revision, or a
	// later mismatch -- not the live admit count -- would force the pass.
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("converge Tick: %v", err)
	}

	// A push route admits the entry while the daemon stays idle: nothing the
	// revision reads moves, and no other route can take the entry again.
	if err := rt.Store.AdmitIndex("admitd", 0); err != nil {
		t.Fatalf("AdmitIndex: %v", err)
	}

	reads := map[string]int{}
	rt.Store.SetReadLogHook(func(name string) { reads[name]++ })
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	if reads["admitd"] == 0 {
		t.Error("the idle tick skipped a done binding with an admitted payload owed its settle")
	}
	// The dead holder's admit is cleared, back into the claimable queue.
	entries, err := rt.Store.ReadLog("admitd")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(entries) != 1 || entries[0].AdmittedAt != nil {
		t.Errorf("entries = %+v, want the admitted entry cleared back to claimable", entries)
	}

	// Once nothing is owed, the skip re-engages: the clear, like the admit,
	// moved no revision the guard reads.
	reads2 := map[string]int{}
	rt.Store.SetReadLogHook(func(name string) { reads2[name]++ })
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("third Tick: %v", err)
	}
	if reads2["admitd"] != 0 {
		t.Errorf("a settled done binding decoded its log %d times, want the skip back", reads2["admitd"])
	}
}

// TestDeadBuilderWithStillRevisionIsStillReconciled pins the amendment's
// second case: an ACTIVE binding whose builder died -- a death that writes
// nothing the revision reads -- is reconciled on the next tick to the same
// result a direct reconcile gives. Apply the guard to active bindings and the
// second tick skips, the halt never lands, and this test fails.
func TestDeadBuilderWithStillRevisionIsStillReconciled(t *testing.T) {
	t.Parallel()

	seed := func(t *testing.T) (Runtime, *fakeRunner) {
		t.Helper()
		rt := newRuntime(t)
		b := store.Binding{
			Name: "died", CWD: "/repo", Round: 1, State: store.StateActive, RoundCap: 20,
			MasterMind: store.Endpoint{Kind: "claude"},
			Builder:    store.Endpoint{Kind: "agy", Mode: store.ModeHeadless, PID: 4712, StartedAt: 1_700_000_000},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("save: %v", err)
		}
		if err := rt.Store.AppendLog("died", store.LogEntry{
			TS: baseTime, Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt,
		}); err != nil {
			t.Fatalf("AppendLog prompt: %v", err)
		}
		fr := rt.Runner.(*fakeRunner)
		fr.script(4712, true) // alive until the test kills it
		return rt, fr
	}

	rtA, frA := seed(t)
	d := NewDaemon(rtA, time.Second)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("alive Tick: %v", err)
	}
	if got := frA.aliveCalls; got == 0 {
		t.Fatalf("alive Tick made no liveness probe, want the full path")
	}
	// The builder dies without a report. Its death is a stopped process, not
	// a store write: the revision it left stands.
	frA.script(4712, false)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("death Tick: %v", err)
	}
	gotA, err := rtA.Store.Load("died")
	if err != nil {
		t.Fatalf("Load after death: %v", err)
	}
	if gotA.State != store.StateNeedsYou {
		t.Errorf("state after the dead builder's tick = %s, want today's halt (needs_you): the guard must never reach active bindings", gotA.State)
	}

	rtB, frB := seed(t)
	frB.script(4712, false)
	bB, err := rtB.Store.Load("died")
	if err != nil {
		t.Fatalf("Load b: %v", err)
	}
	outB, err := reconcile(t, rtB, bB)
	if err != nil {
		t.Fatalf("direct Reconcile: %v", err)
	}
	if err := rtB.Store.Save(outB); err != nil {
		t.Fatalf("Save: %v", err)
	}
	gotB, err := rtB.Store.Load("died")
	if err != nil {
		t.Fatalf("Load after direct reconcile: %v", err)
	}
	if !sameReconcileResult(gotA, gotB) {
		t.Errorf("the dead builder reconciled differently under the daemon: %+v vs %+v", gotA, gotB)
	}
	if gotB.State != store.StateNeedsYou {
		t.Fatalf("the direct reconcile of the dead builder left state %s, want today's halt", gotB.State)
	}
	entriesA, err := rtA.Store.ReadLog("died")
	if err != nil {
		t.Fatalf("ReadLog A: %v", err)
	}
	entriesB, err := rtB.Store.ReadLog("died")
	if err != nil {
		t.Fatalf("ReadLog B: %v", err)
	}
	if len(entriesA) != len(entriesB) || len(entriesA) < 2 {
		t.Fatalf("log lengths = %d and %d, want the same advance of the open round", len(entriesA), len(entriesB))
	}
	for i := range entriesA {
		a, b := entriesA[i], entriesB[i]
		if a.Kind != b.Kind || a.Round != b.Round || a.Direction != b.Direction || a.Note != b.Note || !a.TS.Equal(b.TS) {
			t.Errorf("entry %d differs: %+v vs %+v", i, a, b)
		}
	}
	if entriesA[len(entriesA)-1].Kind != store.KindHalt {
		t.Errorf("last entry kind = %s, want the halt the dead builder owes", entriesA[len(entriesA)-1].Kind)
	}
}

// sameReconcileResult compares the two paths' verdicts on one binding. The
// stores stamp CreatedAt and UpdatedAt from the wall clock at save time, so
// those -- not the verdict -- are what differ between two fixtures.
func sameReconcileResult(a, b store.Binding) bool {
	a.CreatedAt, a.UpdatedAt = time.Time{}, time.Time{}
	b.CreatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	return store.SameBinding(a, b)
}

// TestDoneTickSkipRefusesEveryOtherBinding pins the predicate itself: only a
// done binding, past its first full pass, with no owed halt, no open consult,
// no admitted payload and no unsealed round, and a readable revision that
// matches, skips.
func TestDoneTickSkipRefusesEveryOtherBinding(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	d := NewDaemon(rt, time.Hour)

	load := func(name string) store.Binding {
		t.Helper()
		b, err := rt.Store.Load(name)
		if err != nil {
			t.Fatalf("Load %s: %v", name, err)
		}
		return b
	}
	// wantRun is the answer for a binding the tick must reconcile in full.
	run := func(name string) {
		t.Helper()
		rev, skip := d.doneTickSkip(load(name))
		if skip {
			t.Errorf("%s: doneTickSkip skipped, want the full path", name)
		}
		if rev != "" && d.tickPassed[name] == rev {
			t.Errorf("%s: revision %q already recorded, want a run", name, rev)
		}
	}

	for _, st := range []store.State{store.StateActive, store.StatePaused, store.StateNeedsYou, store.StateBroken} {
		if err := rt.Store.Save(store.Binding{Name: "st-" + string(st), CWD: "/repo/" + string(st), Round: 1, State: st, RoundCap: 20,
			MasterMind: store.Endpoint{Kind: "claude"}, Builder: store.Endpoint{Kind: "agy", Mode: store.ModeHeadless}}); err != nil {
			t.Fatalf("save %s: %v", st, err)
		}
		d.tickPassed["st-"+string(st)] = "any" // even a matching revision must not guard them
		run("st-" + string(st))
	}

	quietDone(t, rt, "first")
	firstRev, skip := d.doneTickSkip(load("first"))
	if skip || firstRev == "" {
		t.Fatalf("quiet done with no recorded pass: (rev %q, skip %v), want the pass and its revision", firstRev, skip)
	}
	d.tickPassed["first"] = firstRev
	if rev, skip := d.doneTickSkip(load("first")); !skip || rev != firstRev {
		t.Errorf("quiet done at its recorded revision: (rev %q, skip %v), want the skip", rev, skip)
	}

	if err := rt.Store.Save(store.Binding{Name: "owed", CWD: "/repo", Round: 1, State: store.StateDone, RoundCap: 20,
		MasterMind: store.Endpoint{Kind: "claude"}, Builder: store.Endpoint{Kind: "agy", Mode: store.ModeHeadless},
		OwedHalt: &store.OwedHalt{Round: 1, Text: "the owed halt"}}); err != nil {
		t.Fatalf("save owed: %v", err)
	}
	owedRev, err := rt.Store.Revision("owed")
	if err != nil {
		t.Fatalf("Revision owed: %v", err)
	}
	d.tickPassed["owed"] = owedRev
	if rev, skip := d.doneTickSkip(load("owed")); skip || rev != "" {
		t.Errorf("owed halt: (rev %q, skip %v), want the full path before any read", rev, skip)
	}

	if err := rt.Store.Save(store.Binding{Name: "consult", CWD: "/repo", Round: 1, State: store.StateDone, RoundCap: 20,
		MasterMind: store.Endpoint{Kind: "claude"}, Builder: store.Endpoint{Kind: "agy", Mode: store.ModeHeadless},
		Consults: []store.Consult{{ID: "abcd1234", Round: 1, State: store.ConsultRunning}}}); err != nil {
		t.Fatalf("save consult: %v", err)
	}
	consultRev, err := rt.Store.Revision("consult")
	if err != nil {
		t.Fatalf("Revision consult: %v", err)
	}
	d.tickPassed["consult"] = consultRev
	if rev, skip := d.doneTickSkip(load("consult")); skip || rev != "" {
		t.Errorf("open consult: (rev %q, skip %v), want the full path before any read", rev, skip)
	}

	quietDone(t, rt, "unsealed")
	if err := os.MkdirAll(rt.Store.Dir("unsealed"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rt.Store.Dir("unsealed"), "001-report.md"), []byte("r"), 0o644); err != nil {
		t.Fatalf("write round file: %v", err)
	}
	unsealedRev, err := rt.Store.Revision("unsealed")
	if err != nil {
		t.Fatalf("Revision unsealed: %v", err)
	}
	d.tickPassed["unsealed"] = unsealedRev
	if rev, skip := d.doneTickSkip(load("unsealed")); skip || rev == "" {
		t.Errorf("unsealed round: (rev %q, skip %v), want the full path: the seal may still be waiting on a clock or the cap", rev, skip)
	}

	quietDone(t, rt, "openadmit")
	if err := rt.Store.AppendLog("openadmit", store.LogEntry{
		TS: baseTime, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Payload: "p",
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	admitRev, err := rt.Store.Revision("openadmit")
	if err != nil {
		t.Fatalf("Revision openadmit: %v", err)
	}
	if err := rt.Store.AdmitIndex("openadmit", 0); err != nil {
		t.Fatalf("AdmitIndex: %v", err)
	}
	d.tickPassed["openadmit"] = admitRev
	if rev, skip := d.doneTickSkip(load("openadmit")); skip || rev != admitRev {
		t.Errorf("admitted payload: (rev %q, skip %v), want the full path on the live count alone", rev, skip)
	}

	if rev, skip := d.doneTickSkip(store.Binding{Name: "ghost", CWD: "/repo", Round: 1, State: store.StateDone, RoundCap: 20,
		MasterMind: store.Endpoint{Kind: "claude"}, Builder: store.Endpoint{Kind: "agy", Mode: store.ModeHeadless}}); skip || rev != "" {
		t.Errorf("unreadable revision: (rev %q, skip %v), want the full path", rev, skip)
	}
}

// TestTickPassedPrunesWithTheBindingList pins that an unbound name leaves the
// map with the tick's list, so no re-added name inherits a revision it never
// passed.
func TestTickPassedPrunesWithTheBindingList(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	d := NewDaemon(rt, time.Hour)
	d.tickPassed["gone"] = "rev-gone"
	d.tickPassed["kept"] = "rev-kept"

	d.pruneTickPassed([]store.Binding{{Name: "kept"}})

	if _, ok := d.tickPassed["gone"]; ok {
		t.Error("a binding that left the list kept its recorded revision")
	}
	if d.tickPassed["kept"] != "rev-kept" {
		t.Error("a listed binding lost its recorded revision")
	}
}
