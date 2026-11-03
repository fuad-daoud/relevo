package relevo

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// seedSettleChain seeds a legacy chain row in status with one ACTIVE member
// per name, mapped onto the part columns in order, so the end verbs' walk
// finds them exactly as the statusline's read does.
func seedSettleChain(t *testing.T, rt Runtime, name, status, mastermindID string, members ...string) db.ChainRow {
	t.Helper()
	now := rt.Now().UTC()
	row := db.ChainRow{
		ID:           db.NewID(),
		Name:         name,
		Status:       status,
		Phase:        "build",
		Step:         "reviewing",
		Plan:         1,
		Plans:        1,
		SettingsJSON: []byte(`{"max_corrections":1}`),
		MasterMindID: mastermindID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	parts := []*string{&row.Builder, &row.Reviewer, &row.Planner, &row.Security}
	bindings := make([]store.Binding, 0, len(members))
	for i, m := range members {
		if i < len(parts) {
			*parts[i] = m
		}
		bindings = append(bindings, store.Binding{
			Name: m, CWD: "/repo/" + m, Round: 1, State: store.StateActive,
			MasterMindID: mastermindID,
		})
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.CreateChain(row, bindings) }); err != nil {
		t.Fatalf("seed chain %s: %v", name, err)
	}
	return row
}

func memberSet(members []string) map[string]bool {
	set := make(map[string]bool, len(members))
	for _, m := range members {
		set[m] = true
	}
	return set
}

// TestDoneChainMembersSelectsOnlyInScopeDoneChains pins the settle set: four
// chains covering every non-gone status, across two masterminds, and only the
// in-scope done chain's members come back. Dropping the status filter lets
// the running, halted and stopped members in.
func TestDoneChainMembersSelectsOnlyInScopeDoneChains(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	seedSettleChain(t, rt, "ch-done", "done", testMasterMindID, "ch-done", "ch-done-plan")
	seedSettleChain(t, rt, "ch-run", "running", testMasterMindID, "ch-run", "ch-run-plan")
	seedSettleChain(t, rt, "ch-halt", "halted", "pl_otherbbbbbb", "ch-halt", "ch-halt-plan")
	seedSettleChain(t, rt, "ch-stop", "stopped", testMasterMindID, "ch-stop", "ch-stop-plan")

	chains, err := rt.Store.Chains()
	if err != nil {
		t.Fatalf("Chains: %v", err)
	}

	got := memberSet(doneChainMembers(housekeep.GCOptions{MasterMindID: testMasterMindID}, chains, rt.Store))
	want := map[string]bool{"ch-done": true, "ch-done-plan": true}
	if len(got) != len(want) {
		t.Fatalf("in-scope members = %v, want %v", got, want)
	}
	for m := range want {
		if !got[m] {
			t.Errorf("in-scope members = %v, missing %q", got, m)
		}
	}

	if got := doneChainMembers(housekeep.GCOptions{AllMasterMinds: true}, chains, rt.Store); len(got) != 2 {
		t.Errorf("all-masterminds members = %v, want only the done chain's two", got)
	}
	if got := doneChainMembers(housekeep.GCOptions{MasterMindID: "pl_otherbbbbbb"}, chains, rt.Store); len(got) != 0 {
		t.Errorf("other mastermind members = %v, want none: its chain is halted", got)
	}
}

// TestGCDoneChainsRefusesScopeLessOpts pins the scope guard: the sweep
// refuses a missing or a doubled scope before reading the store, so neither
// call settles or clears anything and the seeded chain survives untouched.
func TestGCDoneChainsRefusesScopeLessOpts(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	seedSettleChain(t, rt, "ch-scope", "done", testMasterMindID, "ch-scope", "ch-scope-plan")

	beforeBindings, err := rt.Store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	beforeChain, err := rt.Store.Chain("ch-scope")
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}

	if _, err := GCDoneChains(context.Background(), rt, housekeep.GCOptions{}); !errors.Is(err, housekeep.ErrGCNoScope) {
		t.Fatalf("GCDoneChains without scope: err = %v, want ErrGCNoScope", err)
	}
	if _, err := GCDoneChains(context.Background(), rt,
		housekeep.GCOptions{MasterMindID: testMasterMindID, AllMasterMinds: true}); !errors.Is(err, housekeep.ErrGCNoScope) {
		t.Fatalf("GCDoneChains with both scopes: err = %v, want ErrGCNoScope", err)
	}

	afterBindings, err := rt.Store.List()
	if err != nil {
		t.Fatalf("List after: %v", err)
	}
	if !reflect.DeepEqual(afterBindings, beforeBindings) {
		t.Errorf("bindings after scope refusals differ: before %+v, after %+v", beforeBindings, afterBindings)
	}
	afterChain, err := rt.Store.Chain("ch-scope")
	if err != nil {
		t.Fatalf("Chain after: %v", err)
	}
	if !reflect.DeepEqual(afterChain, beforeChain) {
		t.Errorf("chain after scope refusals = %+v, want %+v", afterChain, beforeChain)
	}
}

// TestDoneChainMembersFollowsWorkflowRows pins the workflow read: a row
// that carries a workflow names its engine-recorded members, not the legacy
// part columns, so the sweep settles what the end verbs would release.
func TestDoneChainMembersFollowsWorkflowRows(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	now := rt.Now().UTC()
	row := db.ChainRow{
		ID:           db.NewID(),
		Name:         "ch-wf",
		Status:       "done",
		Phase:        "build",
		Step:         "reviewing",
		Plan:         1,
		Plans:        1,
		SettingsJSON: []byte(`{"max_corrections":1}`),
		Builder:      "engine-a",
		Planner:      "engine-b",
		WorkflowJSON: []byte(`{}`),
		MasterMindID: testMasterMindID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	members := []store.Binding{
		{Name: "engine-a", CWD: "/repo/engine-a", Round: 1, State: store.StateActive, MasterMindID: testMasterMindID},
		{Name: "engine-b", CWD: "/repo/engine-b", Round: 1, State: store.StateActive, MasterMindID: testMasterMindID},
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.CreateChain(row, members) }); err != nil {
		t.Fatalf("seed the workflow chain: %v", err)
	}
	// Diverge the legacy columns from the engine-recorded members: the
	// chain_member rows stay keyed by chain ID, so only the row changes.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		row.Builder = "legacy-a"
		row.Planner = "legacy-b"
		return tx.ChainPut(row)
	}); err != nil {
		t.Fatalf("diverge the legacy columns: %v", err)
	}

	chains, err := rt.Store.Chains()
	if err != nil {
		t.Fatalf("Chains: %v", err)
	}

	got := memberSet(doneChainMembers(housekeep.GCOptions{MasterMindID: testMasterMindID}, chains, rt.Store))
	want := map[string]bool{"engine-a": true, "engine-b": true}
	if len(got) != len(want) {
		t.Fatalf("workflow members = %v, want %v", got, want)
	}
	for m := range want {
		if !got[m] {
			t.Errorf("workflow members = %v, missing %q", got, m)
		}
	}
	for _, m := range []string{"legacy-a", "legacy-b"} {
		if got[m] {
			t.Errorf("workflow members = %v, want no legacy column %q", got, m)
		}
	}
}

// TestGCDoneChainsSettlesADoneChainThenClears pins the sweep: a done chain
// with two ACTIVE members and no worktrees ends with both members archived
// and the chain row still done. Without the settle the members survive, since
// a GC alone never touches a live binding.
func TestGCDoneChainsSettlesADoneChainThenClears(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	seedSettleChain(t, rt, "ch-settle", "done", testMasterMindID, "ch-settle", "ch-settle-plan")

	done, err := GCDoneChains(context.Background(), rt, housekeep.GCOptions{MasterMindID: testMasterMindID})
	if err != nil {
		t.Fatalf("GCDoneChains: %v", err)
	}
	if len(done) != 2 {
		t.Fatalf("GC results = %+v, want the two settled members", done)
	}
	for _, m := range []string{"ch-settle", "ch-settle-plan"} {
		if _, err := rt.Store.Load(m); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Load %s after sweep = %v, want the record archived away", m, err)
		}
	}
	row, err := rt.Store.Chain("ch-settle")
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	if row.Status != "done" {
		t.Errorf("chain status = %q, want done", row.Status)
	}
}

// TestGCDoneChainsSkipsAChainThatFailsToSettle pins the sweep's contract when
// one chain cannot be closed (its server answers 500): that chain is named in
// the returned error and its members stay ACTIVE, while every other done
// chain still settles and the trailing GC still clears what is done.
func TestGCDoneChainsSkipsAChainThatFailsToSettle(t *testing.T) {
	t.Parallel()

	fr := &fakeRemote{doneErr: &client.HTTPError{Status: 500, Body: remote.ErrorBody{Code: "boom"}}}
	rt := newRuntime(t)
	rt.Remote = fr
	seedSettleChain(t, rt, "ch-aa-ok", "done", testMasterMindID, "ch-aa-ok", "ch-aa-ok-plan")

	now := rt.Now().UTC()
	bad := db.ChainRow{
		ID:           db.NewID(),
		Name:         "ch-zz-bad",
		Status:       "done",
		Phase:        "build",
		Step:         "reviewing",
		Plan:         1,
		Plans:        1,
		SettingsJSON: []byte(`{"max_corrections":1}`),
		Builder:      "ch-zz-bad",
		Planner:      "ch-zz-bad-plan",
		MasterMindID: testMasterMindID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	badMembers := []store.Binding{
		{Name: "ch-zz-bad", CWD: "/repo/zz-bad", Round: 1, State: store.StateActive,
			MasterMindID: testMasterMindID,
			Builder:      store.Endpoint{Kind: "opencode", Mode: store.ModeRemote, Server: "zen"}},
		{Name: "ch-zz-bad-plan", CWD: "/repo/zz-bad-plan", Round: 1, State: store.StateActive,
			Role: "planner", Shape: store.ShapeReader, MasterMindID: testMasterMindID},
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.CreateChain(bad, badMembers) }); err != nil {
		t.Fatalf("seed the failing chain: %v", err)
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.Save(store.Binding{Name: "lone", CWD: "/repo/lone", Round: 1,
			State: store.StateDone, MasterMindID: testMasterMindID})
	}); err != nil {
		t.Fatalf("seed the DONE binding: %v", err)
	}

	done, err := GCDoneChains(context.Background(), rt, housekeep.GCOptions{MasterMindID: testMasterMindID})
	var unsettled *ChainsNotSettledError
	if !errors.As(err, &unsettled) {
		t.Fatalf("GCDoneChains err = %v, want a ChainsNotSettledError", err)
	}
	if len(unsettled.Failures) != 1 || unsettled.Failures[0].Chain != "ch-zz-bad" {
		t.Fatalf("unsettled = %+v, want only ch-zz-bad", unsettled.Failures)
	}
	cleared := map[string]bool{}
	for _, r := range done {
		cleared[r.Name] = true
	}
	for _, m := range []string{"lone", "ch-aa-ok", "ch-aa-ok-plan"} {
		if !cleared[m] {
			t.Errorf("GC results = %+v, want %s cleared past the failed chain", done, m)
		}
		if _, lerr := rt.Store.Load(m); !errors.Is(lerr, store.ErrNotFound) {
			t.Errorf("Load %s after sweep = %v, want the record archived away", m, lerr)
		}
	}
	for _, m := range []string{"ch-zz-bad", "ch-zz-bad-plan"} {
		got, lerr := rt.Store.Load(m)
		if lerr != nil || got.State != store.StateActive {
			t.Errorf("%s = %+v err %v, want it still ACTIVE: its chain was not closed", m, got, lerr)
		}
		if cleared[m] {
			t.Errorf("%s was cleared, want it left for its own close", m)
		}
	}
}

// TestGCDoneChainsLeavesUnfinishedChainsAlone pins the guard: halted,
// stopped and running chains keep their ACTIVE members, and the running
// chain's refusal never fires because it is never called.
func TestGCDoneChainsLeavesUnfinishedChainsAlone(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	seedSettleChain(t, rt, "ch-run", "running", testMasterMindID, "ch-run", "ch-run-plan")
	seedSettleChain(t, rt, "ch-halt", "halted", testMasterMindID, "ch-halt", "ch-halt-plan")
	seedSettleChain(t, rt, "ch-stop", "stopped", testMasterMindID, "ch-stop", "ch-stop-plan")

	done, err := GCDoneChains(context.Background(), rt, housekeep.GCOptions{MasterMindID: testMasterMindID})
	if err != nil {
		t.Fatalf("GCDoneChains: %v", err)
	}
	if len(done) != 0 {
		t.Errorf("GC results = %+v, want none: no DONE binding exists", done)
	}
	for _, m := range []string{"ch-run", "ch-run-plan", "ch-halt", "ch-halt-plan", "ch-stop", "ch-stop-plan"} {
		got, err := rt.Store.Load(m)
		if err != nil {
			t.Errorf("Load %s: %v", m, err)
			continue
		}
		if got.State != store.StateActive {
			t.Errorf("%s state = %q, want active", m, got.State)
		}
	}
}

// TestGCDoneChainsDryRunReportsWithoutSettling pins the dry run: the store
// reads back identical, and both members are still reported as would-clear
// even though neither is done yet. An already-done member prints exactly
// once, through the GC rows rather than the settle rows; a selected name
// with no record names nothing; and a member with a kept worktree still
// lists as would-clear with its binding kept.
func TestGCDoneChainsDryRunReportsWithoutSettling(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Git = &fakeGit{dirtyResult: true}
	seedSettleChain(t, rt, "ch-dry", "done", testMasterMindID, "ch-dry", "ch-dry-plan")
	seedSettleChain(t, rt, "ch-mixed", "done", testMasterMindID, "ch-mixed", "ch-mixed-plan")
	seedSettleChain(t, rt, "ch-dirty", "done", testMasterMindID, "ch-dirty")

	now := rt.Now().UTC()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("ch-mixed-plan")
		if err != nil {
			return err
		}
		b.State = store.StateDone
		return tx.Save(b)
	}); err != nil {
		t.Fatalf("mark the mixed member done: %v", err)
	}
	// A selected name with no record: the row names a planner that was
	// never saved, so the dry run has nothing to report for it.
	ghost := db.ChainRow{
		ID:           db.NewID(),
		Name:         "ch-ghost",
		Status:       "done",
		Phase:        "build",
		Step:         "reviewing",
		Plan:         1,
		Plans:        1,
		SettingsJSON: []byte(`{"max_corrections":1}`),
		Builder:      "ch-ghost",
		Planner:      "ch-ghost-plan",
		MasterMindID: testMasterMindID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.ChainPut(ghost); err != nil {
			return err
		}
		return tx.Save(store.Binding{Name: "ch-ghost", CWD: "/repo/ch-ghost", Round: 1,
			State: store.StateActive, MasterMindID: testMasterMindID})
	}); err != nil {
		t.Fatalf("seed the ghost chain: %v", err)
	}
	// A member with a kept worktree: the dirtied tree reports kept, so the
	// binding stays even though the dry run still lists it.
	wt := t.TempDir()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("ch-dirty")
		if err != nil {
			return err
		}
		b.Worktree = wt
		return tx.Save(b)
	}); err != nil {
		t.Fatalf("give the dirty member a worktree: %v", err)
	}

	beforeBindings, err := rt.Store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	beforeChains, err := rt.Store.Chains()
	if err != nil {
		t.Fatalf("Chains: %v", err)
	}

	done, err := GCDoneChains(context.Background(), rt, housekeep.GCOptions{MasterMindID: testMasterMindID, DryRun: true})
	if err != nil {
		t.Fatalf("GCDoneChains dry-run: %v", err)
	}
	seen := map[string]int{}
	byName := map[string]housekeep.GCResult{}
	for _, r := range done {
		seen[r.Name]++
		byName[r.Name] = r
	}
	for _, m := range []string{"ch-dry", "ch-dry-plan", "ch-mixed", "ch-ghost", "ch-dirty"} {
		if seen[m] != 1 {
			t.Errorf("dry-run results = %+v, want exactly one would-clear row for %q", done, m)
		}
	}
	if seen["ch-mixed-plan"] != 1 {
		t.Errorf("dry-run results = %+v, want the done member exactly once, via the GC rows", done)
	}
	if seen["ch-ghost-plan"] != 0 {
		t.Errorf("dry-run results = %+v, want nothing for the unrecorded member", done)
	}
	kept, ok := byName["ch-dirty"]
	if !ok {
		t.Fatalf("dry-run results = %+v, missing would-clear member %q", done, "ch-dirty")
	}
	if !kept.BindingKept || kept.WorktreeKept != wt || kept.KeptReason != "uncommitted changes" {
		t.Errorf("dirty result = %+v, want BindingKept with the dirty-tree reason", kept)
	}
	if kept.Archived || kept.Deleted {
		t.Errorf("dirty result = %+v, want neither archived nor deleted on a dry run", kept)
	}

	afterBindings, err := rt.Store.List()
	if err != nil {
		t.Fatalf("List after: %v", err)
	}
	afterChains, err := rt.Store.Chains()
	if err != nil {
		t.Fatalf("Chains after: %v", err)
	}
	if !reflect.DeepEqual(afterBindings, beforeBindings) {
		t.Errorf("dry run changed the bindings: before %+v, after %+v", beforeBindings, afterBindings)
	}
	if !reflect.DeepEqual(afterChains, beforeChains) {
		t.Errorf("dry run changed the chains: before %+v, after %+v", beforeChains, afterChains)
	}
}

// TestGCDoneChainsSettlesARemoteMemberLocallyOn404 pins the 404 fall-through
// through the whole sweep: the server's already-gone answer settles the
// remote member locally instead of failing the chain's close.
func TestGCDoneChainsSettlesARemoteMemberLocallyOn404(t *testing.T) {
	t.Parallel()

	fr := &fakeRemote{doneErr: goneErr, unbindErr: goneErr}
	rt := newRuntime(t)
	rt.Remote = fr
	now := rt.Now().UTC()
	row := db.ChainRow{
		ID: db.NewID(), Name: "ch-far", Status: "done",
		Phase: "build", Step: "reviewing", Plan: 1, Plans: 1,
		SettingsJSON: []byte(`{"max_corrections":1}`),
		Builder:      "ch-far", Planner: "ch-far-plan",
		MasterMindID: testMasterMindID, CreatedAt: now, UpdatedAt: now,
	}
	members := []store.Binding{
		{Name: "ch-far", CWD: "/repo/far", Round: 1, State: store.StateActive,
			MasterMindID: testMasterMindID,
			Builder:      store.Endpoint{Kind: "opencode", Mode: store.ModeRemote, Server: "zen"}},
		{Name: "ch-far-plan", CWD: "/repo/far-plan", Round: 1, State: store.StateActive,
			Role: "planner", Shape: store.ShapeReader, MasterMindID: testMasterMindID},
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.CreateChain(row, members) }); err != nil {
		t.Fatalf("seed the far chain: %v", err)
	}

	if _, err := GCDoneChains(context.Background(), rt, housekeep.GCOptions{MasterMindID: testMasterMindID}); err != nil {
		t.Fatalf("GCDoneChains: %v", err)
	}
	for _, m := range []string{"ch-far", "ch-far-plan"} {
		if _, err := rt.Store.Load(m); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Load %s after sweep = %v, want the record archived away", m, err)
		}
	}
	if row, err := rt.Store.Chain("ch-far"); err != nil || row.Status != "done" {
		t.Errorf("chain = %+v err = %v, want status done", row, err)
	}
}

// TestGCDoneChainsMakesTheStatusHintClearable pins the footer's truthfulness
// without touching its text: one DONE binding plus one done chain with two
// ACTIVE members reads as hidden two, and after the sweep every hidden unit
// is gone, so the footer count is zero.
func TestGCDoneChainsMakesTheStatusHintClearable(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.Save(store.Binding{Name: "lone", CWD: "/repo/lone", Round: 1,
			State: store.StateDone, MasterMindID: testMasterMindID})
	}); err != nil {
		t.Fatalf("seed the DONE binding: %v", err)
	}
	seedSettleChain(t, rt, "ch-x", "done", testMasterMindID, "ch-x", "ch-x-plan")

	rep, err := ScopedStatus(context.Background(), rt, Scope{MasterMindID: testMasterMindID})
	if err != nil {
		t.Fatalf("ScopedStatus: %v", err)
	}
	if rep.DoneHidden != 2 {
		t.Fatalf("DoneHidden = %d, want 2: the DONE binding and the done chain", rep.DoneHidden)
	}
	if out := view.RenderStatus(rep); !strings.Contains(out, "2 done · relevo unbind --done to clear") {
		t.Errorf("status before the sweep misses the 2-done footer:\n%s", out)
	}

	done, err := GCDoneChains(context.Background(), rt, housekeep.GCOptions{MasterMindID: testMasterMindID})
	if err != nil {
		t.Fatalf("GCDoneChains: %v", err)
	}
	if len(done) != 3 {
		t.Fatalf("GC results = %+v, want all three hidden units", done)
	}

	after, err := ScopedStatus(context.Background(), rt, Scope{MasterMindID: testMasterMindID})
	if err != nil {
		t.Fatalf("ScopedStatus after: %v", err)
	}
	if after.DoneHidden != 0 {
		t.Errorf("DoneHidden after = %d, want 0", after.DoneHidden)
	}
	if out := view.RenderStatus(after); strings.Contains(out, "done ·") {
		t.Errorf("status after the sweep still prints a done footer:\n%s", out)
	}
}
