package relevo

import (
	"context"
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// forwardBinding clamps b's round onto a burst node at phase: the shape the
// serve side records when it forwards.
func forwardBinding(b store.Binding, phase string) store.Binding {
	b.Burst = &store.BurstFacts{Forward: &store.Forward{
		Node:        "node-1",
		NodeBinding: "deadbeef-webshop",
		Round:       b.Round,
		Phase:       phase,
	}}
	return b
}

// TestServedViewNodeNamesRunningForward pins the wire node: set while the
// node runs the round, empty for a forward the node has not taken up, one
// it is done with, and for a round that never left.
func TestServedViewNodeNamesRunningForward(t *testing.T) {
	t.Parallel()

	b := store.Binding{Name: "webshop", Round: 1}
	for _, tc := range []struct {
		phase string
		fwd   bool
		want  string
	}{
		{phase: "running", fwd: true, want: "node-1"},
		{phase: "sending", fwd: true, want: ""},
		{phase: "closed", fwd: true, want: ""},
		{phase: "", fwd: false, want: ""},
	} {
		in := b
		if tc.fwd {
			in = forwardBinding(in, tc.phase)
		}
		if got := ServedView(in, nil, "", ""); got.Node != tc.want {
			t.Errorf("phase %q: ServedView.Node = %q, want %q", tc.phase, got.Node, tc.want)
		}
	}
}

// TestServedLiveReturnsNodeLiveForRunningForward pins the node hook: a
// running forward answers the serve side's cached view without reading the
// local row.
func TestServedLiveReturnsNodeLiveForRunningForward(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt = withClock(rt, &fakeClock{now: baseTime})
	b = forwardBinding(b, "running")
	want := &remote.LiveView{At: baseTime, PID: 4242, Tail: []string{"node tail"}}
	called := 0
	rt.NodeLive = func(got store.Binding) *remote.LiveView {
		called++
		if got.Name != b.Name {
			t.Errorf("NodeLive binding = %q, want %q", got.Name, b.Name)
		}
		return want
	}

	got, err := ServedLive(context.Background(), rt, b)
	if err != nil {
		t.Fatalf("ServedLive: %v", err)
	}
	if called != 1 {
		t.Fatalf("NodeLive calls = %d, want 1", called)
	}
	if got != want {
		t.Errorf("ServedLive = %+v, want the hook's view", got)
	}
}

// TestServedLiveReadsLocalRowOtherwise pins the hook's bounds: a local
// round, a forward the node has not taken up, one it is done with, and a
// hook with nothing cached all read the binding's own row, and the hook is
// never consulted except for a running forward.
func TestServedLiveReadsLocalRowOtherwise(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		phase string
		fwd   bool
		nilV  bool
	}{
		{name: "local round", fwd: false},
		{name: "sending forward", phase: "sending", fwd: true},
		{name: "closed forward", phase: "closed", fwd: true},
		{name: "hook with nothing cached", phase: "running", fwd: true, nilV: true},
	} {
		fr := newFakeRunner()
		rt, b := sentHeadless(t, fr)
		rt = withClock(rt, &fakeClock{now: baseTime})
		if tc.fwd {
			b = forwardBinding(b, tc.phase)
		}
		called := false
		rt.NodeLive = func(store.Binding) *remote.LiveView {
			called = true
			if tc.nilV {
				return nil
			}
			return &remote.LiveView{At: baseTime, PID: 4242}
		}

		got, err := ServedLive(context.Background(), rt, b)
		if err != nil {
			t.Fatalf("%s: ServedLive: %v", tc.name, err)
		}
		if got == nil {
			t.Fatalf("%s: ServedLive = nil, want the local row", tc.name)
		}
		if !tc.nilV && called {
			t.Errorf("%s: NodeLive consulted, want the local row without it", tc.name)
		}
		rt.NodeLive = nil
		want, err := ServedLive(context.Background(), rt, b)
		if err != nil {
			t.Fatalf("%s: ServedLive without hook: %v", tc.name, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: ServedLive = %+v, want the local row %+v", tc.name, got, want)
		}
	}
}
