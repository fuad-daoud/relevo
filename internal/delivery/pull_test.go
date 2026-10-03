package delivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestPullPendingReturnsAndMarksDelivered: Pull hands back the oldest
// pending payload and confirms it with the route it was given -- the helper
// `relevo wait` calls with route "wait".
func TestPullPendingReturnsAndMarksDelivered(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	payload, found, err := Pull(context.Background(), rt.Store, "webshop", "wait")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !found || payload == "" {
		t.Fatalf("Pull found=%v payload=%q, want the queued report", found, payload)
	}
	if _, still, err := rt.Store.PendingForMasterMind("webshop"); err != nil || still {
		t.Errorf("Pull must confirm what it returns (still pending=%v err=%v)", still, err)
	}
}

// TestPullPendingWithNothingPending: nothing queued reads as nothing to print.
func TestPullPendingWithNothingPending(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")
	if _, _, err := Pull(context.Background(), rt.Store, "webshop", "wait"); err != nil {
		t.Fatalf("first Pull: %v", err)
	}

	payload, found, err := Pull(context.Background(), rt.Store, "webshop", "wait")
	if err != nil {
		t.Fatalf("second Pull: %v", err)
	}
	if found || payload != "" {
		t.Errorf("second Pull = (%q, %v), want nothing pending", payload, found)
	}
}

// TestPullPendingSkipsAdmitted pins the reader's half of exactly-once: an entry
// a push route already admitted is not claimable, so Pull prints nothing
// and leaves it unconfirmed for the deliverer's own read-back.
func TestPullPendingSkipsAdmitted(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	if err := rt.Store.AdmitIndex("webshop", 0); err != nil {
		t.Fatalf("AdmitIndex: %v", err)
	}

	payload, found, err := Pull(context.Background(), rt.Store, "webshop", "wait")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if found || payload != "" {
		t.Fatalf("Pull = (%q, %v), want nothing: the entry is admitted", payload, found)
	}

	text, found, err := PullPendingThrough(context.Background(), rt.Store, "webshop", "wait", 0)
	if err != nil {
		t.Fatalf("PullPendingThrough: %v", err)
	}
	if found || text != "" {
		t.Fatalf("PullPendingThrough = (%q, %v), want nothing: the entry is admitted", text, found)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && e.Confirmed {
			t.Error("an admitted entry must not be confirmed by a reader")
		}
	}
}

// TestPullPendingMarksDeliveredRoute: Pull marks the entry delivered
// with the route the caller passed -- "wait" from Wait.
func TestPullPendingMarksDeliveredRoute(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	if _, _, err := Pull(context.Background(), rt.Store, "webshop", "wait"); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := entries[len(entries)-1]
	if !last.Confirmed {
		t.Error("the entry must be confirmed")
	}
	if last.Route != "wait" {
		t.Errorf("Route = %q, want wait", last.Route)
	}
}

// seedPendingReport saves an active binding and one unconfirmed mastermind-bound
// entry with the caller's Path and Kind, so Pull's expansion (PushText)
// is exercised against a file the test owns. seedPending's own entry points at
// the literal /tmp/report.md, which may exist on a developer machine.
//
// It is modelled on seedPending (same binding, same critical section) but
// appends the entry directly rather than through Queue: Queue prepends the
// origin line to Payload (deliver.go's WithOrigin), and these tests need the
// stored Payload to be exactly "round 1 report".
func seedPendingReport(t *testing.T, rt Deps, name, path string, kind store.Kind) store.Binding {
	t.Helper()
	b := store.Binding{
		Name:         name,
		CWD:          "/repo/" + name,
		Round:        1,
		State:        store.StateActive,
		MasterMind:   store.Endpoint{Kind: "claude", SessionID: "sess"},
		MasterMindID: "pl_aaaaaaaabbbb",
		Builder:      store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(b); err != nil {
			return err
		}
		return tx.AppendLog(name, store.LogEntry{
			TS: rt.Now().UTC(), Round: 1, Direction: store.DirToMasterMind, Kind: kind,
			Payload: "round 1 report", Path: path, Confirmed: false,
		})
	}); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return b
}

// TestPullPendingPrintsReportText: Pull returns the pointer payload
// followed by a blank line and the report file's own text, so the mastermind
// needs no second read.
func TestPullPendingPrintsReportText(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	path := filepath.Join(t.TempDir(), "001-report.md")
	if err := os.WriteFile(path, []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	seedPendingReport(t, rt, "webshop", path, store.KindReport)

	text, found, err := Pull(context.Background(), rt.Store, "webshop", "wait")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !found {
		t.Fatal("Pull found nothing, want the queued report")
	}
	if !strings.HasPrefix(text, "round 1 report\n\n") {
		t.Errorf("Pull text = %q, want it to start with the payload and a blank line", text)
	}
	if !strings.Contains(text, "line two") {
		t.Errorf("Pull text = %q, want it to carry the report's own text", text)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := entries[len(entries)-1]
	if !last.Confirmed || last.Route != "wait" {
		t.Errorf("entry = confirmed:%v route:%q, want confirmed with route=wait", last.Confirmed, last.Route)
	}
}

// TestPullPendingCapsReportText: a report larger than MaxPushBytes is cut back
// to the cap and names the `relevo show` command that prints the full text.
func TestPullPendingCapsReportText(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	path := filepath.Join(t.TempDir(), "001-report.md")
	// Exactly MaxPushBytes + 4096 bytes of newline-terminated lines.
	if err := os.WriteFile(path, []byte(strings.Repeat("x\n", (MaxPushBytes+4096)/2)), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	seedPendingReport(t, rt, "webshop", path, store.KindReport)

	text, found, err := Pull(context.Background(), rt.Store, "webshop", "wait")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !found {
		t.Fatal("Pull found nothing, want the queued report")
	}

	want := fmt.Sprintf("[truncated at %d KiB -- full text: relevo show webshop --round 1 --report]", MaxPushBytes/1024)
	if !strings.Contains(text, want) {
		t.Errorf("Pull text does not carry the truncation tail %q:\n%s", want, text)
	}
	if len(text) >= MaxPushBytes+len("round 1 report")+200 {
		t.Errorf("len(text) = %d, want less than %d", len(text), MaxPushBytes+len("round 1 report")+200)
	}
}

// TestPullPendingUnreadableReportFallsBackToPayload: a missing report file is
// not an error and the entry stays delivered -- the payload still names the
// show command.
func TestPullPendingUnreadableReportFallsBackToPayload(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	path := filepath.Join(t.TempDir(), "missing-report.md")
	seedPendingReport(t, rt, "webshop", path, store.KindReport)

	text, found, err := Pull(context.Background(), rt.Store, "webshop", "wait")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !found {
		t.Fatal("Pull found nothing, want the queued report")
	}
	if text != "round 1 report" {
		t.Errorf("Pull text = %q, want the bare payload", text)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := entries[len(entries)-1]
	if !last.Confirmed || last.Route != "wait" {
		t.Errorf("entry = confirmed:%v route:%q, want confirmed with route=wait", last.Confirmed, last.Route)
	}
}

// TestPullMatchingClaimsOnlyWhatMatchAccepts pins the conditional claim: the
// same fixture as an unconditional Pull, with a match that accepts nothing.
// Nothing is confirmed, nothing is returned, and the entry is still claimable
// afterwards by a reader that accepts it.
func TestPullMatchingClaimsOnlyWhatMatchAccepts(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedPendingReport(t, rt, "webshop", "", store.KindReport)

	never := func(store.LogEntry) bool { return false }
	text, found, err := PullMatching(context.Background(), rt.Store, "webshop", "show", never)
	if err != nil {
		t.Fatalf("PullMatching: %v", err)
	}
	if found || text != "" {
		t.Errorf("PullMatching with a rejecting match = (%q, %v), want nothing", text, found)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && e.Confirmed {
			t.Fatal("a rejecting match must confirm nothing")
		}
	}

	// The entry survived: a reader that does accept it claims it as before.
	always := func(store.LogEntry) bool { return true }
	if _, found, err := PullMatching(context.Background(), rt.Store, "webshop", "show", always); err != nil || !found {
		t.Errorf("PullMatching with an accepting match = found %v, err %v; want the payload", found, err)
	}
}

// TestPullMatchingNeverReordersTheQueue pins that a conditional claim is still
// the oldest claimable entry or nothing: with rounds 1 and 2 pending and a match
// that accepts only round 2, the read confirms nothing rather than skipping
// ahead to the entry it accepts.
func TestPullMatchingNeverReordersTheQueue(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "001-report.md"), filepath.Join(dir, "002-report.md")}
	for i, p := range paths {
		if err := os.WriteFile(p, []byte(fmt.Sprintf("round %d body\n", i+1)), 0o644); err != nil {
			t.Fatalf("write round %d report: %v", i+1, err)
		}
	}
	seedPendingRounds(t, rt, "webshop", []int{1, 2}, paths)

	round2Only := func(e store.LogEntry) bool { return e.Round == 2 }
	if _, found, err := PullMatching(context.Background(), rt.Store, "webshop", "show", round2Only); err != nil || found {
		t.Errorf("PullMatching accepting round 2 = found %v, err %v; want nothing: round 1 comes first", found, err)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && e.Confirmed {
			t.Fatal("a conditional claim must not skip the older payload")
		}
	}
}

// TestPullMatchingSkipsAdmitted pins that the admit exclusion outranks the
// match: an entry a push route admitted is not claimable, so even a match that
// accepts it confirms nothing.
func TestPullMatchingSkipsAdmitted(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedPendingReport(t, rt, "webshop", "", store.KindReport)
	if err := rt.Store.AdmitIndex("webshop", 0); err != nil {
		t.Fatalf("AdmitIndex: %v", err)
	}

	always := func(store.LogEntry) bool { return true }
	text, found, err := PullMatching(context.Background(), rt.Store, "webshop", "show", always)
	if err != nil || found || text != "" {
		t.Errorf("PullMatching on an admitted entry = (%q, %v, %v), want nothing", text, found, err)
	}
}

// TestRetryBusy pins retryBusy's contract: a busy error is retried with
// the delays it was given, a non-busy error is not retried, and a cancelled
// context stops before the next sleep. The sleep function is injected, so no
// test really sleeps.
func TestRetryBusy(t *testing.T) {
	t.Parallel()

	busy := fmt.Errorf("tx begin: %w", db.ErrBusy)
	t.Run("busy twice then nil retries with the delays", func(t *testing.T) {
		calls := 0
		var slept []time.Duration
		err := retryBusy(context.Background(), busyRetryDelays, func(d time.Duration) { slept = append(slept, d) }, func() error {
			calls++
			if calls <= 2 {
				return busy
			}
			return nil
		})
		if err != nil {
			t.Fatalf("retryBusy: %v", err)
		}
		if calls != 3 {
			t.Errorf("fn called %d times, want 3", calls)
		}
		if len(slept) != 2 || slept[0] != 250*time.Millisecond || slept[1] != time.Second {
			t.Errorf("slept = %v, want [250ms 1s]", slept)
		}
	})
	t.Run("always busy returns an error that wraps db.ErrBusy", func(t *testing.T) {
		calls := 0
		err := retryBusy(context.Background(), busyRetryDelays, func(time.Duration) {}, func() error {
			calls++
			return busy
		})
		if calls != 4 {
			t.Errorf("fn called %d times, want 4", calls)
		}
		if !errors.Is(err, db.ErrBusy) {
			t.Errorf("err = %v, want it to wrap db.ErrBusy", err)
		}
	})
	t.Run("a non-busy error returns at once", func(t *testing.T) {
		calls := 0
		sentinel := errors.New("boom")
		err := retryBusy(context.Background(), busyRetryDelays, func(time.Duration) {}, func() error {
			calls++
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Errorf("err = %v, want %v", err, sentinel)
		}
		if calls != 1 {
			t.Errorf("fn called %d times, want 1", calls)
		}
	})
	t.Run("a cancelled context stops before the first sleep", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		slept := false
		err := retryBusy(ctx, busyRetryDelays, func(time.Duration) { slept = true }, func() error {
			calls++
			return busy
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if calls != 1 {
			t.Errorf("fn called %d times, want 1", calls)
		}
		if slept {
			t.Error("sleep ran once ctx was done")
		}
	})
}

// seedPendingRounds saves an active binding and one unconfirmed mastermind-bound
// report per round and path, in the order given: seedPendingReport generalized
// from one round to several, each entry's payload naming its round.
func seedPendingRounds(t *testing.T, rt Deps, name string, rounds []int, paths []string) store.Binding {
	t.Helper()
	if len(rounds) != len(paths) {
		t.Fatalf("seedPendingRounds: %d rounds for %d paths", len(rounds), len(paths))
	}
	b := store.Binding{
		Name:         name,
		CWD:          "/repo/" + name,
		Round:        rounds[len(rounds)-1],
		State:        store.StateActive,
		MasterMind:   store.Endpoint{Kind: "claude", SessionID: "sess"},
		MasterMindID: "pl_aaaaaaaabbbb",
		Builder:      store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(b); err != nil {
			return err
		}
		for i, round := range rounds {
			if err := tx.AppendLog(name, store.LogEntry{
				TS: rt.Now().UTC(), Round: round, Direction: store.DirToMasterMind, Kind: store.KindReport,
				Payload: fmt.Sprintf("round %d report", round), Path: paths[i], Confirmed: false,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return b
}

// TestPullPendingThroughDeliversEarlierAndWaited: with rounds 1 and 2 both
// pending, PullPendingThrough returns round 1's text under a header naming its
// round, then round 2's text last, and confirms both with route "wait".
func TestPullPendingThroughDeliversEarlierAndWaited(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	dir := t.TempDir()
	r1 := filepath.Join(dir, "001-report.md")
	r2 := filepath.Join(dir, "002-report.md")
	if err := os.WriteFile(r1, []byte("round 1 body\n"), 0o644); err != nil {
		t.Fatalf("write round 1 report: %v", err)
	}
	if err := os.WriteFile(r2, []byte("round 2 body\n"), 0o644); err != nil {
		t.Fatalf("write round 2 report: %v", err)
	}
	seedPendingRounds(t, rt, "webshop", []int{1, 2}, []string{r1, r2})

	text, found, err := PullPendingThrough(context.Background(), rt.Store, "webshop", "wait", 2)
	if err != nil {
		t.Fatalf("pullPendingThrough: %v", err)
	}
	if !found {
		t.Fatal("pullPendingThrough found nothing, want both pending reports")
	}

	header := fmt.Sprintf("── round 1: not delivered earlier (%s) ──", r1)
	if !strings.Contains(text, header) {
		t.Errorf("text does not carry the earlier round's header %q:\n%s", header, text)
	}
	if !strings.Contains(text, "round 1 body") {
		t.Errorf("text does not carry round 1's report text:\n%s", text)
	}
	if !strings.HasSuffix(text, "round 2 body\n") {
		t.Errorf("text does not end with round 2's report text:\n%s", text)
	}
	if strings.Index(text, "round 1 body") > strings.Index(text, "round 2 body") {
		t.Errorf("round 1's report came after round 2's:\n%s", text)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for i, e := range entries {
		if e.Direction != store.DirToMasterMind {
			continue
		}
		if !e.Confirmed || e.Route != "wait" {
			t.Errorf("entry %d = confirmed:%v route:%q, want confirmed with route=wait", i, e.Confirmed, e.Route)
		}
	}
}

// TestPullPendingThroughSingleIsUnchanged: with only one pending report,
// PullPendingThrough returns exactly what Pull returns for the same
// fixture -- a single delivery is untouched by the through-round path.
func TestPullPendingThroughSingleIsUnchanged(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "002-report.md")
	if err := os.WriteFile(path, []byte("round 2 body\n"), 0o644); err != nil {
		t.Fatalf("write round 2 report: %v", err)
	}

	// The same fixture twice, so neither call sees the other's confirmation.
	throughRT := routeRuntime(t)
	seedPendingRounds(t, throughRT, "webshop", []int{2}, []string{path})
	pullRT := routeRuntime(t)
	seedPendingRounds(t, pullRT, "webshop", []int{2}, []string{path})

	through, found, err := PullPendingThrough(context.Background(), throughRT.Store, "webshop", "wait", 2)
	if err != nil {
		t.Fatalf("pullPendingThrough: %v", err)
	}
	if !found {
		t.Fatal("PullPendingThrough found nothing, want the single pending report")
	}

	pulled, found, err := Pull(context.Background(), pullRT.Store, "webshop", "wait")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !found {
		t.Fatal("Pull found nothing, want the same pending report")
	}

	if through != pulled {
		t.Errorf("pullPendingThrough = %q, want it to equal Pull's %q", through, pulled)
	}
}
