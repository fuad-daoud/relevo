package relevo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/store/storetest"
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

// testBackoffAt is a backoff on a mutable clock, so a test steps time without
// waiting out the window.
func testBackoffAt(clock *time.Time) *remoteBackoff {
	rb := &remoteBackoff{}
	rb.now = func() time.Time { return *clock }
	return rb
}

func TestBackoffFirstFailureOnlySuspects(t *testing.T) {
	t.Parallel()

	clock := baseTime
	rb := testBackoffAt(&clock)
	rep := rb.recordFailure("zen", errors.New("connection refused"), 1)
	if rep.confirmed || rep.inEpisode || rep.ended {
		t.Fatalf("first failure report = %+v, want no episode transition", rep)
	}
	if rb.skip("zen") {
		t.Fatal("skip = true after one failure, want a suspicion with no backoff")
	}
}

func TestBackoffSecondConsecutiveFailureArmsTheEpisode(t *testing.T) {
	t.Parallel()

	clock := baseTime
	rb := testBackoffAt(&clock)
	opening := errors.New("connection refused")
	rb.recordFailure("zen", opening, 1)
	clock = clock.Add(4 * time.Second)
	if rep := rb.recordFailure("zen", errors.New("connection refused"), 2); rep.confirmed {
		t.Fatalf("hard failures spanning 4s confirmed, want suspicion until one prefetch bound")
	}
	if rb.skip("zen") {
		t.Fatal("skip = true after hard failures spanning 4s, want suspicion")
	}
	clock = baseTime
	rb2 := testBackoffAt(&clock)
	rb2.recordFailure("zen", opening, 1)
	clock = clock.Add(5 * time.Second)
	rep := rb2.recordFailure("zen", errors.New("connection refused"), 2)
	if !rep.confirmed {
		t.Fatalf("second failure report = %+v, want confirmed spanning one prefetch bound", rep)
	}
	if !rep.start.Equal(baseTime) {
		t.Errorf("episode start = %v, want the first failure's time %v", rep.start, baseTime)
	}
	if rep.opening == nil || rep.opening.Error() != opening.Error() {
		t.Errorf("episode opening = %v, want the first failure %v", rep.opening, opening)
	}
	if !rb2.skip("zen") {
		t.Fatal("skip = false after confirming failures, want the backoff armed")
	}
	if got, want := rb2.outages["zen"].until.Sub(clock), blipBackoff; got != want {
		t.Errorf("first armed window = %v, want %v", got, want)
	}
}

func TestBackoffSuccessBetweenFailuresNeverArms(t *testing.T) {
	t.Parallel()

	clock := baseTime
	rb := testBackoffAt(&clock)
	if rep := rb.recordSuccess("zen"); rep.ended {
		t.Fatalf("success with no memory ended an episode: %+v", rep)
	}
	rb.recordFailure("zen", errors.New("connection refused"), 1)
	if rep := rb.recordSuccess("zen"); rep.ended {
		t.Fatalf("clearing a lone suspicion ended an episode: %+v", rep)
	}
	rep := rb.recordFailure("zen", errors.New("connection refused"), 2)
	if rep.confirmed || rep.inEpisode {
		t.Fatalf("failure after a clearing success reports %+v, want a fresh suspicion", rep)
	}
	if rb.skip("zen") {
		t.Fatal("skip = true with successes between failures, want no backoff")
	}
}

func TestBackoffStaleFailureRestartsAtSuspicion(t *testing.T) {
	t.Parallel()

	clock := baseTime
	rb := testBackoffAt(&clock)
	rb.recordFailure("zen", errors.New("connection refused"), 1)
	clock = clock.Add(2*serverBackoff + time.Second)
	rep := rb.recordFailure("zen", errors.New("connection refused"), 2)
	if rep.confirmed || rep.inEpisode {
		t.Fatalf("failure after a long quiet reports %+v, want a fresh suspicion", rep)
	}
	if rb.skip("zen") {
		t.Fatal("skip = true after a long quiet, want the stale memory dropped")
	}
}

func TestBackoffProbeOnRecentEpisodeRearmsSilently(t *testing.T) {
	t.Parallel()

	clock := baseTime
	rb := testBackoffAt(&clock)
	rb.recordFailure("zen", errors.New("connection refused"), 1)
	clock = clock.Add(hardConfirmSpan)
	rep := rb.recordFailure("zen", errors.New("connection refused"), 2)
	if !rep.confirmed {
		t.Fatalf("confirming failure reports %+v, want confirmed", rep)
	}
	clock = clock.Add(blipBackoff + time.Second)
	rep = rb.recordFailure("zen", errors.New("connection refused"), 3)
	if rep.confirmed || !rep.inEpisode || rep.ended {
		t.Fatalf("probe on a recent episode reports %+v, want a silent re-arm", rep)
	}
	if !rep.start.Equal(baseTime) {
		t.Errorf("episode start = %v, want the first failure's time %v", rep.start, baseTime)
	}
	if !rb.skip("zen") {
		t.Fatal("skip = false after the probe, want the backoff re-armed")
	}
	if got, want := rb.outages["zen"].until.Sub(clock), serverBackoff; got != want {
		t.Errorf("re-armed window = %v, want %v", got, want)
	}
}

func TestBackoffSuccessEndsAnArmedEpisode(t *testing.T) {
	t.Parallel()

	clock := baseTime
	rb := testBackoffAt(&clock)
	opening := errors.New("connection refused")
	rb.recordFailure("zen", opening, 1)
	clock = clock.Add(hardConfirmSpan)
	rb.recordFailure("zen", errors.New("connection refused"), 2)
	clock = clock.Add(30 * time.Second)
	rep := rb.recordSuccess("zen")
	if !rep.ended {
		t.Fatalf("success on an armed episode reports %+v, want ended", rep)
	}
	if !rep.start.Equal(baseTime) {
		t.Errorf("episode start = %v, want the first failure's time %v", rep.start, baseTime)
	}
	if rep.opening == nil || rep.opening.Error() != opening.Error() {
		t.Errorf("episode opening = %v, want the first failure %v", rep.opening, opening)
	}
	if got, want := rep.end.Sub(rep.start), hardConfirmSpan+30*time.Second; got != want {
		t.Errorf("episode duration = %v, want %v", got, want)
	}
	if rb.skip("zen") {
		t.Fatal("skip = true after the clearing success, want the backoff gone")
	}
}

func TestBackoffSoftNeedsThreeFailuresOverThirtySeconds(t *testing.T) {
	t.Parallel()

	soft := func() error {
		return fmt.Errorf("%w: %w", client.ErrUnreachable, context.DeadlineExceeded)
	}
	clock := baseTime
	rb := testBackoffAt(&clock)
	rb.recordFailure("zen", soft(), 1)
	clock = clock.Add(29 * time.Second)
	if rep := rb.recordFailure("zen", soft(), 2); rep.confirmed || rep.inEpisode {
		t.Fatalf("two slow failures spanning 29s report %+v, want suspicion", rep)
	}
	if rb.skip("zen") {
		t.Fatal("skip = true after two slow failures, want suspicion")
	}
	clock = clock.Add(time.Second)
	rep := rb.recordFailure("zen", soft(), 3)
	if !rep.confirmed {
		t.Fatalf("third slow failure report = %+v, want confirmed spanning 30s", rep)
	}
	if !rep.start.Equal(baseTime) {
		t.Errorf("episode start = %v, want the first failure's time %v", rep.start, baseTime)
	}
	if got, want := rb.outages["zen"].until.Sub(clock), blipBackoff; got != want {
		t.Errorf("first armed window = %v, want %v", got, want)
	}
	clock = clock.Add(blipBackoff + time.Second)
	rep = rb.recordFailure("zen", soft(), 4)
	if rep.confirmed || !rep.inEpisode {
		t.Fatalf("probe after the short window reports %+v, want a silent re-arm", rep)
	}
	if got, want := rb.outages["zen"].until.Sub(clock), serverBackoff; got != want {
		t.Errorf("re-armed window = %v, want %v", got, want)
	}
}

func TestBackoffThreeSlowFailuresInsideThirtySecondsStaySuspect(t *testing.T) {
	t.Parallel()

	soft := func() error {
		return fmt.Errorf("%w: %w", client.ErrUnreachable, context.DeadlineExceeded)
	}
	clock := baseTime
	rb := testBackoffAt(&clock)
	rb.recordFailure("zen", soft(), 1)
	clock = clock.Add(15 * time.Second)
	rb.recordFailure("zen", soft(), 2)
	clock = clock.Add(14 * time.Second)
	if rep := rb.recordFailure("zen", soft(), 3); rep.confirmed || rep.inEpisode {
		t.Fatalf("three slow failures spanning 29s report %+v, want suspicion", rep)
	}
	if rb.skip("zen") {
		t.Fatal("skip = true after three slow failures inside 30s, want suspicion")
	}
}

func TestBackoffTwoSlowFailuresSpanningThirtySecondsStaySuspect(t *testing.T) {
	t.Parallel()

	// Two slow failures already span the slow rule's window, so only the
	// third counted failure may confirm: count and span stay separate inputs.
	soft := func() error {
		return fmt.Errorf("%w: %w", client.ErrUnreachable, context.DeadlineExceeded)
	}
	clock := baseTime
	rb := testBackoffAt(&clock)
	rb.recordFailure("zen", soft(), 1)
	clock = clock.Add(softConfirmSpan)
	if rep := rb.recordFailure("zen", soft(), 2); rep.confirmed || rep.inEpisode {
		t.Fatalf("two slow failures spanning 30s report %+v, want suspicion", rep)
	}
	if rb.skip("zen") {
		t.Fatal("skip = true after two slow failures spanning 30s, want suspicion")
	}
	rep := rb.recordFailure("zen", soft(), 3)
	if !rep.confirmed {
		t.Fatalf("third slow failure report = %+v, want confirmed spanning 30s", rep)
	}
	if !rep.start.Equal(baseTime) {
		t.Errorf("episode start = %v, want the first failure's time %v", rep.start, baseTime)
	}
}

func TestBackoffSoftThenHardFailureStaysOnTheSlowRule(t *testing.T) {
	t.Parallel()

	// The latch keeps the episode slow once a slow probe opened it: judging
	// only the latest failure would let a refusal confirm on the hard rule.
	hard := errors.New("connection refused")
	soft := fmt.Errorf("%w: %w", client.ErrUnreachable, context.DeadlineExceeded)
	clock := baseTime
	rb := testBackoffAt(&clock)
	rb.recordFailure("zen", soft, 1)
	clock = clock.Add(hardConfirmSpan)
	if rep := rb.recordFailure("zen", hard, 2); rep.confirmed || rep.inEpisode {
		t.Fatalf("one slow plus one refused failure reports %+v, want suspicion on the slow rule", rep)
	}
	if rb.skip("zen") {
		t.Fatal("skip = true after a slow plus a refused failure, want suspicion")
	}
	clock = clock.Add(softConfirmSpan - hardConfirmSpan)
	rep := rb.recordFailure("zen", hard, 3)
	if !rep.confirmed {
		t.Fatalf("mixed failures spanning 30s report %+v, want confirmed on the slow rule", rep)
	}
	if !rep.start.Equal(baseTime) {
		t.Errorf("episode start = %v, want the first failure's time %v", rep.start, baseTime)
	}
}

func TestBackoffSameGenerationCountsOnce(t *testing.T) {
	t.Parallel()

	clock := baseTime
	rb := testBackoffAt(&clock)
	rb.recordFailure("zen", errors.New("connection refused"), 7)
	if rep := rb.recordFailure("zen", errors.New("connection refused"), 7); rep.confirmed || rep.inEpisode {
		t.Fatalf("second failure in the same generation reports %+v, want deduped suspicion", rep)
	}
	if !rb.countedIn("zen", 7) {
		t.Fatal("countedIn = false for the generation that counted, want true")
	}
	if rb.countedIn("zen", 8) {
		t.Fatal("countedIn = true for a fresh generation, want false")
	}
}

func TestBackoffMixedFailuresConfirmOnTheSlowRule(t *testing.T) {
	t.Parallel()

	hard := errors.New("connection refused")
	soft := fmt.Errorf("%w: %w", client.ErrUnreachable, context.DeadlineExceeded)
	clock := baseTime
	rb := testBackoffAt(&clock)
	rb.recordFailure("zen", hard, 1)
	clock = clock.Add(hardConfirmSpan)
	if rep := rb.recordFailure("zen", soft, 2); rep.confirmed {
		t.Fatalf("one hard plus one slow failure reports %+v, want suspicion on the slow rule", rep)
	}
	clock = clock.Add(softConfirmSpan - hardConfirmSpan)
	if rep := rb.recordFailure("zen", hard, 3); !rep.confirmed {
		t.Fatalf("mixed failures spanning 30s report %+v, want confirmed on the slow rule", rep)
	}
}

func TestPrefetchOfSilentServerFailsWithinDeadline(t *testing.T) {
	shortenPrefetchDeadline(t, 100*time.Millisecond)
	st := storetest.New(t)
	sr := &silentRemote{fakeRemote: &fakeRemote{}}
	d := NewDaemon(Runtime{Store: st, Remote: sr, Now: func() time.Time { return baseTime }}, time.Second)

	start := time.Now()
	pre := d.prefetchRemote(context.Background(), remoteBinding("zen"), 0)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("prefetch took %v against a server that never answers", took)
	}
	if pre == nil || pre.Err == nil {
		t.Fatalf("prefetch = %+v, want the failed read", pre)
	}
}

func TestTickDialsFailedServerOncePerTick(t *testing.T) {
	st := storetest.New(t)
	saveRemoteBindings(t, st, "zen", 4)
	fr := unreachableRemote()
	clock := baseTime
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return clock }}

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)

	d := NewDaemon(rt, time.Second)
	d.backoff.now = func() time.Time { return clock }
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// One slow tick counts one failure: a single dial, every binding left
	// alone.
	if n := countCalls(fr, "GetBinding:"); n != 1 {
		t.Fatalf("GetBinding calls = %d, want 1: one dial per tick", n)
	}
	for i := 0; i < 4; i++ {
		name := fmt.Sprintf("api%d", i)
		b, err := st.Load(name)
		if err != nil {
			t.Fatal(err)
		}
		if !b.RemoteUnreachableSince.IsZero() {
			t.Errorf("%s stamped after one failure, want no binding flipped that tick", name)
		}
		if b.State != store.StateActive {
			t.Errorf("%s state = %s, an outage inside the grace must not halt", name, b.State)
		}
	}
	if n := strings.Count(logged.String(), "level=WARN"); n != 0 {
		t.Errorf("WARN lines = %d, want none before confirmation, got:\n%s", n, logged.String())
	}

	clock = clock.Add(hardConfirmSpan)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := countCalls(fr, "GetBinding:"); n != 2 {
		t.Fatalf("GetBinding calls = %d, want 2: suspicion then hard confirmation", n)
	}
	for i := 0; i < 4; i++ {
		name := fmt.Sprintf("api%d", i)
		b, err := st.Load(name)
		if err != nil {
			t.Fatal(err)
		}
		if !b.RemoteUnreachableSince.Equal(baseTime) {
			t.Errorf("%s stamp = %v, want the first failure's time %v", name, b.RemoteUnreachableSince, baseTime)
		}
	}
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := countCalls(fr, "GetBinding:"); n != 2 {
		t.Fatalf("GetBinding calls = %d, want still 2: the armed episode skips every dial", n)
	}
	if n := strings.Count(logged.String(), "level=WARN"); n != 1 {
		t.Errorf("WARN lines = %d, want exactly one per episode, got:\n%s", n, logged.String())
	}
}

func TestTickBacksOffFailedServerAcrossTicks(t *testing.T) {
	st := storetest.New(t)
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
	clock = clock.Add(hardConfirmSpan)
	tick()
	if n := countCalls(fr, "GetBinding:"); n != 2 {
		t.Fatalf("GetBinding calls after two ticks = %d, want 2: suspicion then hard confirmation", n)
	}

	clock = clock.Add(blipBackoff + time.Second)
	tick()
	if n := countCalls(fr, "GetBinding:"); n != 3 {
		t.Fatalf("GetBinding calls after the short window expired = %d, want one probe more", n)
	}
	tick()
	if n := countCalls(fr, "GetBinding:"); n != 3 {
		t.Fatalf("a failed probe must re-arm the backoff, got %d calls", n)
	}

	fr.getBindingErr = nil
	clock = clock.Add(serverBackoff + time.Second)
	tick()
	tick()
	if n := countCalls(fr, "GetBinding:"); n != 7 {
		t.Fatalf("GetBinding calls = %d, want a successful probe to end the backoff (both bindings dialled)", n)
	}
}

func TestTickDoesNotPrefetchDoneBindings(t *testing.T) {
	st := storetest.New(t)
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
		t.Fatalf("GetBinding calls = %d, want one bounded dial for five bindings in one generation", n)
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
	st := storetest.New(t)
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
		name        string
		err         error
		wantUnreach bool
		wantSoft    bool
	}{
		{"wrapped deadline", fmt.Errorf("%w: %w", client.ErrUnreachable, context.DeadlineExceeded), true, true},
		{"bare deadline", context.DeadlineExceeded, true, true},
		{"refused", fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable), true, false},
		{"dns", fmt.Errorf("%w: %w", client.ErrUnreachable, &net.DNSError{Err: "no such host", Name: "zen"}), true, false},
		{"gateway 503", fmt.Errorf("%w: %w", client.ErrUnreachable, &client.GatewayError{Status: 503, Text: "Service Unavailable"}), true, true},
		{"wrapped unreachable", fmt.Errorf("%w: refused", client.ErrUnreachable), true, false},
		{"404", &client.HTTPError{Status: 404}, false, false},
		{"401", &client.HTTPError{Status: 401}, false, false},
		{"unknown server", client.ErrUnknownServer, false, false},
		{"other", errors.New("boom"), false, false},
	}
	for _, c := range cases {
		if got := serverUnreachable(c.err); got != c.wantUnreach {
			t.Errorf("%s: serverUnreachable = %v, want %v", c.name, got, c.wantUnreach)
		}
		if got := failureSoft(c.err); got != c.wantSoft {
			t.Errorf("%s: failureSoft = %v, want %v", c.name, got, c.wantSoft)
		}
	}
}

func TestTickSilentServerStampsAndHaltsEveryOpenRound(t *testing.T) {
	st := storetest.New(t)
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

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)

	tick := func() {
		t.Helper()
		if err := d.Tick(context.Background()); err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}
	// The first tick suspects with one dial; the next tick past one prefetch
	// bound confirms, and the same tick's skips stamp every binding.
	tick()
	clock = clock.Add(hardConfirmSpan)
	tick()
	if got := countCalls(fr, "GetBinding:"); got != 2 {
		t.Fatalf("GetBinding calls = %d, want the suspicion and the confirmation only", got)
	}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("api%d", i)
		b, err := st.Load(name)
		if err != nil {
			t.Fatal(err)
		}
		if !b.RemoteUnreachableSince.Equal(baseTime) {
			t.Fatalf("%s stamp = %v, want the episode start %v", name, b.RemoteUnreachableSince, baseTime)
		}
		if b.State != store.StateActive {
			t.Fatalf("%s state = %s, an outage inside the grace must not halt", name, b.State)
		}
	}

	// A single jump past twice the backoff would start a fresh suspicion by
	// design, so the clock moves a minute at a time instead: every tick probes
	// once and re-arms, and every stamp stays the episode start. The steps run
	// one minute past the budget plus the grace.
	steps := int(roundBudgetFor(1000)+unreachableGrace)/int(time.Minute) + 1
	for i := 0; i < steps; i++ {
		clock = clock.Add(time.Minute)
		tick()
	}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("api%d", i)
		b, err := st.Load(name)
		if err != nil {
			t.Fatal(err)
		}
		if b.State != store.StateNeedsYou || b.RemoteHaltKind != store.HaltKindUnreachable {
			t.Fatalf("%s state=%s kind=%q, want needs_you with the unreachable kind", name, b.State, b.RemoteHaltKind)
		}
		if !b.RemoteUnreachableSince.Equal(baseTime) {
			t.Errorf("%s stamp = %v, want the episode start %v", name, b.RemoteUnreachableSince, baseTime)
		}
	}
	if got, want := countCalls(fr, "GetBinding:"), 2+steps; got != want {
		t.Fatalf("GetBinding calls = %d, want %d: the first two ticks' dials plus one probe per minute step", got, want)
	}
	if got := strings.Count(logged.String(), "level=WARN"); got != 1 {
		t.Errorf("WARN lines = %d, want exactly one per episode, got:\n%s", got, logged.String())
	}
}

func TestTickSingleFailureLeavesServerReachable(t *testing.T) {
	shortenPrefetchDeadline(t, 100*time.Millisecond)
	st := storetest.New(t)
	saveRemoteBindings(t, st, "zen", 1)
	sr := &silentRemote{fakeRemote: &fakeRemote{}}
	clock := baseTime
	rt := Runtime{Store: st, Remote: sr, Now: func() time.Time { return clock }}
	d := NewDaemon(rt, time.Second)
	d.backoff.now = func() time.Time { return clock }

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)

	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	got, err := st.Load("api0")
	if err != nil {
		t.Fatal(err)
	}
	if got.Builder.RemoteStatus == "unreachable" || !got.RemoteUnreachableSince.IsZero() {
		t.Fatalf("status %q since %v after one failure, want no outage",
			got.Builder.RemoteStatus, got.RemoteUnreachableSince)
	}
	if d.backoff.skip("zen") {
		t.Fatal("skip = true after one failure, want no backoff")
	}
	if out := logged.String(); !strings.Contains(out, "http_response=false") {
		t.Errorf("first-failure line misses http_response=false for a deadline, got:\n%s", out)
	}

	// Two slow reads inside 30s stay suspect: the stamp stays zero and no
	// backoff arms.
	clock = clock.Add(time.Second)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	got, err = st.Load("api0")
	if err != nil {
		t.Fatal(err)
	}
	if !got.RemoteUnreachableSince.IsZero() {
		t.Errorf("stamp = %v after two slow failures inside 30s, want zero", got.RemoteUnreachableSince)
	}
	if d.backoff.skip("zen") {
		t.Error("skip = true after two slow failures, want suspicion")
	}
	// The third slow read spanning 30s confirms: the stamp is the first
	// failure's time, not the third tick's.
	clock = baseTime.Add(softConfirmSpan)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	got, err = st.Load("api0")
	if err != nil {
		t.Fatal(err)
	}
	if !got.RemoteUnreachableSince.Equal(baseTime) {
		t.Errorf("stamp = %v, want the first failure's time %v", got.RemoteUnreachableSince, baseTime)
	}
	if !d.backoff.skip("zen") {
		t.Error("skip = false after three slow failures spanning 30s, want the backoff armed")
	}
}

func TestTickFlappingServerNeverBacksOff(t *testing.T) {
	st := storetest.New(t)
	saveRemoteBindings(t, st, "zen", 1)
	flap := fmt.Errorf("%w: connection refused", client.ErrUnreachable)
	fr := &fakeRemote{getBindingErr: flap}
	clock := baseTime
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return clock }}
	d := NewDaemon(rt, time.Second)
	d.backoff.now = func() time.Time { return clock }

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)

	tick := func() {
		t.Helper()
		if err := d.Tick(context.Background()); err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}
	tick()
	fr.getBindingErr = nil
	tick()
	fr.getBindingErr = flap
	tick()

	got, err := st.Load("api0")
	if err != nil {
		t.Fatal(err)
	}
	if got.Builder.RemoteStatus == "unreachable" || !got.RemoteUnreachableSince.IsZero() {
		t.Fatalf("status %q since %v after fail-success-fail, want no outage",
			got.Builder.RemoteStatus, got.RemoteUnreachableSince)
	}
	if d.backoff.skip("zen") {
		t.Fatal("skip = true after fail-success-fail, want no backoff")
	}
	if n := strings.Count(logged.String(), "level=WARN"); n != 0 {
		t.Errorf("WARN lines = %d, want none without a confirmation, got:\n%s", n, logged.String())
	}
	if n := strings.Count(logged.String(), "remote prefetch failed"); n != 2 {
		t.Errorf("first-failure lines = %d, want one per blip, got:\n%s", n, logged.String())
	}
}

func TestTickEpisodeLogsOneWarnAndOneInfoOnRecovery(t *testing.T) {
	st := storetest.New(t)
	saveRemoteBindings(t, st, "zen", 3)
	fr := unreachableRemote()
	clock := baseTime
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return clock }}
	d := NewDaemon(rt, time.Second)
	d.backoff.now = func() time.Time { return clock }

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)

	tick := func() {
		t.Helper()
		if err := d.Tick(context.Background()); err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}
	tick()
	clock = clock.Add(hardConfirmSpan)
	tick()
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("api%d", i)
		b, err := st.Load(name)
		if err != nil {
			t.Fatal(err)
		}
		if !b.RemoteUnreachableSince.Equal(baseTime) {
			t.Fatalf("%s stamp = %v, want the episode start %v", name, b.RemoteUnreachableSince, baseTime)
		}
	}
	if n := strings.Count(logged.String(), "level=WARN"); n != 1 {
		t.Fatalf("WARN lines = %d, want exactly one per episode, got:\n%s", n, logged.String())
	}
	for _, line := range strings.Split(logged.String(), "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "binding=") {
			t.Errorf("per-binding WARN line, want one server line only: %s", line)
		}
	}

	logged.Reset()
	clock = clock.Add(blipBackoff + time.Second)
	fr.getBindingErr = nil
	fr.getBindingResp = remote.BindingView{RoundState: remote.RoundRunning}
	tick()
	if n := strings.Count(logged.String(), "level=INFO"); n != 1 {
		t.Fatalf("INFO lines = %d, want exactly one on recovery, got:\n%s", n, logged.String())
	}
	if out := logged.String(); !strings.Contains(out, "duration=") || !strings.Contains(out, "connection refused") {
		t.Errorf("recovery INFO names the duration and the opening error, got:\n%s", out)
	}
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("api%d", i)
		b, err := st.Load(name)
		if err != nil {
			t.Fatal(err)
		}
		if !b.RemoteUnreachableSince.IsZero() {
			t.Errorf("%s stamp = %v, want the recovery to clear it", name, b.RemoteUnreachableSince)
		}
	}
}

func roundBudgetFor(ms int) time.Duration {
	return roundBudget(store.Binding{RoundTimeoutMS: ms})
}

// The backoff's clock reads local time, and every other stamp on a binding is
// UTC, so the outage's start is normalized as it lands.
func TestTickStampsTheOutageStartInUTC(t *testing.T) {
	st := storetest.New(t)
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Store: st, Remote: unreachableRemote(), Now: func() time.Time { return baseTime }}
	d := NewDaemon(rt, time.Second)
	east := baseTime.In(time.FixedZone("east", 3*60*60))
	d.backoff.now = func() time.Time { return east }

	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	east = east.Add(hardConfirmSpan)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, err := st.Load(remoteBinding("zen").Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.RemoteUnreachableSince.IsZero() {
		t.Fatal("no outage stamped after two failed reads")
	}
	if _, offset := got.RemoteUnreachableSince.Zone(); offset != 0 {
		t.Errorf("RemoteUnreachableSince = %v, want UTC", got.RemoteUnreachableSince)
	}
}

func TestTickDeadlineOnceThenAnswersNeverFlips(t *testing.T) {
	st := storetest.New(t)
	saveRemoteBindings(t, st, "zen", 3)
	fr := &fakeRemote{
		deadlineFails:  1,
		getBindingResp: remote.BindingView{RoundState: remote.RoundRunning},
	}
	clock := baseTime
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return clock }}
	d := NewDaemon(rt, time.Second)
	d.backoff.now = func() time.Time { return clock }

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)

	tick := func() {
		t.Helper()
		if err := d.Tick(context.Background()); err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}
	tick()
	if n := countCalls(fr, "GetBinding:"); n != 1 {
		t.Fatalf("GetBinding calls after the blip tick = %d, want 1: one dial per generation", n)
	}
	for i := 0; i < 3; i++ {
		b, err := st.Load(fmt.Sprintf("api%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if !b.RemoteUnreachableSince.IsZero() || b.Builder.RemoteStatus == "unreachable" {
			t.Fatalf("%s flipped after one slow read, want no outage", b.Name)
		}
	}
	tick()
	tick()
	for i := 0; i < 3; i++ {
		b, err := st.Load(fmt.Sprintf("api%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if !b.RemoteUnreachableSince.IsZero() || b.Builder.RemoteStatus == "unreachable" {
			t.Fatalf("%s flipped after the server answered, want no outage", b.Name)
		}
	}
	if n := strings.Count(logged.String(), "level=WARN"); n != 0 {
		t.Errorf("WARN lines = %d, want none for one slow blip, got:\n%s", n, logged.String())
	}
}

func TestTickAlwaysDeadlineDeclaresAtSoftBound(t *testing.T) {
	st := storetest.New(t)
	const n = 3
	for i := 0; i < n; i++ {
		b := remoteBinding("zen")
		b.Name = fmt.Sprintf("api%d", i)
		b.RoundTimeoutMS = 1000
		if err := st.Save(b); err != nil {
			t.Fatal(err)
		}
		if err := st.WithLock(func(tx *store.Tx) error {
			return tx.AppendLog(b.Name, store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt})
		}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := fmt.Errorf("%w: %w", client.ErrUnreachable, context.DeadlineExceeded)
	fr := &fakeRemote{getBindingErr: deadline}
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
		if !b.RemoteUnreachableSince.IsZero() {
			t.Fatalf("%s stamped after one slow failure, want suspicion", b.Name)
		}
	}
	clock = clock.Add(15 * time.Second)
	tick()
	for i := 0; i < n; i++ {
		b, err := st.Load(fmt.Sprintf("api%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if !b.RemoteUnreachableSince.IsZero() {
			t.Fatalf("%s stamped after two slow failures, want suspicion until 30s", b.Name)
		}
	}
	clock = clock.Add(15 * time.Second)
	tick()
	if got := countCalls(fr, "GetBinding:"); got != 3 {
		t.Fatalf("GetBinding calls = %d, want 3: one dial per generation", got)
	}
	for i := 0; i < n; i++ {
		b, err := st.Load(fmt.Sprintf("api%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if !b.RemoteUnreachableSince.Equal(baseTime) {
			t.Fatalf("%s stamp = %v, want the first failure's time %v", b.Name, b.RemoteUnreachableSince, baseTime)
		}
		if b.State != store.StateActive {
			t.Fatalf("%s state = %s, want active inside the grace", b.Name, b.State)
		}
	}
	steps := int(roundBudgetFor(1000)+unreachableGrace)/int(time.Minute) + 1
	for i := 0; i < steps; i++ {
		clock = clock.Add(time.Minute)
		tick()
	}
	for i := 0; i < n; i++ {
		b, err := st.Load(fmt.Sprintf("api%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if b.State != store.StateNeedsYou || b.RemoteHaltKind != store.HaltKindUnreachable {
			t.Fatalf("%s state=%s kind=%q, want needs_you with the unreachable kind", b.Name, b.State, b.RemoteHaltKind)
		}
		if !b.RemoteUnreachableSince.Equal(baseTime) {
			t.Errorf("%s stamp = %v, want the episode start %v", b.Name, b.RemoteUnreachableSince, baseTime)
		}
	}
}

func TestTickAlwaysRefusedDeclaresAtHardBound(t *testing.T) {
	st := storetest.New(t)
	saveRemoteBindings(t, st, "zen", 3)
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
	for i := 0; i < 3; i++ {
		b, err := st.Load(fmt.Sprintf("api%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if !b.RemoteUnreachableSince.IsZero() {
			t.Fatalf("%s stamped after one refused dial, want suspicion", b.Name)
		}
	}
	clock = clock.Add(hardConfirmSpan)
	tick()
	if got := countCalls(fr, "GetBinding:"); got != 2 {
		t.Fatalf("GetBinding calls = %d, want 2: suspicion then hard confirmation", got)
	}
	for i := 0; i < 3; i++ {
		b, err := st.Load(fmt.Sprintf("api%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if !b.RemoteUnreachableSince.Equal(baseTime) {
			t.Fatalf("%s stamp = %v, want the first failure's time %v", b.Name, b.RemoteUnreachableSince, baseTime)
		}
	}
}
