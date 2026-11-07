package relevo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
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
	rep := rb.recordFailure("zen", errors.New("connection refused"))
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
	rb.recordFailure("zen", opening)
	rep := rb.recordFailure("zen", errors.New("connection refused"))
	if !rep.confirmed {
		t.Fatalf("second failure report = %+v, want confirmed", rep)
	}
	if !rep.start.Equal(baseTime) {
		t.Errorf("episode start = %v, want the first failure's time %v", rep.start, baseTime)
	}
	if rep.opening == nil || rep.opening.Error() != opening.Error() {
		t.Errorf("episode opening = %v, want the first failure %v", rep.opening, opening)
	}
	if !rb.skip("zen") {
		t.Fatal("skip = false after two consecutive failures, want the backoff armed")
	}
}

func TestBackoffSuccessBetweenFailuresNeverArms(t *testing.T) {
	t.Parallel()

	clock := baseTime
	rb := testBackoffAt(&clock)
	if rep := rb.recordSuccess("zen"); rep.ended {
		t.Fatalf("success with no memory ended an episode: %+v", rep)
	}
	rb.recordFailure("zen", errors.New("connection refused"))
	if rep := rb.recordSuccess("zen"); rep.ended {
		t.Fatalf("clearing a lone suspicion ended an episode: %+v", rep)
	}
	rep := rb.recordFailure("zen", errors.New("connection refused"))
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
	rb.recordFailure("zen", errors.New("connection refused"))
	clock = clock.Add(2*serverBackoff + time.Second)
	rep := rb.recordFailure("zen", errors.New("connection refused"))
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
	rb.recordFailure("zen", errors.New("connection refused"))
	rb.recordFailure("zen", errors.New("connection refused"))
	clock = clock.Add(serverBackoff + time.Second)
	rep := rb.recordFailure("zen", errors.New("connection refused"))
	if rep.confirmed || !rep.inEpisode || rep.ended {
		t.Fatalf("probe on a recent episode reports %+v, want a silent re-arm", rep)
	}
	if !rep.start.Equal(baseTime) {
		t.Errorf("episode start = %v, want the first failure's time %v", rep.start, baseTime)
	}
	if !rb.skip("zen") {
		t.Fatal("skip = false after the probe, want the backoff re-armed")
	}
}

func TestBackoffSuccessEndsAnArmedEpisode(t *testing.T) {
	t.Parallel()

	clock := baseTime
	rb := testBackoffAt(&clock)
	opening := errors.New("connection refused")
	rb.recordFailure("zen", opening)
	rb.recordFailure("zen", errors.New("connection refused"))
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
	if got, want := rep.end.Sub(rep.start), 30*time.Second; got != want {
		t.Errorf("episode duration = %v, want %v", got, want)
	}
	if rb.skip("zen") {
		t.Fatal("skip = true after the clearing success, want the backoff gone")
	}
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

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)

	d := NewDaemon(rt, time.Second)
	d.backoff.now = func() time.Time { return baseTime }
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// The first read's failure is unconfirmed, so it dials again only through
	// the second read, which confirms the outage and arms the backoff.
	if n := countCalls(fr, "GetBinding:"); n != 2 {
		t.Fatalf("GetBinding calls = %d, want 2: suspicion then confirmation", n)
	}
	first := strings.TrimPrefix(fr.calls[0], "GetBinding:zen:")
	for i := 0; i < 4; i++ {
		name := fmt.Sprintf("api%d", i)
		b, err := st.Load(name)
		if err != nil {
			t.Fatal(err)
		}
		if name == first {
			if !b.RemoteUnreachableSince.IsZero() {
				t.Errorf("%s stamped after one failure, want the first binding left alone", name)
			}
		} else if !b.RemoteUnreachableSince.Equal(baseTime) {
			t.Errorf("%s stamp = %v, want the first failure's time %v", name, b.RemoteUnreachableSince, baseTime)
		}
		if b.State != store.StateActive {
			t.Errorf("%s state = %s, an outage inside the grace must not halt", name, b.State)
		}
	}

	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := countCalls(fr, "GetBinding:"); n != 2 {
		t.Fatalf("GetBinding calls = %d, want still 2: the armed episode skips every dial", n)
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
	if n := strings.Count(logged.String(), "level=WARN"); n != 1 {
		t.Errorf("WARN lines = %d, want exactly one per episode, got:\n%s", n, logged.String())
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
	if n := countCalls(fr, "GetBinding:"); n != 2 {
		t.Fatalf("GetBinding calls after two ticks inside the backoff = %d, want 2: suspicion then confirmation", n)
	}

	clock = clock.Add(serverBackoff + time.Second)
	tick()
	if n := countCalls(fr, "GetBinding:"); n != 3 {
		t.Fatalf("GetBinding calls after the backoff expired = %d, want one probe more", n)
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
	if n := sr.gets.Load(); n != 2 {
		t.Fatalf("GetBinding calls = %d, want two bounded dials for five bindings: suspicion then confirmation", n)
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
	// The first tick suspects through its first read and confirms through its
	// second; the next tick at the same time skips every dial and stamps the
	// first binding too.
	tick()
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
	st := store.New(t.TempDir())
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

	// The next read confirms: the stamp is the first failure's time, not the
	// second tick's.
	clock = clock.Add(time.Second)
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
		t.Error("skip = false after two failures, want the backoff armed")
	}
}

func TestTickFlappingServerNeverBacksOff(t *testing.T) {
	st := store.New(t.TempDir())
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
	st := store.New(t.TempDir())
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
	clock = clock.Add(serverBackoff + time.Second)
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
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Store: st, Remote: unreachableRemote(), Now: func() time.Time { return baseTime }}
	d := NewDaemon(rt, time.Second)
	east := baseTime.In(time.FixedZone("east", 3*60*60))
	d.backoff.now = func() time.Time { return east }

	for range 2 {
		if err := d.Tick(context.Background()); err != nil {
			t.Fatalf("Tick: %v", err)
		}
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
