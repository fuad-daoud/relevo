package relevo

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// fakeWaitClaims is the map-backed delivery.WaitClaimStore the wait-liveness
// tests use, the wait-side twin of fakeClaimStore: a registration present for
// a binding name means live, absent means not. kept records every write and
// removal in order, so a test can pin that the registration is refreshed while
// the poll runs and dropped when it exits.
type fakeWaitClaims struct {
	held    map[string]delivery.WaitClaim
	events  []string
	removed []string
}

func newFakeWaitClaims() *fakeWaitClaims {
	return &fakeWaitClaims{held: map[string]delivery.WaitClaim{}}
}

func (f *fakeWaitClaims) Live(name string, _ time.Time) (*delivery.WaitClaim, error) {
	c, ok := f.held[name]
	if !ok {
		return nil, nil
	}
	return &c, nil
}

func (f *fakeWaitClaims) Write(c delivery.WaitClaim, _ time.Time) error {
	f.held[c.Name] = c
	f.events = append(f.events, "write:"+c.Name)
	return nil
}

func (f *fakeWaitClaims) Remove(name string, pid int) error {
	if c, ok := f.held[name]; ok && c.PID == pid {
		delete(f.held, name)
		f.removed = append(f.removed, name)
	}
	f.events = append(f.events, "remove:"+name)
	return nil
}

func (f *fakeWaitClaims) live(name string) bool {
	_, ok := f.held[name]
	return ok
}

// waitLivenessRuntime is a Runtime with a wait store wired and the wait seams
// driven by hand, so the registration's whole lifecycle runs without sleeping.
func waitLivenessRuntime(t *testing.T, waits *fakeWaitClaims) Runtime {
	t.Helper()
	rt := routeRuntime(t)
	rt.Waits = waits
	return rt
}

// seedOpenRound saves an active binding whose round 1 was sent but has not
// closed, so a wait polls it instead of returning on the first pass.
func seedOpenRound(t *testing.T, rt Runtime, name string) {
	t.Helper()
	b := store.Binding{
		Name:         name,
		CWD:          "/repo/" + name,
		Round:        1,
		State:        store.StateActive,
		MasterMind:   store.Endpoint{Kind: "claude", SessionID: "sess"},
		MasterMindID: testClaimMasterMind,
		Builder:      store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(b); err != nil {
			return err
		}
		return tx.AppendLog(name, store.LogEntry{
			TS: baseTime, Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt,
		})
	}); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

// TestWaitRegistersAndReleases pins that a wait records itself for each name it
// polls while it runs, and leaves no registration behind when it exits. The
// release matters as much as the register: a row left behind would read as a
// live wait for as long as its TTL, and every payload on that binding would
// stay off NEEDS YOU for that window after the wait was gone.
func TestWaitRegistersAndReleases(t *testing.T) {
	waits := newFakeWaitClaims()
	rt := waitLivenessRuntime(t, waits)
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")

	// The round is already closed by the seeded report, so the wait returns on
	// its first pass -- which is exactly the shape that must still have
	// registered on the way in and released on the way out.
	_, _, err := Wait(context.Background(), rt, WaitOptions{
		Names: []string{"webshop"}, Round: 1,
		Timeout: time.Second, Interval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if len(waits.events) == 0 || waits.events[0] != "write:webshop" {
		t.Errorf("events = %v, want a registration written before the first poll", waits.events)
	}
	if len(waits.removed) != 1 || waits.removed[0] != "webshop" {
		t.Errorf("removed = %v, want exactly one removal of webshop on exit", waits.removed)
	}
	if waits.live("webshop") {
		t.Error("registration still held after Wait returned, want it released")
	}
}

// TestWaitRefreshesWhilePolling pins the other half of liveness: the
// registration is re-stamped on each pass, not written once and left to age
// out. A wait whose whole run stayed inside one TTL would pass without it.
func TestWaitRefreshesWhilePolling(t *testing.T) {
	waits := newFakeWaitClaims()
	rt := waitLivenessRuntime(t, waits)
	seedOpenRound(t, rt, "webshop")

	// The clock the wait reads advances a step per call, so the poll loop runs
	// several passes and then leaves on its own timeout rather than on a signal.
	clock := baseTime
	polls := 0
	_, res, err := Wait(context.Background(), rt, WaitOptions{
		Names: []string{"webshop"}, Round: 1,
		Timeout: time.Minute, Interval: time.Millisecond,
		// One step per call is what a real poll clock does; the step is a
		// tenth of a second so a minute of poll time is a few hundred passes
		// rather than a million.
		now: func() time.Time {
			clock = clock.Add(100 * time.Millisecond)
			return clock
		},
		sleep: func(context.Context, time.Duration) error {
			polls++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Code != WaitTimeout {
		t.Errorf("exit code = %d, want WaitTimeout so the loop ran to its timeout", res.Code)
	}
	if polls < 3 {
		t.Fatalf("polls = %d, want the loop to run several passes before timing out", polls)
	}

	// One write before the loop plus one per pass: several writes, not one.
	if len(waits.events) < polls+1 {
		t.Errorf("events = %v over %d polls, want a refresh on each pass", waits.events, polls)
	}
}

// TestWaitKeepsRegistrationLiveDuringSlowSync pins that the registration is
// re-stamped while a remote sync runs, not only between passes. A single sync
// is allowed longer than the registration's whole TTL, so a wait that refreshes
// once per pass leaves the row looking dead while it is plainly still polling --
// and the statusline then escalates a payload that is about to be collected.
//
// The remote fetch blocks until a second write reaches the wait store, so the
// re-stamp can only have come from the ticker running under the sync.
func TestWaitKeepsRegistrationLiveDuringSlowSync(t *testing.T) {
	waits := newFakeWaitClaims()
	rt := waitLivenessRuntime(t, waits)
	seedOpenRound(t, rt, "webshop")

	// The binding must be a remote one for the wait's sync to have anything to
	// fetch. The first fetch holds its pass open until the ticker has written
	// again, and checks from inside that open pass that the row still reads live:
	// a re-stamp that only landed between passes could not be seen from here.
	var checked bool
	fr := &fakeRemote{beforeCall: func(call string) {
		if checked || !strings.HasPrefix(call, "GetBinding:") {
			return
		}
		checked = true
		deadline := time.After(10 * time.Second)
		for len(waits.events) < 2 {
			select {
			case <-deadline:
				t.Error("no re-stamp reached the wait store during the sync")
				return
			case <-time.After(time.Millisecond):
			}
		}
		if _, err := waits.Live("webshop", baseTime); err != nil {
			t.Errorf("Live during the sync: %v", err)
		}
		if !waits.live("webshop") {
			t.Error("registration reads as dead while the wait is still syncing")
		}
	}}
	rt.Remote = fr
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.Save(store.Binding{
			Name:       "webshop",
			CWD:        "/repo/webshop",
			Round:      1,
			State:      store.StateActive,
			Builder:    store.Endpoint{Mode: store.ModeRemote, Server: "zen"},
			MasterMind: store.Endpoint{Kind: "claude", SessionID: "sess"},
		})
	}); err != nil {
		t.Fatal(err)
	}

	// A tick of a millisecond, so the ticker fires many times over a fetch that
	// waits on real wall-clock time rather than the injected poll clock. The
	// poll clock still advances a step per call, or the loop never reaches its
	// timeout and the test runs forever.
	clock := baseTime
	_, res, err := Wait(context.Background(), rt, WaitOptions{
		Names: []string{"webshop"}, Round: 1,
		Timeout: 20 * time.Second, Interval: time.Millisecond,
		syncRefresh: time.Millisecond,
		now:         func() time.Time { clock = clock.Add(100 * time.Millisecond); return clock },
		sleep:       func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Code != WaitTimeout {
		t.Errorf("exit code = %d, want WaitTimeout so the loop ran to its timeout", res.Code)
	}

	// The write the fetch waited for is the pin: one registration before the
	// loop, and at least one more while the sync was still in flight.
	if len(waits.events) < 2 {
		t.Errorf("events = %v, want a re-stamp while the sync ran", waits.events)
	}
}

// TestWaitWithNoStoreStillWaits pins that a Runtime with no wait store wired is
// not a failure. The registration is a fact for the status row to read, not a
// precondition for collecting the payload, so a host without the database must
// still wait.
func TestWaitWithNoStoreStillWaits(t *testing.T) {
	rt := routeRuntime(t)
	rt.Waits = nil
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")

	res := WaitOutcome(store.Binding{Round: 1}, []store.LogEntry{{
		Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt,
	}}, 1, func(string, int) string { return "" })
	if res.Done {
		t.Fatalf("WaitOutcome = %+v, want the round still open", res)
	}

	if _, _, err := Wait(context.Background(), rt, WaitOptions{
		Names: []string{"webshop"}, Round: 1,
		Timeout: time.Millisecond, Interval: time.Millisecond,
	}); err != nil {
		t.Fatalf("Wait with no wait store: %v", err)
	}
}

// TestStatusRowWaitLive pins that statusRow carries the store's liveness onto
// the row, and that it distinguishes a live wait from an absent one -- the
// distinction the graced pull rule reads.
func TestStatusRowWaitLive(t *testing.T) {
	t.Run("a registered wait reads as live", func(t *testing.T) {
		waits := newFakeWaitClaims()
		rt := waitLivenessRuntime(t, waits)
		b := seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
		waits.held[b.Name] = delivery.WaitClaim{Name: b.Name, PID: os.Getpid(), SeenAt: baseTime}

		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if len(rep.Bindings) != 1 {
			t.Fatalf("status has %d rows, want 1", len(rep.Bindings))
		}
		if !rep.Bindings[0].WaitLive {
			t.Error("WaitLive = false, want true for a binding a wait is polling")
		}
	})

	t.Run("no registration reads as not live", func(t *testing.T) {
		waits := newFakeWaitClaims()
		rt := waitLivenessRuntime(t, waits)
		seedPending(t, rt, "webshop", testClaimMasterMind, "claude")

		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if rep.Bindings[0].WaitLive {
			t.Error("WaitLive = true, want false when nobody is waiting on the binding")
		}
	})

	// The deliberate direction: a store that cannot answer must not hold back an
	// escalation, so an error reads as not live rather than as a guess.
	t.Run("an unreadable store reads as not live", func(t *testing.T) {
		rt := routeRuntime(t)
		rt.Waits = errorWaitClaims{}
		seedPending(t, rt, "webshop", testClaimMasterMind, "claude")

		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if rep.Bindings[0].WaitLive {
			t.Error("WaitLive = true on a read error, want false: the row must still escalate")
		}
	})

	t.Run("a runtime with no wait store reads as not live", func(t *testing.T) {
		rt := routeRuntime(t)
		seedPending(t, rt, "webshop", testClaimMasterMind, "claude")

		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if rep.Bindings[0].WaitLive {
			t.Error("WaitLive = true with no wait store, want false")
		}
	})
}

// TestWaitPeekDoesNotRegister: a --peek wait never collects, so it
// must not register as a live wait. The registration is the status row's one
// fact about whether a payload is about to be collected, and a peeking script or
// loop holding one would keep a stranded pull-route report at REPORT IN instead
// of NEEDS YOU, for as long as it keeps peeking.
func TestWaitPeekDoesNotRegister(t *testing.T) {
	waits := newFakeWaitClaims()
	rt := waitLivenessRuntime(t, waits)
	seedOpenRound(t, rt, "webshop")

	clock := baseTime
	_, res, err := Wait(context.Background(), rt, WaitOptions{
		Names: []string{"webshop"}, Round: 1, Peek: true,
		Timeout: time.Minute, Interval: time.Millisecond,
		now:   func() time.Time { clock = clock.Add(100 * time.Millisecond); return clock },
		sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("Wait --peek: %v", err)
	}
	if res.Code != WaitTimeout {
		t.Errorf("exit code = %d, want WaitTimeout so the loop ran several passes", res.Code)
	}
	if waits.live("webshop") {
		t.Error("a peek wait is held as live, want no registration at all")
	}
	if len(waits.events) != 0 {
		t.Errorf("events = %v, want no write and no remove from a peek wait", waits.events)
	}
}

// TestPeekWaitDoesNotSuppressNeedsYou is the end of that chain: a peek wait
// plus a pending pull payload past the grace reads NEEDS YOU. The peek runs on
// the binding and registers nothing, so the
// status row reports no live wait, and the graced pull arm has nothing to hold
// the row back -- the same row with a collecting wait registered stays REPORT IN.
func TestPeekWaitDoesNotSuppressNeedsYou(t *testing.T) {
	waits := newFakeWaitClaims()
	rt := waitLivenessRuntime(t, waits)
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")

	// The round is closed by the seeded report, so the peek resolves on its
	// first pass -- which is exactly the shape that must still not register.
	if _, _, err := Wait(context.Background(), rt, WaitOptions{
		Names: []string{"webshop"}, Round: 1, Peek: true,
		Timeout: time.Millisecond, Interval: time.Millisecond,
	}); err != nil {
		t.Fatalf("Wait --peek: %v", err)
	}

	// The pending payload is the report the peek left behind. Age it past the
	// pull grace: Pending.TS is the payload's own stamp, so the row is aged by
	// advancing the clock the statusline reads rather than by rewriting state.
	// Thirty minutes is well past the two-minute grace and is the same age
	// TestStatusLineRowsPullGrace uses for its escalating fixture.
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 1 {
		t.Fatalf("status has %d rows, want 1", len(rep.Bindings))
	}
	row := rep.Bindings[0]
	if row.WaitLive {
		t.Fatal("WaitLive = true after a peek wait, want false: only a collecting wait registers")
	}
	if row.MasterMindRoute != "pull" {
		t.Fatalf("MasterMindRoute = %q, want pull for the fixture", row.MasterMindRoute)
	}
	if row.Pending == nil {
		t.Fatal("Pending = nil, want the peeked payload still pending")
	}

	rows := view.StatusLineRows(rep, row.Pending.TS.Add(30*time.Minute))
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if !rows[0].NeedsYou {
		t.Errorf("NeedsYou = %v, want true: a peeking loop must not hide a stranded payload", rows[0].NeedsYou)
	}
}

// errorWaitClaims is a wait store whose every read fails.
type errorWaitClaims struct{}

func (errorWaitClaims) Live(string, time.Time) (*delivery.WaitClaim, error) {
	return nil, errors.New("kv unavailable")
}
func (errorWaitClaims) Write(delivery.WaitClaim, time.Time) error { return nil }
func (errorWaitClaims) Remove(string, int) error                  { return nil }
