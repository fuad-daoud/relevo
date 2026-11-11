package relevo

import (
	"context"
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

func retireRemoteBinding(name, server string) store.Binding {
	b := remoteBinding(server)
	b.Name = name
	return b
}

func retireChainRow(name, server, status string) db.ChainRow {
	return db.ChainRow{Name: name, Server: server, Status: status}
}

// TestPlanServerRetire pins the retire rule: archive candidates are remote
// bindings on the server, deduped and sorted; a live process refuses the
// binding; every unfinished chain refuses naming its status; done and stopped
// chains, other servers and non-remote bindings stay out of both lists.
func TestPlanServerRetire(t *testing.T) {
	t.Parallel()

	remote := func(name, server string) store.Binding {
		return store.Binding{Name: name, Builder: store.Endpoint{Mode: store.ModeRemote, Server: server}}
	}
	local := func(name string) store.Binding {
		return store.Binding{Name: name, Builder: store.Endpoint{Mode: store.ModeHeadless}}
	}

	cases := []struct {
		name         string
		bindings     []store.Binding
		chains       []db.ChainRow
		live         map[string]bool
		wantArchive  []string
		wantRefusals []RetireRefusal
	}{
		{
			name:         "running chain refuses",
			bindings:     []store.Binding{remote("api", "zen")},
			chains:       []db.ChainRow{retireChainRow("shop", "zen", string(chain.StatusRunning))},
			wantArchive:  []string{"api"},
			wantRefusals: []RetireRefusal{{Name: "shop", Reason: "chain is running"}},
		},
		{
			name:         "halted chain refuses",
			bindings:     []store.Binding{remote("api", "zen")},
			chains:       []db.ChainRow{retireChainRow("shop", "zen", string(chain.StatusHalted))},
			wantArchive:  []string{"api"},
			wantRefusals: []RetireRefusal{{Name: "shop", Reason: "chain is halted"}},
		},
		{
			name:        "done and stopped chains do not",
			bindings:    []store.Binding{remote("api", "zen")},
			chains:      []db.ChainRow{retireChainRow("done-one", "zen", string(chain.StatusDone)), retireChainRow("stopped-one", "zen", string(chain.StatusStopped))},
			wantArchive: []string{"api"},
		},
		{
			name:         "live-process binding refuses and is not archived",
			bindings:     []store.Binding{remote("api", "zen")},
			live:         map[string]bool{"api": true},
			wantRefusals: []RetireRefusal{{Name: "api", Reason: "live local process"}},
		},
		{
			name:        "binding on another server left alone",
			bindings:    []store.Binding{remote("web", "zen"), remote("api", "zen"), remote("other", "contabo"), local("desk")},
			wantArchive: []string{"api", "web"},
		},
		{
			name:        "duplicate binding name archived once",
			bindings:    []store.Binding{remote("api", "zen"), remote("api", "zen")},
			wantArchive: []string{"api"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := PlanServerRetire(tc.bindings, tc.chains, tc.live, "zen")
			if !reflect.DeepEqual(got.Archive, tc.wantArchive) {
				t.Errorf("Archive = %q, want %q", got.Archive, tc.wantArchive)
			}
			if !reflect.DeepEqual(got.Refusals, tc.wantRefusals) {
				t.Errorf("Refusals = %+v, want %+v", got.Refusals, tc.wantRefusals)
			}
		})
	}
}

// TestRetireServerBindingsArchivesWithoutServerCall pins the store-level
// retire: bindings on the retired server archive with no server call even
// though the remote errors, the other server's binding stays live, and the
// result names exactly what landed.
func TestRetireServerBindingsArchivesWithoutServerCall(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt := newRuntime(t)
	fr := &fakeRemote{unbindErr: client.ErrUnreachable}
	rt.Remote = fr
	for _, b := range []store.Binding{
		retireRemoteBinding("web", "zen"),
		retireRemoteBinding("api", "zen"),
		retireRemoteBinding("other", "contabo"),
	} {
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("Save %s: %v", b.Name, err)
		}
	}

	res, err := RetireServerBindings(ctx, rt, "zen", false)
	if err != nil {
		t.Fatalf("RetireServerBindings: %v", err)
	}
	if len(fr.calls) != 0 {
		t.Errorf("server calls = %q, want none", fr.calls)
	}
	if want := []string{"api", "web"}; !reflect.DeepEqual(res.Archived, want) {
		t.Errorf("Archived = %q, want %q", res.Archived, want)
	}
	if want := []string{"api", "web"}; !reflect.DeepEqual(res.Plan.Archive, want) {
		t.Errorf("Plan.Archive = %q, want %q", res.Plan.Archive, want)
	}
	if len(res.Plan.Refusals) != 0 {
		t.Errorf("Plan.Refusals = %+v, want none", res.Plan.Refusals)
	}
	live, err := rt.Store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(live) != 1 || live[0].Name != "other" {
		names := make([]string, 0, len(live))
		for _, b := range live {
			names = append(names, b.Name)
		}
		t.Errorf("live bindings = %q, want [other]", names)
	}
	archived, err := rt.Store.ListArchived()
	if err != nil {
		t.Fatalf("ListArchived: %v", err)
	}
	got := make([]string, 0, len(archived))
	for _, a := range archived {
		got = append(got, a.Binding.Name)
	}
	if want := []string{"api", "web"}; !reflect.DeepEqual(got, want) {
		t.Errorf("archived = %q, want %q", got, want)
	}
}

// TestRetireServerBindingsRefusesARunningChain pins the refusal: a running
// chain on the server archives nothing and keeps its members live.
func TestRetireServerBindingsRefusesARunningChain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt := newRuntime(t)
	rt.Remote = &fakeRemote{unbindErr: client.ErrUnreachable}
	seedServerChain(t, rt, "shop")

	res, err := RetireServerBindings(ctx, rt, "zen", false)
	if err != nil {
		t.Fatalf("RetireServerBindings: %v", err)
	}
	if len(res.Archived) != 0 {
		t.Errorf("Archived = %q, want none", res.Archived)
	}
	found := false
	for _, r := range res.Plan.Refusals {
		if r.Name == "shop" {
			found = true
		}
	}
	if !found {
		t.Errorf("Plan.Refusals = %+v, want a refusal naming shop", res.Plan.Refusals)
	}
	if _, err := rt.Store.Load("shop"); err != nil {
		t.Errorf("shop still live: Load: %v", err)
	}
}

// TestUnbindLocalOnlyMarksServerNotReleased pins the core the retire runs on:
// a remote binding archived without its server carries ServerNotReleased, a
// local one does not, and the ordinary path never sets it.
func TestUnbindLocalOnlyMarksServerNotReleased(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("remote archived locally", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime(t)
		rt.Remote = &fakeRemote{}
		if err := rt.Store.Save(retireRemoteBinding("api", "zen")); err != nil {
			t.Fatal(err)
		}
		res, err := unbindWithLocal(ctx, rt, "api", true, false, true)
		if err != nil {
			t.Fatalf("unbindWithLocal: %v", err)
		}
		if !res.ServerNotReleased {
			t.Errorf("ServerNotReleased = false, want true")
		}
		if len(rt.Remote.(*fakeRemote).calls) != 0 {
			t.Errorf("server calls = %q, want none", rt.Remote.(*fakeRemote).calls)
		}
	})

	t.Run("local archived locally", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime(t)
		if err := rt.Store.Save(store.Binding{Name: "desk", CWD: "/repo", Repo: "/repo", State: store.StateActive, Builder: store.Endpoint{Mode: store.ModeHeadless}}); err != nil {
			t.Fatal(err)
		}
		res, err := unbindWithLocal(ctx, rt, "desk", true, false, true)
		if err != nil {
			t.Fatalf("unbindWithLocal: %v", err)
		}
		if res.ServerNotReleased {
			t.Errorf("ServerNotReleased = true for a local binding, want false")
		}
	})

	t.Run("ordinary unbind of a remote binding", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime(t)
		fr := &fakeRemote{}
		rt.Remote = fr
		if err := rt.Store.Save(retireRemoteBinding("api", "zen")); err != nil {
			t.Fatal(err)
		}
		res, err := UnbindWith(ctx, rt, "api", true, false)
		if err != nil {
			t.Fatalf("UnbindWith: %v", err)
		}
		if res.ServerNotReleased {
			t.Errorf("ServerNotReleased = true on the ordinary path, want false")
		}
		if len(fr.calls) != 1 {
			t.Errorf("server calls = %q, want the one Unbind", fr.calls)
		}
	})
}
