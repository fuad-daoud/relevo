package relevo

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/housekeep"
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
// even though neither is done yet.
func TestGCDoneChainsDryRunReportsWithoutSettling(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	seedSettleChain(t, rt, "ch-dry", "done", testMasterMindID, "ch-dry", "ch-dry-plan")

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
	got := memberSet(nil)
	for _, r := range done {
		got[r.Name] = true
	}
	for _, m := range []string{"ch-dry", "ch-dry-plan"} {
		if !got[m] {
			t.Errorf("dry-run results = %+v, missing would-clear member %q", done, m)
		}
	}

	afterBindings, err := rt.Store.List()
	if err != nil {
		t.Fatalf("List after: %v", err)
	}
	afterChains, err := rt.Store.Chains()
	if err != nil {
		t.Fatalf("Chains after: %v", err)
	}
	if len(afterBindings) != len(beforeBindings) || len(afterChains) != len(beforeChains) {
		t.Fatalf("dry run changed the store: %d bindings/%d chains before, %d/%d after",
			len(beforeBindings), len(beforeChains), len(afterBindings), len(afterChains))
	}
	for _, b := range afterBindings {
		if b.State != store.StateActive {
			t.Errorf("%s state = %q after dry run, want active", b.Name, b.State)
		}
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
