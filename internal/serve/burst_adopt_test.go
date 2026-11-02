package serve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// saveNodeOpenRound stores a node-side binding with an open round, the way a
// node that took the send looks when the head asks after a restart.
func saveNodeOpenRound(t *testing.T, nodeSrv *Server, owner remote.ClientID, name string, round int) {
	t.Helper()
	dir, ok := owner.Dir()
	if !ok {
		t.Fatalf("owner dir for %s: malformed client id", owner)
	}
	cwd := filepath.Join(t.TempDir(), name+"-node-wt")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	st := nodeSrv.ownerStore(filepath.Join(nodeSrv.cfg.Root, "bindings", dir))
	b := store.Binding{
		Name: name, Owner: string(owner), CWD: cwd,
		State: store.StateActive, Round: round,
		BuilderCandidate: "claude/anthropic/haiku",
	}
	if err := st.Save(b); err != nil {
		t.Fatalf("save node binding %s: %v", name, err)
	}
	if err := st.AppendLog(name, store.LogEntry{
		TS: time.Now().UTC(), Round: round,
		Direction: store.DirToBuilder, Kind: store.KindPrompt,
	}); err != nil {
		t.Fatalf("append node prompt %s: %v", name, err)
	}
}

// burstLetterKey generates a head key whose owner half of a node binding
// name starts with a letter: the store's name rule refuses a digit-led
// name, and the node binding under test is saved through the store.
func burstLetterKey(t *testing.T) remote.Keypair {
	t.Helper()
	for i := 0; i < 100; i++ {
		kp, err := remote.Generate()
		if err != nil {
			t.Fatal(err)
		}
		if c := burstOwner8(remote.IDOf(kp.Public))[0]; c >= 'a' && c <= 'z' {
			return kp
		}
	}
	t.Fatal("no letter-led owner in 100 keys")
	return remote.Keypair{}
}

// TestBurstAdoptSending pins the restart resolve: a forward the node still
// holds turns running, while a forward the node never took and one naming
// an unknown node return to the local queue at their FIFO place. The node
// is a real serve server over httptest TLS, reached through the production
// client; only the classify half below runs without the network.
func TestBurstAdoptSending(t *testing.T) {
	headKP := burstLetterKey(t)
	env := burstVerifyEnv(t, headKP, nil)
	nodeSrv, nodeTS, nodeFP := startBurstNode(t, &headKP)
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{
		burstRowFor("b1", nodeTS.URL, nodeFP),
	}}

	since := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	headID := remote.IDOf(headKP.Public)
	held := burstNodeBinding(headID, "a")
	saveNodeOpenRound(t, nodeSrv, headID, held, 3)
	saveHeadForward(t, env, "a", "b1", held, 3, since)
	saveHeadForward(t, env, "b", "b1", burstNodeBinding(env.id, "b"), 5, since)
	saveHeadForward(t, env, "c", "b9", "deadbeef-c", 7, since)

	env.srv.adoptSending(context.Background())

	rt := env.runtime(t)
	kept := loadBurstBinding(t, rt, "a")
	if fwd := burstStoredForward(kept); fwd == nil || fwd.Phase != "running" || fwd.Node != "b1" || fwd.Round != 3 {
		t.Errorf("a forward = %+v, want the held round confirmed running on b1", kept.Burst)
	}
	if !kept.QueuedAt.IsZero() {
		t.Errorf("a QueuedAt = %v, want zero while the forward is out", kept.QueuedAt)
	}
	for _, name := range []string{"b", "c"} {
		back := loadBurstBinding(t, rt, name)
		if burstStoredForward(back) != nil {
			t.Errorf("%s forward = %+v, want it cleared", name, back.Burst.Forward)
		}
		if !back.QueuedAt.Equal(since) {
			t.Errorf("%s QueuedAt = %v, want the original %v", name, back.QueuedAt, since)
		}
	}
}

// TestBurstAdoptClassify pins the pure decision adopt fetches feed: open at
// the claimed round stays out, everything else -- a refused fetch, a
// missing or different round, a closed one -- waits locally again. No
// network here; the live fetch above owns the wire.
func TestBurstAdoptClassify(t *testing.T) {
	fwd := store.Forward{Node: "b1", Round: 3, Phase: "sending"}
	view := func(round int, state remote.RoundState) remote.BindingView {
		return remote.BindingView{Round: round, RoundState: state}
	}
	cases := []struct {
		name string
		view remote.BindingView
		err  error
		want bool
	}{
		{"running at the round stays out", view(3, remote.RoundRunning), nil, true},
		{"queued at the round stays out", view(3, remote.RoundQueued), nil, true},
		{"different round requeues", view(4, remote.RoundRunning), nil, false},
		{"closed round requeues", view(3, remote.RoundClosed), nil, false},
		{"idle round requeues", view(3, remote.RoundIdle), nil, false},
		{"broken round requeues", view(3, remote.RoundBroken), nil, false},
		{"missing binding requeues", view(0, ""), errors.New("http 404: not_found"), false},
		{"unreachable node requeues", view(0, ""), client.ErrUnreachable, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := burstAdoptRunning(fwd, c.view, c.err); got != c.want {
				t.Errorf("burstAdoptRunning(%+v, %+v, %v) = %v, want %v", fwd, c.view, c.err, got, c.want)
			}
		})
	}
}
