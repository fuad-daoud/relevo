package relevo

import (
	"context"
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// goneErr is the server's 404: the answer that says the binding is gone
// there, whatever the reason.
var goneErr = &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: remote.CodeNotFound}}

// newDoneChainMember seeds a DONE chain whose planner member is still locally
// NEEDS YOU with a remote builder on server zen: the stale row the waiting
// notice names and nothing clears by itself. The member keeps no worktree, so
// a settle touches no tree and no branch.
func newDoneChainMember(t *testing.T, fr *fakeRemote) (Runtime, store.Binding) {
	t.Helper()
	rt := newRuntime(t)
	rt.Remote = fr

	now := rt.Now().UTC()
	row := db.ChainRow{
		ID:           db.NewID(),
		Name:         "ch-orphan",
		Status:       string(chain.StatusDone),
		Phase:        "review",
		Step:         "reviewing",
		Plan:         1,
		Plans:        1,
		SettingsJSON: []byte(`{"max_corrections":1}`),
		Builder:      "ch-orphan",
		Planner:      "ch-orphan-plan",
		MasterMindID: testMasterMindID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	member := store.Binding{
		Name: "ch-orphan-plan", CWD: "/repo/orphan-plan", Round: 2, State: store.StateNeedsYou,
		Role: "lite-planner", Shape: store.ShapeReader, MasterMindID: testMasterMindID,
		Builder: store.Endpoint{Kind: "opencode", Mode: store.ModeRemote, Server: "zen"},
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.ChainPut(row); err != nil {
			return err
		}
		return tx.Save(member)
	}); err != nil {
		t.Fatalf("seed the stale member: %v", err)
	}
	return rt, member
}

// TestDoneOnAStaleChainMemberWithAFourOhFourServerSettlesLocally pins the 404
// fall-through: the server's already-gone answer is not a reason to refuse a
// done the human has decided, so the member settles locally. A remote error of
// any other kind still fails the verb and holds the record back.
func TestDoneOnAStaleChainMemberWithAFourOhFourServerSettlesLocally(t *testing.T) {
	t.Parallel()

	fr := &fakeRemote{doneErr: goneErr}
	rt, member := newDoneChainMember(t, fr)

	if _, err := Done(context.Background(), rt, member.Name); err != nil {
		t.Fatalf("Done on a gone member = %v, want it settled locally", err)
	}
	if n := exactCalls(fr.calls, "Done:zen:"+member.Name); n != 1 {
		t.Errorf("Done calls = %d, want exactly one ask", n)
	}
	got, err := rt.Store.Load(member.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.State != store.StateDone {
		t.Errorf("state = %q, want done", got.State)
	}

	fr2 := &fakeRemote{doneErr: &client.HTTPError{Status: 500, Body: remote.ErrorBody{Code: "boom"}}}
	rt2, member2 := newDoneChainMember(t, fr2)
	if _, err := Done(context.Background(), rt2, member2.Name); err == nil {
		t.Fatal("Done with a 500 = nil, want the server's failure kept")
	}
	if got, lerr := rt2.Store.Load(member2.Name); lerr != nil || got.State == store.StateDone {
		t.Errorf("after a 500: state = %q err = %v, want the record held back", got.State, lerr)
	}
}

// TestUnbindOnAStaleChainMemberWithAFourOhFourServerSettlesLocally pins the
// same tolerance in unbind: a gone-binding 404 proceeds exactly as an
// agreement would, and the record is deleted here.
func TestUnbindOnAStaleChainMemberWithAFourOhFourServerSettlesLocally(t *testing.T) {
	t.Parallel()

	fr := &fakeRemote{unbindErr: goneErr}
	rt, member := newDoneChainMember(t, fr)

	if _, err := Unbind(context.Background(), rt, member.Name, false); err != nil {
		t.Fatalf("Unbind on a gone member = %v, want it settled locally", err)
	}
	if _, err := rt.Store.Load(member.Name); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Load after unbind = %v, want the record gone", err)
	}
}

// TestGCArchivesAStaleChainMember pins the end of the clear path: a DONE row
// the human acknowledged is exactly what gc clears, archived by default, so
// the acknowledged name can never reappear in a notice.
func TestGCArchivesAStaleChainMember(t *testing.T) {
	t.Parallel()

	fr := &fakeRemote{doneErr: goneErr}
	rt, member := newDoneChainMember(t, fr)
	if _, err := Done(context.Background(), rt, member.Name); err != nil {
		t.Fatalf("Done: %v", err)
	}

	done, err := housekeep.GC(context.Background(), HousekeepDeps(rt), housekeep.GCOptions{MasterMindID: testMasterMindID})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(done) != 1 || done[0].Name != member.Name || !done[0].Archived {
		t.Fatalf("GC = %+v, want exactly the stale member archived", done)
	}
	if _, err := rt.Store.Load(member.Name); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Load after GC = %v, want the record archived away", err)
	}
}
