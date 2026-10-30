package relevo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

func gcRemoteRuntime(t *testing.T, fr *fakeRemote) (Runtime, *store.Store) {
	t.Helper()
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.State = store.StateDone
	b.MasterMindID = "mm"
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	return Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}, st
}

func gcRemoteOpts(o housekeep.GCOptions) housekeep.GCOptions {
	o.MasterMindID = "mm"

	return o
}

func TestGCKeepsRemoteBindingWhenServerKeepsWorktree(t *testing.T) {
	t.Parallel()

	fr := &fakeRemote{unbindErr: &client.HTTPError{Status: 409, Body: remote.ErrorBody{Code: remote.CodeWorktreeKept, Message: "binding api kept"}}}
	rt, st := gcRemoteRuntime(t, fr)

	res, err := housekeep.GC(context.Background(), HousekeepDeps(rt), gcRemoteOpts(housekeep.GCOptions{}))
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	b, lerr := st.Load("api")
	if lerr != nil || b.State != store.StateDone {
		t.Fatalf("binding = %+v, %v; want it still DONE", b.State, lerr)
	}
	if len(res) != 1 || !res[0].BindingKept || res[0].WorktreeKept == "" || res[0].Archived {
		t.Errorf("result = %+v, want kept with the server's reason", res)
	}
	if len(fr.unbindForce) != 1 || fr.unbindForce[0] {
		t.Errorf("force sent = %v, want [false]", fr.unbindForce)
	}
}

func TestGCRemoteKeptLineCarriesTheBareReason(t *testing.T) {
	t.Parallel()

	fr := &fakeRemote{unbindErr: &client.HTTPError{Status: 409, Body: remote.ErrorBody{Code: remote.CodeWorktreeKept, Message: oldServerKeptBody}}}
	rt, _ := gcRemoteRuntime(t, fr)

	res, err := housekeep.GC(context.Background(), HousekeepDeps(rt), gcRemoteOpts(housekeep.GCOptions{}))
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(res) != 1 || res[0].KeptReason != "uncommitted changes" || res[0].WorktreeKept != "on zen" {
		t.Errorf("result = %+v, want reason %q on zen", res, "uncommitted changes")
	}
}

func TestGCForcePassesForceToServer(t *testing.T) {
	t.Parallel()

	fr := &fakeRemote{}
	rt, st := gcRemoteRuntime(t, fr)

	res, err := housekeep.GC(context.Background(), HousekeepDeps(rt), gcRemoteOpts(housekeep.GCOptions{Force: true}))
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(fr.unbindForce) != 1 || !fr.unbindForce[0] {
		t.Errorf("force sent = %v, want [true]", fr.unbindForce)
	}
	if len(res) != 1 || !res[0].Archived {
		t.Errorf("result = %+v, want archived", res)
	}
	if _, lerr := st.Load("api"); !errors.Is(lerr, store.ErrNotFound) {
		t.Errorf("binding still loads: %v", lerr)
	}
}

func TestGCArchivesRemoteBindingWhenServerUnreachable(t *testing.T) {
	t.Parallel()

	fr := &fakeRemote{unbindErr: client.ErrUnreachable}
	rt, st := gcRemoteRuntime(t, fr)

	res, err := housekeep.GC(context.Background(), HousekeepDeps(rt), gcRemoteOpts(housekeep.GCOptions{}))
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(res) != 1 || !res[0].Archived || res[0].ServerErr == "" {
		t.Errorf("result = %+v, want archived with ServerErr", res)
	}
	if _, lerr := st.Load("api"); !errors.Is(lerr, store.ErrNotFound) {
		t.Errorf("binding still loads: %v", lerr)
	}
}

func TestGCRemoteResumedBetweenPassesIsSkipped(t *testing.T) {
	t.Parallel()

	fr := &fakeRemote{}
	rt, st := gcRemoteRuntime(t, fr)
	fr.onUnbind = func() {
		b, err := st.Load("api")
		if err != nil {
			t.Error(err)
			return
		}
		b.State = store.StateActive
		if err := st.Save(b); err != nil {
			t.Error(err)
		}
	}

	res, err := housekeep.GC(context.Background(), HousekeepDeps(rt), gcRemoteOpts(housekeep.GCOptions{}))
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	b, lerr := st.Load("api")
	if lerr != nil || b.State != store.StateActive {
		t.Fatalf("binding = %v, %v; want it left ACTIVE", b.State, lerr)
	}
	if len(res) != 0 {
		t.Errorf("results = %+v, want none", res)
	}
}

func TestGCDryRunDoesNotContactServer(t *testing.T) {
	t.Parallel()

	fr := &fakeRemote{}
	rt, st := gcRemoteRuntime(t, fr)

	res, err := housekeep.GC(context.Background(), HousekeepDeps(rt), gcRemoteOpts(housekeep.GCOptions{DryRun: true}))
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(fr.calls) != 0 {
		t.Errorf("server calls = %v, want none", fr.calls)
	}
	if len(res) != 1 || res[0].Archived {
		t.Errorf("result = %+v, want one unarchived row", res)
	}
	if _, lerr := st.Load("api"); lerr != nil {
		t.Errorf("binding must stay: %v", lerr)
	}
}
