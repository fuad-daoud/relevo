package relevo

import (
	"context"
	"errors"
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
	return remote.BindingView{}, fmt.Errorf("%w: %w", client.ErrUnreachable, ctx.Err())
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
			t.Errorf("api%d state = %s, an outage inside the grace must not halt", i, b.State)
		}
	}
	if stamped != 4 {
		t.Fatalf("%d bindings stamped unreachable, want all four: a skipped binding reconciles as unreachable", stamped)
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

// notFoundOneRemote answers 404 for the binding named bad and the fake's view
// for every other.
type notFoundOneRemote struct {
	*fakeRemote
	bad string
}

func (n *notFoundOneRemote) GetBinding(ctx context.Context, server, name string) (remote.BindingView, error) {
	view, err := n.fakeRemote.GetBinding(ctx, server, name)
	if name == n.bad {
		return view, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no binding"}}
	}
	return view, err
}

func TestTickPerBindingAnswerDoesNotBackOffServer(t *testing.T) {
	st := store.New(t.TempDir())
	saveRemoteBindings(t, st, "zen", 2)
	fr := &notFoundOneRemote{fakeRemote: &fakeRemote{}, bad: "api0"}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}
	d := NewDaemon(rt, time.Second)
	d.backoff.now = func() time.Time { return baseTime }

	for i := 1; i <= 2; i++ {
		if err := d.Tick(context.Background()); err != nil {
			t.Fatalf("Tick: %v", err)
		}
		if n := countCalls(fr.fakeRemote, "GetBinding:zen:api1"); n != i {
			t.Fatalf("after tick %d the healthy binding was fetched %d times, want %d", i, n, i)
		}
	}
	if d.backoff.skip("zen") {
		t.Fatal("a 404 for one binding armed a backoff for the whole server")
	}
}

func TestServerUnreachableCountsOnlyServerFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"wrapped unreachable", fmt.Errorf("%w: refused", client.ErrUnreachable), true},
		{"bare deadline", context.DeadlineExceeded, true},
		{"404", &client.HTTPError{Status: 404}, false},
		{"401", &client.HTTPError{Status: 401}, false},
		{"unknown server", client.ErrUnknownServer, false},
		{"other", errors.New("boom"), false},
	}
	for _, c := range cases {
		if got := serverUnreachable(c.err); got != c.want {
			t.Errorf("%s: serverUnreachable = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTickSilentServerStampsAndHaltsEveryOpenRound(t *testing.T) {
	st := store.New(t.TempDir())
	const n = 3
	for i := 0; i < n; i++ {
		b := remoteBinding("zen")
		b.Name = fmt.Sprintf("api%d", i)
		b.RoundTimeoutMS = 1000
		if err := st.Save(b); err != nil {
			t.Fatal(err)
		}
		name := b.Name
		if err := st.WithLock(func(tx *store.Tx) error {
			return tx.AppendLog(name, store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt})
		}); err != nil {
			t.Fatal(err)
		}
	}
	fr := unreachableRemote()
	clock := baseTime
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return clock }}
	d := NewDaemon(rt, time.Second)
	d.backoff.now = func() time.Time { return clock }

	tick := func() {
		t.Helper()
		if err := d.Tick(context.Background()); err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}
	tick()
	for i := 0; i < n; i++ {
		b, err := st.Load(fmt.Sprintf("api%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if b.RemoteUnreachableSince.IsZero() {
			t.Fatalf("api%d not stamped unreachable on the first tick", i)
		}
	}

	clock = clock.Add(roundBudgetFor(1000) + unreachableGrace + time.Minute)
	tick()
	for i := 0; i < n; i++ {
		b, err := st.Load(fmt.Sprintf("api%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if b.State != store.StateNeedsYou || b.RemoteHaltKind != store.HaltKindUnreachable {
			t.Fatalf("api%d state=%s kind=%q, want needs_you with the unreachable kind", i, b.State, b.RemoteHaltKind)
		}
	}
	if got := countCalls(fr, "GetBinding:"); got != 2 {
		t.Fatalf("GetBinding calls = %d, want only the first tick's dial and the post-backoff probe", got)
	}
}

func roundBudgetFor(ms int) time.Duration {
	return roundBudget(store.Binding{RoundTimeoutMS: ms})
}
