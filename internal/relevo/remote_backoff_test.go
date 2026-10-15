package relevo

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// silentRemote is a server that never answers a binding read: GetBinding
// returns only when its context ends.
type silentRemote struct {
	*fakeRemote
	gets atomic.Int32
}

func (s *silentRemote) GetBinding(ctx context.Context, server, name string) (remote.BindingView, error) {
	s.gets.Add(1)
	<-ctx.Done()
	return remote.BindingView{}, fmt.Errorf("%w: %v", client.ErrUnreachable, ctx.Err())
}

func shortenPrefetchDeadline(t *testing.T, d time.Duration) {
	t.Helper()
	orig := prefetchDeadline
	prefetchDeadline = d
	t.Cleanup(func() { prefetchDeadline = orig })
}

func saveRemoteBindings(t *testing.T, st *store.Store, server string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		b := remoteBinding(server)
		b.Name = fmt.Sprintf("api%d", i)
		if err := st.Save(b); err != nil {
			t.Fatal(err)
		}
	}
}

func unreachableRemote() *fakeRemote {
	return &fakeRemote{getBindingErr: fmt.Errorf("%w: connection refused", client.ErrUnreachable)}
}

func TestPrefetchOfSilentServerFailsWithinDeadline(t *testing.T) {
	shortenPrefetchDeadline(t, 100*time.Millisecond)
	st := store.New(t.TempDir())
	sr := &silentRemote{fakeRemote: &fakeRemote{}}
	d := NewDaemon(Runtime{Store: st, Remote: sr, Now: func() time.Time { return baseTime }}, time.Second)

	start := time.Now()
	pre := d.prefetchRemote(context.Background(), remoteBinding("zen"))
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("prefetch took %v against a server that never answers", took)
	}
	if pre == nil || pre.Err == nil {
		t.Fatalf("prefetch = %+v, want the failed read", pre)
	}
}

func TestTickDialsFailedServerOncePerTick(t *testing.T) {
	st := store.New(t.TempDir())
	saveRemoteBindings(t, st, "zen", 4)
	fr := unreachableRemote()
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if n := countCalls(fr, "GetBinding:"); n != 1 {
		t.Fatalf("GetBinding calls = %d, want 1 for four bindings on one failed server", n)
	}
	stamped := 0
	for i := 0; i < 4; i++ {
		b, err := st.Load(fmt.Sprintf("api%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if !b.RemoteUnreachableSince.IsZero() {
			stamped++
		}
		if b.State != store.StateActive {
			t.Errorf("api%d state = %s, a skipped binding must be left as it was", i, b.State)
		}
	}
	if stamped != 1 {
		t.Fatalf("%d bindings stamped unreachable, want only the one that dialled", stamped)
	}
}

func TestTickBacksOffFailedServerAcrossTicks(t *testing.T) {
	st := store.New(t.TempDir())
	saveRemoteBindings(t, st, "zen", 2)
	fr := unreachableRemote()
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}
	d := NewDaemon(rt, time.Second)
	clock := baseTime
	d.backoff.now = func() time.Time { return clock }

	tick := func() {
		t.Helper()
		if err := d.Tick(context.Background()); err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}
	tick()
	tick()
	if n := countCalls(fr, "GetBinding:"); n != 1 {
		t.Fatalf("GetBinding calls after two ticks inside the backoff = %d, want 1", n)
	}

	clock = clock.Add(serverBackoff + time.Second)
	tick()
	if n := countCalls(fr, "GetBinding:"); n != 2 {
		t.Fatalf("GetBinding calls after the backoff expired = %d, want one probe more", n)
	}
	tick()
	if n := countCalls(fr, "GetBinding:"); n != 2 {
		t.Fatalf("a failed probe must re-arm the backoff, got %d calls", n)
	}

	fr.getBindingErr = nil
	clock = clock.Add(serverBackoff + time.Second)
	tick()
	tick()
	if n := countCalls(fr, "GetBinding:"); n != 6 {
		t.Fatalf("GetBinding calls = %d, want a successful probe to end the backoff (both bindings dialled)", n)
	}
}

func TestTickDoesNotPrefetchDoneBindings(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.State = store.StateDone
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	fr := unreachableRemote()
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := countCalls(fr, "GetBinding:"); n != 0 {
		t.Fatalf("GetBinding calls = %d, want none for a done binding", n)
	}
}

func TestTickWithSilentServerStillReconcilesLocalBindings(t *testing.T) {
	shortenPrefetchDeadline(t, 100*time.Millisecond)
	rt, _ := sentBinding(t)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	saveRemoteBindings(t, rt.Store, "zen", 5)
	sr := &silentRemote{fakeRemote: &fakeRemote{}}
	rt.Remote = sr

	start := time.Now()
	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("Tick took %v with one server that never answers", took)
	}
	if n := sr.gets.Load(); n != 1 {
		t.Fatalf("GetBinding calls = %d, want one bounded dial for five bindings", n)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if b.Round != 2 {
		t.Fatalf("local binding round = %d, want the tick to have advanced it to 2", b.Round)
	}
}
