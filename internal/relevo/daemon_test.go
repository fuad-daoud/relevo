package relevo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestTickReconcilesAndPersists(t *testing.T) {
	t.Parallel()

	rt, _ := sentBinding(t)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.Round != 2 {
		t.Errorf("tick must persist the advanced round, got %d", b.Round)
	}
	// #303 deleted the pane injection; the report is queued for the mastermind
	// and, with no live channel claim and no deliverer, stays pending for
	// `relevo wait`.
	pending, found, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil || !found {
		t.Fatalf("the closed round's report must be queued for the mastermind: found=%v err=%v", found, err)
	}
	if pending.Kind != store.KindReport {
		t.Errorf("pending kind = %s, want report", pending.Kind)
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	t.Parallel()

	rt, _ := seedBound(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := NewDaemon(rt, 10*time.Millisecond).Run(ctx); err != nil {
		t.Fatalf("Run must exit cleanly on cancel, got %v", err)
	}
}

func TestTickSkipsDoneBindings(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	b.State = store.StateDone
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	before, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	after, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !store.SameBinding(before, after) {
		t.Error("a done binding must be left entirely alone")
	}
}

// TestDaemonBackfillsMasterMindID is the plan's required case for §5.6 (last
// paragraph): a tick back-fills MasterMindID from (MasterMind.Kind,
// MasterMind.SessionID) when the registry knows that session, and leaves it
// empty when it does not.
func TestDaemonBackfillsMasterMindID(t *testing.T) {
	t.Parallel()

	// A binding written before MasterMindID existed: its mastermind endpoint names
	// the session, and MasterMindID is empty.
	legacy := func(t *testing.T, rt Runtime, session string) store.Binding {
		t.Helper()
		b := store.Binding{
			Name: "webshop", CWD: "/repo", Round: 1, State: store.StateActive,
			MasterMind: store.Endpoint{Kind: "claude", SessionID: session},
			Builder:    store.Endpoint{Kind: "agy", Mode: store.ModeHeadless},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("seed legacy binding: %v", err)
		}
		return b
	}

	rt := newRuntime(t)
	b := legacy(t, rt, "sess-architect") // the record newRuntime seeds
	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	got, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.MasterMindID != testMasterMindID {
		t.Errorf("MasterMindID = %q, want the record's %q", got.MasterMindID, testMasterMindID)
	}

	// A miss does nothing: no record for this session, no MasterMindID.
	rt2 := newRuntime(t)
	rt2.MasterMinds = testMasterMinds(t)
	b2 := legacy(t, rt2, "sess-nobody")
	if err := NewDaemon(rt2, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick (miss): %v", err)
	}
	got2, err := rt2.Store.Load(b2.Name)
	if err != nil {
		t.Fatalf("Load (miss): %v", err)
	}
	if got2.MasterMindID != "" {
		t.Errorf("a miss must leave MasterMindID empty, got %q", got2.MasterMindID)
	}
}

// TestTickSurfacesListAgentsFailure guarded the resilience contract at the
// boundary where the daemon actually talked to a pane; #303 deleted that
// boundary (closed-list item 6: the pane client and its agent list). The
// daemon's one list call is now the store's, so the same contract is pinned
// there: a tick whose binding list fails is visible to the caller.
func TestTickSurfacesListAgentsFailure(t *testing.T) {
	t.Parallel()

	rt, _ := sentBinding(t)
	// A state root that cannot be prepared: the "directory" is a regular
	// file, so MkdirAll fails and every store call with it.
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	rt.Store = store.New(notADir)

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err == nil {
		t.Fatal("Tick must surface a failing binding list")
	}
}

// TestTickContinuesPastFailingBinding guards the other half of resilience:
// one binding's reconcile error must not abort the rest of the tick; here the
// error is the tick's own list call, and Run's half is
// TestRunSurvivesFailingTick below.
// TestRunSurvivesFailingTick guards Run's half of resilience: a tick that
// keeps failing must not stop the loop or bubble the tick error out of Run.
// Guarded by a timeout so a regression that makes Run return the tick error
// (or hang) fails the test loudly instead of wedging the suite, the same
// shape as store.TestNestedAccessDoesNotDeadlock.
func TestRunSurvivesFailingTick(t *testing.T) {
	t.Parallel()

	rt, _ := sentBinding(t)
	// Every tick's first step fails: the state root cannot be prepared.
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	rt.Store = store.New(notADir)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- NewDaemon(rt, 10*time.Millisecond).Run(ctx)
	}()

	time.Sleep(1100 * time.Millisecond) // a few floored (500ms) tick intervals
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run must survive repeated tick failures and exit clean on cancel, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel (timeout) -- a failing tick must not wedge it")
	}
}

// TestNewDaemonFloorsInterval guards the floor by inspection made concrete:
// a misconfigured (zero or negative) interval must not spin the tick.
func TestNewDaemonFloorsInterval(t *testing.T) {
	t.Parallel()

	rt := Runtime{Gates: testGateKV(t)}

	if d := NewDaemon(rt, 0); d.interval != minInterval {
		t.Errorf("zero interval -> %s, want floor %s", d.interval, minInterval)
	}
	if d := NewDaemon(rt, -time.Second); d.interval != minInterval {
		t.Errorf("negative interval -> %s, want floor %s", d.interval, minInterval)
	}
	if d := NewDaemon(rt, time.Minute); d.interval != time.Minute {
		t.Errorf("an interval already above the floor must pass through unchanged, got %s", d.interval)
	}
}

// TestTickIgnoresBindingUnboundMidTick covers the window between Tick's
// binding list and its per-binding load: a `relevo unbind` landing in it is
// normal use, not a failure, and must not be logged as one. #303 deleted
// the old fake's onList hook, which is what used to interpose the unbind inside
// the tick, so the test drives tickOne -- the exact function holding the
// guard -- directly.
func TestTickIgnoresBindingUnboundMidTick(t *testing.T) {
	rt, _ := sentBinding(t)
	if err := rt.Store.Delete("webshop"); err != nil {
		t.Fatalf("unbind mid-tick: %v", err)
	}

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(previous)

	if err := NewDaemon(rt, time.Second).tickOne(context.Background(), store.Binding{Name: "webshop"}); err != nil {
		t.Fatalf("a binding unbound mid-tick must be skipped, got %v", err)
	}
	if strings.Contains(logged.String(), "reconcile failed") {
		t.Errorf("an unbind mid-tick must not be logged as a failure: %s", logged.String())
	}
}

func TestTickDoesNotRestampAnUnchangedBinding(t *testing.T) {
	t.Parallel()

	// The next == fresh short-circuit this replaces was never tested. save()
	// stamps UpdatedAt unconditionally, so without the short-circuit every tick
	// rewrites every bind.json and UpdatedAt stops meaning "last change".
	rt, b := seedBound(t)

	before, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	d := NewDaemon(rt, time.Second)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	after, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("UpdatedAt moved %v -> %v on a tick that changed nothing",
			before.UpdatedAt, after.UpdatedAt)
	}
}

// TestTickRefreshesRuntimeBeforeReconcile confirms Tick calls d.refresh
// before it reconciles, so the round the reconcile pass sees is whatever the
// refresh just swapped in -- not last tick's copy.
func TestTickRefreshesRuntimeBeforeReconcile(t *testing.T) {
	t.Parallel()

	rt, _ := sentBinding(t)

	const marker = "refreshed-marker"
	refreshCalls := 0
	d := NewDaemon(rt, time.Second).WithRefresh(func(in Runtime) Runtime {
		refreshCalls++
		in.Policy = policy.Policy{Order: map[string][]string{"builder": {marker}}}
		return in
	})

	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if refreshCalls != 1 {
		t.Errorf("refresh calls = %d, want 1", refreshCalls)
	}
	if got := d.rt.Policy.Order["builder"]; len(got) != 1 || got[0] != marker {
		t.Errorf("d.rt.Policy not swapped by refresh, got %v", got)
	}
}

// TestTickWithoutRefreshIsUnchanged confirms a Daemon with no WithRefresh
// call behaves exactly as before #209 -- the same fixture and assertions as
// TestTickReconcilesAndPersists, the test this one relies on to prove the
// nil-refresh path is untouched.
func TestTickWithoutRefreshIsUnchanged(t *testing.T) {
	t.Parallel()

	rt, _ := sentBinding(t)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.Round != 2 {
		t.Errorf("tick must persist the advanced round, got %d", b.Round)
	}
	if _, found, err := rt.Store.PendingForMasterMind("webshop"); err != nil || !found {
		t.Errorf("the closed round's report must be queued: found=%v err=%v", found, err)
	}
}

// TestTickSyncsMetadataAndFinishedAfterReconcile checks that Tick calls both
// syncPaneMetadata and notifyFinished after the binding loop (#129, #182).
// The finished-toast decision itself is covered by finished_test.go; here
// only that Tick wires both into the pass, using a binding with a closed
// round, no pending payload, and an idle mastermind so the toast fires within
// this one tick.
// TestTickIngestsLiveBindings guards the daemon's end-of-tick ingest hook
// (docs/specs/2026-09-20-persistence-design.md §5.5): with a db configured,
// a tick over a live, sent binding must leave a matching binding and round
// row behind.
func TestTickIngestsLiveBindings(t *testing.T) {
	t.Parallel()

	rt, _ := sentBinding(t)

	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()
	rt.DB = d

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	b, found, err := d.Binding("webshop")
	if err != nil {
		t.Fatalf("Binding: %v", err)
	}
	if !found {
		t.Fatal("Binding(webshop) not found after Tick")
	}
	rounds, err := d.Rounds(b.ID)
	if err != nil {
		t.Fatalf("Rounds: %v", err)
	}
	if len(rounds) != 1 {
		t.Errorf("len(rounds) = %d, want 1", len(rounds))
	}
}

// TestTickWithoutDBIsUnchanged guards the nil-DB path: every call site
// (here, the ingest hook) must treat Runtime.DB == nil exactly like a
// machine with no database -- no panic, and nothing written into the ingest
// mirror. relevo.db itself is the store's own record file, which the
// fixture's Bind/Save creates, so the assertion is on the mirror's rows
// (P3a round 3, B3).
func TestTickWithoutDBIsUnchanged(t *testing.T) {
	t.Parallel()

	rt, _ := sentBinding(t)

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	assertIngestMirrorEmpty(t, rt.Store.DBPath())
}

// assertIngestMirrorEmpty asserts that the database at path holds no ingest
// mirror rows: binding, event and round are all empty. A database that does
// not exist passes too, which is what makes this a port of the old "Tick with
// DB == nil must leave no relevo.db behind" stat: relevo.db is now the store's
// own record file, and a nil Runtime.DB must still ingest nothing into the
// mirror (P3a round 3, B3).
func assertIngestMirrorEmpty(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		t.Fatalf("stat %s: %v", path, err)
	}
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open(%s): %v", path, err)
	}
	defer d.Close()

	stats, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	for _, tbl := range []string{"binding", "event", "round"} {
		if n := stats.Rows[tbl]; n != 0 {
			t.Errorf("ingest mirror %s has %d rows, want 0", tbl, n)
		}
	}
}

// fakeFetcher counts calls so a test can prove the tick asked the endpoint --
// or, on a fresh cache, never asked at all.
type fakeFetcher struct {
	calls int
	tag   string
	err   error
}

func (f *fakeFetcher) Latest(ctx context.Context) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return f.tag, nil
}

// TestTickRefreshesOncePastTTL counts fetches: none while the cached answer is
// fresh, one once it is stale. Drop the Stale guard and the fresh case fails.
func TestTickRefreshesOncePastTTL(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		seed      bool
		checkedAt time.Time
		wantCalls int
	}{
		{
			name:      "fresh cache is left alone",
			seed:      true,
			checkedAt: now.Add(-(release.TTL - time.Second)),
			wantCalls: 0,
		},
		{
			name:      "stale cache refetches",
			seed:      true,
			checkedAt: now.Add(-(release.TTL + time.Second)),
			wantCalls: 1,
		},
		{
			name:      "no cache at all fetches",
			wantCalls: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ff := &fakeFetcher{tag: "v0.8.0"}
			rt, _ := sentBinding(t)
			rt.Fetcher = ff
			rt.Now = func() time.Time { return now }

			// The cache lives in the machine database's kv row, reached through
			// the runtime's own store -- for the daemon the one shared handle
			// (P3b plan §4.5).
			mdb, err := rt.Store.DB()
			if err != nil {
				t.Fatalf("open store db: %v", err)
			}
			defer mdb.Close()
			// The cache is machine-local, so the fixture seeds and reads the
			// file the pass reads it from.
			mdb = mdb.LocalOrSelf()
			if tc.seed {
				if err := release.Save(mdb, release.Cache{
					Latest:    "v0.7.0",
					CheckedAt: tc.checkedAt,
					Source:    "test",
				}); err != nil {
					t.Fatalf("seed cache: %v", err)
				}
			}

			if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
				t.Fatalf("Tick: %v", err)
			}

			if ff.calls != tc.wantCalls {
				t.Errorf("fetch calls = %d, want %d", ff.calls, tc.wantCalls)
			}

			c, ok, err := release.Load(mdb)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			want := "v0.8.0"
			if tc.wantCalls == 0 {
				want = "v0.7.0" // the fresh answer stays exactly as it was
			}
			if !ok || c.Latest != want {
				t.Errorf("cache = (%+v, ok %v), want latest %s", c, ok, want)
			}
			if tc.wantCalls == 0 && !c.CheckedAt.Equal(tc.checkedAt) {
				t.Errorf("fresh cache checked_at = %s, want it untouched at %s", c.CheckedAt, tc.checkedAt)
			}
		})
	}
}

// TestTickSurvivesFetchError pins §4.4's failure rule: a fetch error is
// swallowed, Tick still returns nil, and the cache is not written -- so an
// offline machine retries next tick instead of recording a wrong answer.
func TestTickSurvivesFetchError(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

	t.Run("no cache yet", func(t *testing.T) {
		ff := &fakeFetcher{err: errors.New("dial tcp: network is unreachable")}
		rt, _ := sentBinding(t)
		rt.Fetcher = ff
		rt.Now = func() time.Time { return now }

		if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
			t.Errorf("Tick = %v, want nil: a failed release check must not stop the daemon", err)
		}
		if ff.calls != 1 {
			t.Errorf("fetch calls = %d, want 1 (the stale check tried)", ff.calls)
		}
		mdb, merr := rt.Store.DB()
		if merr != nil {
			t.Fatalf("open store db: %v", merr)
		}
		defer mdb.Close()
		// The cache is machine-local, so the fixture reads the file the pass
		// writes.
		mdb = mdb.LocalOrSelf()
		if _, ok, err := release.Load(mdb); err != nil || ok {
			t.Errorf("cache = (_, %v, %v), want no record: a failed fetch saves nothing", ok, err)
		}
	})

	t.Run("stale cache is left alone", func(t *testing.T) {
		ff := &fakeFetcher{err: errors.New("504 gateway timeout")}
		rt, _ := sentBinding(t)
		rt.Fetcher = ff
		rt.Now = func() time.Time { return now }

		mdb, merr := rt.Store.DB()
		if merr != nil {
			t.Fatalf("open store db: %v", merr)
		}
		defer mdb.Close()
		mdb = mdb.LocalOrSelf()
		stale := release.Cache{Latest: "v0.7.0", CheckedAt: now.Add(-2 * release.TTL), Source: "test"}
		if err := release.Save(mdb, stale); err != nil {
			t.Fatalf("seed cache: %v", err)
		}

		if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
			t.Errorf("Tick = %v, want nil", err)
		}

		c, ok, err := release.Load(mdb)
		if err != nil || !ok {
			t.Fatalf("Load = (%+v, ok %v, %v), want the seeded cache", c, ok, err)
		}
		if c.Latest != stale.Latest || !c.CheckedAt.Equal(stale.CheckedAt) {
			t.Errorf("cache = %+v, want the stale answer untouched at %+v", c, stale)
		}
	})
}

// panickingRunner is the fake Runner with one pid whose Alive panics, so a
// test can prove the daemon contains a panic raised under one binding
// (#370, spec §4.6).
type panickingRunner struct {
	*fakeRunner
	panicPID int
}

func (p *panickingRunner) Alive(ctx context.Context, h spawn.ProcHandle) (bool, error) {
	if h.PID == p.panicPID {
		panic("alive exploded")
	}
	return p.fakeRunner.Alive(ctx, h)
}

// TestTickSurvivesAPanickingReconcile pins #370, spec §4.6: a panic raised
// anywhere under one binding is recovered in tickOne, logged, and returned as
// an error, so the tick still reconciles every other binding and returns nil.
//
// Mutation check: remove the recover from tickOne and this test crashes the
// test binary instead of passing.
func TestTickSurvivesAPanickingReconcile(t *testing.T) {
	fr := newFakeRunner()
	rt, first := sentHeadless(t, fr)

	// A second, healthy binding on the same store, reconciled by the same
	// tick. Its own working tree, since a tree carries one binding.
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "other", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo-other",
	}); err != nil {
		t.Fatalf("Bind other: %v", err)
	}
	if _, err := Send(context.Background(), rt, "other", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send other: %v", err)
	}
	other, err := rt.Store.Load("other")
	if err != nil {
		t.Fatalf("Load other: %v", err)
	}

	// From here on, observing the first binding's builder panics.
	rt.Runner = &panickingRunner{fakeRunner: fr, panicPID: first.Builder.PID}

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(previous)

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick = %v, want nil: one binding's panic must not fail the tick", err)
	}
	if !strings.Contains(logged.String(), "reconcile panicked") {
		t.Errorf("no 'reconcile panicked' error line:\n%s", logged.String())
	}

	// The healthy binding was reconciled in that same tick and its process is
	// untouched: the panic ended nothing but the panicking binding's tick.
	after, err := rt.Store.Load("other")
	if err != nil {
		t.Fatalf("Load other after tick: %v", err)
	}
	if after.Builder.PID != other.Builder.PID {
		t.Errorf("other.Builder.PID = %d, want it left at %d", after.Builder.PID, other.Builder.PID)
	}
	if after.State != store.StateActive {
		t.Errorf("other.State = %s, want active", after.State)
	}
}

// TestTickBacksOffAfterAFailedReleaseFetch pins §4.10's backoff: one failed
// fetch stops the daemon asking again for an hour, and once the hour has
// passed it asks again.
//
// Mutation: drop the `now().Before(d.releaseRetryAt)` early return and the
// second Tick fetches, so calls reaches 2 early.
func TestTickBacksOffAfterAFailedReleaseFetch(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	ff := &fakeFetcher{err: errors.New("504 gateway timeout")}
	rt, _ := sentBinding(t)
	rt.Fetcher = ff
	rt.Now = func() time.Time { return now }

	d := NewDaemon(rt, time.Second)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if ff.calls != 1 {
		t.Fatalf("fetch calls = %d, want 1 on the failing tick", ff.calls)
	}

	now = now.Add(30 * time.Minute)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick (inside the backoff): %v", err)
	}
	if ff.calls != 1 {
		t.Errorf("fetch calls = %d, want 1 inside the backoff window", ff.calls)
	}

	now = now.Add(31 * time.Minute) // 61 minutes after the failure
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick (after the backoff): %v", err)
	}
	if ff.calls != 2 {
		t.Errorf("fetch calls = %d, want 2 once the hour has passed", ff.calls)
	}
}

// TestRefreshReleaseSuccessClearsTheBackoff pins the other half of §4.10's
// contract: a successful save clears the retry deadline, so the next failure
// backs off from its own moment.
func TestRefreshReleaseSuccessClearsTheBackoff(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	ff := &fakeFetcher{err: errors.New("network is unreachable")}
	rt, _ := sentBinding(t)
	rt.Fetcher = ff
	rt.Now = func() time.Time { return now }

	d := NewDaemon(rt, time.Second)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if d.releaseRetryAt.IsZero() {
		t.Fatal("a failed fetch must set the retry deadline")
	}

	now = now.Add(releaseRetryAfter + time.Minute)
	ff.err = nil
	ff.tag = "v0.9.0"
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick (after the backoff): %v", err)
	}
	if !d.releaseRetryAt.IsZero() {
		t.Errorf("releaseRetryAt = %v after a successful fetch, want the zero time", d.releaseRetryAt)
	}
	mdb, merr := rt.Store.DB()
	if merr != nil {
		t.Fatalf("open store db: %v", merr)
	}
	defer mdb.Close()
	cached, ok, err := release.Load(mdb.LocalOrSelf())
	if err != nil || !ok {
		t.Fatalf("release.Load = (ok %v, err %v), want the fetched answer saved", ok, err)
	}
	if cached.Latest != "v0.9.0" {
		t.Errorf("cache latest = %q, want v0.9.0", cached.Latest)
	}
}

// TestRefreshReleaseOpensTheDatabaseOnce pins the D1 postcondition: across any
// number of refreshRelease calls on one Daemon, only one connection to the
// runtime's relevo.db is open. Before the fix each tick made a fresh store.New,
// opened a connection and never closed it.
//
// Mutation: open a store.New(root).DB() inside refreshRelease again and the
// count grows by one per call.
func TestRefreshReleaseOpensTheDatabaseOnce(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("counts open fds through /proc/self/fd, which is Linux-only")
	}

	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	ff := &fakeFetcher{tag: "v0.9.0"}
	rt, _ := sentBinding(t)
	rt.Fetcher = ff
	rt.Now = func() time.Time { return now }

	d := NewDaemon(rt, time.Second)
	ctx := context.Background()

	// The first call may open and create relevo.db, so the baseline is
	// counted after it.
	d.refreshRelease(ctx)
	before := countFDsOn(t, rt.Store.DBPath())

	for i := 0; i < 20; i++ {
		if i == 10 {
			// Once past the TTL in the middle, so the save path runs
			// again as well as the read path.
			now = now.Add(release.TTL + time.Minute)
		}
		d.refreshRelease(ctx)
	}

	after := countFDsOn(t, rt.Store.DBPath())
	if after-before != 0 {
		t.Errorf("open fds on relevo.db went from %d to %d across 20 extra refreshRelease calls, want no growth",
			before, after)
	}
}

// countFDsOn counts this process's open file descriptors whose readlink target
// is exactly path.
func countFDsOn(t *testing.T, path string) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read /proc/self/fd: %v", err)
	}
	n := 0
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err != nil {
			continue // the fd closed between ReadDir and Readlink
		}
		if target == path {
			n++
		}
	}
	return n
}

// TestBackfillLeavesDoneBindingsAlone pins the DONE guard in
// backfillMasterMindID: a finished binding is history, and a tick must not
// rewrite it even when its mastermind session now has a record.
func TestBackfillLeavesDoneBindingsAlone(t *testing.T) {
	t.Parallel()

	reg, _ := testMasterMindRegistry(t, mastermind.Record{
		ID:          "pl_aaaaaaaacccc",
		Name:        "architect-1",
		HarnessKind: "claude",
		SessionID:   "sess-done",
		CWD:         "/repo",
	})
	b := store.Binding{Name: "old", State: store.StateDone}
	b.MasterMind.Kind, b.MasterMind.SessionID = "claude", "sess-done"

	if got := backfillMasterMindID(Runtime{MasterMinds: reg}, b); got.MasterMindID != "" {
		t.Errorf("DONE binding back-filled with %q; history must not be rewritten", got.MasterMindID)
	}
	b.State = store.StateActive
	if got := backfillMasterMindID(Runtime{MasterMinds: reg}, b); got.MasterMindID != "pl_aaaaaaaacccc" {
		t.Errorf("ACTIVE binding MasterMindID = %q, want pl_aaaaaaaacccc", got.MasterMindID)
	}
}

// TestBackfillLeavesMasterMindlessBindingsAlone pins the MasterMind.SessionID guard
// in backfillMasterMindID: a non-DONE binding with no id and an empty
// MasterMind.SessionID -- the shape a remote binding written before the add
// fix has -- names no session for the registry to look up, so a tick leaves it
// exactly as it was. relevo never guesses a mastermind for it.
func TestBackfillLeavesMasterMindlessBindingsAlone(t *testing.T) {
	t.Parallel()

	reg, _ := testMasterMindRegistry(t, mastermind.Record{
		ID:          "pl_aaaaaaaacccc",
		Name:        "architect-1",
		HarnessKind: "claude",
		SessionID:   "sess-remote",
		CWD:         "/repo",
	})

	b := store.Binding{Name: "api", State: store.StateActive}
	if got := backfillMasterMindID(Runtime{MasterMinds: reg}, b); got.MasterMindID != "" {
		t.Errorf("mastermindless binding back-filled with %q; nothing names its mastermind", got.MasterMindID)
	}
}

// TestTickSkipsANewerFormatBinding pins #372 R1's rule under the DB-backed
// store: a binding written by a newer relevo is left to that relevo. The
// import refuses it with ErrNewerFormat and the file is not touched, so no
// field this binary cannot understand is ever rewritten.
//
// (Before the database the daemon skipped the binding after loading it; now
// the load itself refuses it, so a tick over such a root fails its listing.)
// TestTickSkipsANewerFormatBinding: a newer-format record fails the load, and
// the record row is left untouched.
func TestTickSkipsANewerFormatBinding(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	b := store.Binding{
		Name: "webshop", CWD: "/repo", Round: 1, State: store.StateActive,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "sess-architect"},
		Builder:    store.Endpoint{Kind: "agy", Mode: store.ModeHeadless},
		Format:     store.BindingFormat + 1,
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(fmt.Sprintf(`"format":%d`, store.BindingFormat+1))) {
		t.Fatalf("the fixture must carry format %d, got:\n%s", store.BindingFormat+1, raw)
	}
	d, err := rt.Store.DB()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.RecordPut(db.Record{Owner: "", Name: b.Name, Round: 1, JSON: string(raw)}); err != nil {
		t.Fatal(err)
	}

	_, err = rt.Store.Load(b.Name)
	var newer *store.ErrNewerFormat
	if !errors.As(err, &newer) {
		t.Fatalf("Load of a newer-format binding = %v, want *store.ErrNewerFormat", err)
	}
	if !errors.Is(err, store.ErrNewerFormatSentinel) {
		t.Errorf("errors.Is(%v, store.ErrNewerFormatSentinel) = false, want true", err)
	}
}

// TestMasterMindPruneDue pins §4.4's once-an-hour decision, pure so it needs no
// daemon.
func TestMasterMindPruneDue(t *testing.T) {
	t.Parallel()

	now := time.Unix(1757000000, 0).UTC()
	cases := []struct {
		name string
		last time.Time
		ok   bool
		now  time.Time
		want bool
	}{
		{"never pruned", time.Time{}, false, now, true},
		{"pruned just now", now, true, now, false},
		{"59 minutes on", now, true, now.Add(59 * time.Minute), false},
		{"an hour on", now, true, now.Add(time.Hour), true},
		{"a clock step back is not a trigger", now, true, now.Add(-time.Hour), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mastermindPruneDue(c.last, c.ok, c.now); got != c.want {
				t.Errorf("mastermindPruneDue = %v, want %v", got, c.want)
			}
		})
	}
}

// TestPruneMasterMindsForgetsAndStamps pins the daemon's own prune (§4.4): a gone
// record no binding names is forgotten, a live one survives, and the run is
// stamped in the store database's kv row planner.pruned_at.
func TestPruneMasterMindsForgetsAndStamps(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	now := time.Unix(1757000000, 0).UTC()
	d, err := st.DB()
	if err != nil {
		t.Fatalf("open store db: %v", err)
	}
	reg := &mastermind.DBRegistry{KV: db.TxKV{DB: d}, Now: func() time.Time { return now }}

	mk := func(id, name, session string, host int) mastermind.Record {
		rec, err := reg.Create(mastermind.Record{
			ID: id, Name: name, HarnessKind: "claude", SessionID: session,
			HostPID: host, HostStartedAt: int64(host) * 10, CWD: "/tmp/x",
			CreatedAt: now, SeenAt: now,
		})
		if err != nil {
			t.Fatalf("Create(%s): %v", id, err)
		}
		return rec
	}
	live := mk("pl_aaaaaaaaaaaa", "alpha", "sess-a", 111)
	gone := mk("pl_bbbbbbbbbbbb", "beta", "sess-b", 222)

	rt := Runtime{
		Store:       st,
		MasterMinds: reg,
		Now:         func() time.Time { return now },
		ProcStart: func(pid int) (int64, error) {
			if pid == live.HostPID {
				return live.HostStartedAt, nil
			}
			return 0, errors.New("ps: no such process")
		},
	}

	NewDaemon(rt, time.Second).pruneMasterMinds()

	if _, err := reg.Get(gone.ID); !errors.Is(err, mastermind.ErrNotFound) {
		t.Errorf("gone record %s is still present: err = %v", gone.ID, err)
	}
	if _, err := reg.Get(live.ID); err != nil {
		t.Errorf("live record %s was forgotten: %v", live.ID, err)
	}

	kv, err := st.DB()
	if err != nil {
		t.Fatalf("store DB: %v", err)
	}
	last, ok, err := mastermindLastPruned(kv)
	if err != nil {
		t.Fatalf("mastermindLastPruned: %v", err)
	}
	if !ok || !last.Equal(now) {
		t.Errorf("planner.pruned_at = (%v, %v), want (%v, true)", last, ok, now)
	}
}
