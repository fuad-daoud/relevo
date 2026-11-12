package relevo

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestChainPullAndPollAgreeOnAQueuedMember pins the queued member's shared row
// across the two passes that both read it every tick: the per-binding poll
// records the server's queue facts, and the chain pull must leave them, or
// each tick rewrites the synced row twice and every machine pulls it.
func TestChainPullAndPollAgreeOnAQueuedMember(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC)
	member := chainPullMemberView(chain.MemberBuilder, "shop", 0, store.ShapeWriter)
	member.View.RoundState = remote.RoundQueued
	fr := chainPullFake(chainPullViewOf("shop", string(chain.StatusRunning), member))
	rt := chainPullRuntime(t, fr)
	b := seedServerChain(t, rt, "shop")
	fr.getBindingResp = remote.BindingView{
		Name: "shop", Round: b.Round, RoundState: remote.RoundQueued,
		State: string(store.StateActive), Shape: store.ShapeWriter,
		Queue: &remote.QueueView{Position: 2, Ahead: 1, Running: 3, Cap: 3, Since: since, Wait: "memory"},
	}

	poll := func() {
		t.Helper()
		cur := chainBinding(t, rt, "shop")
		pre := NewDaemon(rt, time.Minute).prefetchRemote(context.Background(), cur, 0)
		defer pre.release()
		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			fresh, err := tx.Load("shop")
			if err != nil {
				return err
			}
			next, err := reconcileRemote(context.Background(), rt, tx, fresh, pre)
			if err != nil {
				return err
			}
			_, err = tx.SaveWithOverlay(next)
			return err
		}); err != nil {
			t.Fatalf("poll: %v", err)
		}
	}
	raw := outboxRaw(t, rt)
	count := func() int { return outboxPerTable(t, raw)["binding_record"] }

	poll()
	pullRounds(t, rt, "shop")
	before := count()
	for range 3 {
		poll()
		pullRounds(t, rt, "shop")
	}
	if got := count(); got != before {
		t.Errorf("binding_record outbox entries = %d after three poll+pull ticks, want %d: a queued member's unchanged row is rewritten", got, before)
	}
	q := chainBinding(t, rt, "shop").Builder.RemoteQueue
	if q == nil || q.Position != 2 || q.Wait != "memory" {
		t.Errorf("RemoteQueue after a pull = %+v, want the poll's position 2 waiting on memory", q)
	}
}

// TestChainPullKeepsARunningMembersLiveFacts pins the running half of the same
// split: the poll's live numbers survive the pull that follows it, so the
// member's status does not lose them on every other tick.
func TestChainPullKeepsARunningMembersLiveFacts(t *testing.T) {
	t.Parallel()

	member := chainPullMemberView(chain.MemberBuilder, "shop", 0, store.ShapeWriter)
	member.View.RoundState = remote.RoundRunning
	fr := chainPullFake(chainPullViewOf("shop", string(chain.StatusRunning), member))
	rt := chainPullRuntime(t, fr)
	b := seedServerChain(t, rt, "shop")
	fr.getBindingResp = remote.BindingView{
		Name: "shop", Round: b.Round, RoundState: remote.RoundRunning,
		State: string(store.StateActive), Shape: store.ShapeWriter,
		Live: &remote.LiveView{At: time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC), PID: 4711},
	}

	pre := NewDaemon(rt, time.Minute).prefetchRemote(context.Background(), b, 0)
	defer pre.release()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		next, err := reconcileRemote(context.Background(), rt, tx, b, pre)
		if err != nil {
			return err
		}
		_, err = tx.SaveWithOverlay(next)
		return err
	}); err != nil {
		t.Fatalf("poll: %v", err)
	}
	pullRounds(t, rt, "shop")

	if live := chainBinding(t, rt, "shop").Builder.RemoteLive; live == nil || live.PID != 4711 {
		t.Errorf("RemoteLive after a pull = %+v, want the poll's pid 4711", live)
	}
}
