package relevo

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// lockFreeWithin reports whether the store's state lock can be taken within d,
// by running a no-op WithLock in a goroutine.
func lockFreeWithin(t *testing.T, st *store.Store, d time.Duration) bool {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = st.WithLock(func(*store.Tx) error { return nil })
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// assertFetchIsUnlocked fails the test if a running-round fetch's network
// calls run while the state lock is held.
func assertFetchIsUnlocked(t *testing.T, st *store.Store, fr *fakeRemote) {
	t.Helper()
	fr.beforeCall = func(call string) {
		if strings.HasPrefix(call, "GetBinding:") || strings.HasPrefix(call, "RoundFileFrom:") {
			if !lockFreeWithin(t, st, 2*time.Second) {
				t.Errorf("%s ran while the state lock was held", call)
			}
		}
	}
}

func countCalls(fr *fakeRemote, prefix string) int {
	n := 0
	for _, c := range fr.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestTickFetchesRemoteWithoutTheStateLock(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRemote{
		getBindingResp:    remote.BindingView{RoundState: remote.RoundRunning},
		roundFileFromResp: io.NopCloser(strings.NewReader("builder log line 1\n")),
	}
	assertFetchIsUnlocked(t, st, fr)
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	data, err := rt.Store.ReadFile(st.BuilderLogPath("api", 1))
	if err != nil {
		t.Fatalf("read mirrored log: %v", err)
	}
	if string(data) != "builder log line 1\n" {
		t.Fatalf("log = %q, want the fetched body", string(data))
	}
	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Builder.RemoteStatus != string(remote.RoundRunning) {
		t.Fatalf("RemoteStatus = %q, want %q", got.Builder.RemoteStatus, remote.RoundRunning)
	}
	if n := countCalls(fr, "GetBinding:"); n != 1 {
		t.Fatalf("GetBinding calls = %d, want 1", n)
	}
}

func TestTickDiscardsFetchWhenBindingIsDoneMidFetch(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRemote{
		getBindingResp:    remote.BindingView{RoundState: remote.RoundRunning},
		roundFileFromResp: io.NopCloser(strings.NewReader("builder log line 1\n")),
	}
	fr.beforeCall = func(call string) {
		if call != "GetBinding:zen:api" {
			return
		}
		if err := st.WithLock(func(tx *store.Tx) error {
			cur, err := tx.Load("api")
			if err != nil {
				return err
			}
			cur.State = store.StateDone
			return tx.Save(cur)
		}); err != nil {
			t.Fatalf("mark done mid-fetch: %v", err)
		}
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateDone {
		t.Fatalf("state = %s, want done", got.State)
	}
	if _, err := st.ReadFile(st.BuilderLogPath("api", 1)); err == nil {
		t.Fatalf("a log row was written for a discarded fetch")
	}
	if n := countCalls(fr, "GetBinding:"); n != 1 {
		t.Fatalf("GetBinding calls = %d, want 1", n)
	}
}

func TestTickDiscardsFetchWhenRoundAdvancedMidFetch(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRemote{
		getBindingResp:    remote.BindingView{RoundState: remote.RoundRunning},
		roundFileFromResp: io.NopCloser(strings.NewReader("builder log line 1\n")),
	}
	fr.beforeCall = func(call string) {
		if call != "GetBinding:zen:api" {
			return
		}
		if err := st.WithLock(func(tx *store.Tx) error {
			cur, err := tx.Load("api")
			if err != nil {
				return err
			}
			cur.Round = 2
			return tx.Save(cur)
		}); err != nil {
			t.Fatalf("advance round mid-fetch: %v", err)
		}
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Round != 2 {
		t.Fatalf("round = %d, want 2", got.Round)
	}
	if got.Builder.RemoteStatus != "" {
		t.Fatalf("RemoteStatus = %q, want it unchanged from the hook's save", got.Builder.RemoteStatus)
	}
	if _, err := st.ReadFile(st.BuilderLogPath("api", 1)); err == nil {
		t.Fatalf("a round-1 log row was written for a discarded fetch")
	}
	if n := countCalls(fr, "GetBinding:"); n != 1 {
		t.Fatalf("GetBinding calls = %d, want 1", n)
	}
}

func TestSyncRemoteFetchesWithoutTheStateLock(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRemote{
		getBindingResp:    remote.BindingView{RoundState: remote.RoundRunning},
		roundFileFromResp: io.NopCloser(strings.NewReader("builder log line 1\n")),
	}
	assertFetchIsUnlocked(t, st, fr)
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if _, err := SyncRemote(context.Background(), rt); err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}

	data, err := rt.Store.ReadFile(st.BuilderLogPath("api", 1))
	if err != nil {
		t.Fatalf("read mirrored log: %v", err)
	}
	if string(data) != "builder log line 1\n" {
		t.Fatalf("log = %q, want the fetched body", string(data))
	}
	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Builder.RemoteStatus != string(remote.RoundRunning) {
		t.Fatalf("RemoteStatus = %q, want %q", got.Builder.RemoteStatus, remote.RoundRunning)
	}
	if n := countCalls(fr, "GetBinding:"); n != 1 {
		t.Fatalf("GetBinding calls = %d, want 1", n)
	}
}

func TestApplyLogMirrorSkipsWhenTheRowMoved(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	logPath := st.BuilderLogPath("api", 1)
	rt := Runtime{Store: st}

	putRow := func(body string) {
		t.Helper()
		if err := st.WithLock(func(tx *store.Tx) error {
			return tx.PutRoundFile("api", 1, logPath, []byte(body))
		}); err != nil {
			t.Fatalf("put row: %v", err)
		}
	}
	apply := func(m *logMirror) {
		t.Helper()
		if err := st.WithLock(func(tx *store.Tx) error {
			applyLogMirror(rt, tx, "api", 1, m)
			return nil
		}); err != nil {
			t.Fatalf("applyLogMirror: %v", err)
		}
	}
	readRow := func() string {
		t.Helper()
		data, err := st.ReadFile(logPath)
		if err != nil {
			t.Fatalf("read row: %v", err)
		}
		return string(data)
	}

	putRow("12345678")
	apply(&logMirror{Path: logPath, Base: 5, Body: []byte("XY")})
	if got := readRow(); got != "12345678" {
		t.Fatalf("row after a moved-row append = %q, want it unchanged", got)
	}

	putRow("12345")
	apply(&logMirror{Path: logPath, Base: 5, Body: []byte("XY")})
	if got := readRow(); got != "12345XY" {
		t.Fatalf("row after an in-place append = %q, want %q", got, "12345XY")
	}

	apply(&logMirror{Path: logPath, Base: -1, Body: []byte("ZZZ")})
	if got := readRow(); got != "ZZZ" {
		t.Fatalf("row after a full replacement = %q, want %q", got, "ZZZ")
	}
}

func TestFetchMatchesOnlyTheSameRoundAndServer(t *testing.T) {
	f := remoteFetch{Name: "api", Server: "zen", Round: 1}
	cases := []struct {
		name   string
		mutate func(*store.Binding)
		want   bool
	}{
		{"same", func(*store.Binding) {}, true},
		{"round advanced", func(b *store.Binding) { b.Round = 2 }, false},
		{"server changed", func(b *store.Binding) { b.Builder.Server = "mars" }, false},
		{"name changed", func(b *store.Binding) { b.Name = "other" }, false},
		{"done", func(b *store.Binding) { b.State = store.StateDone }, false},
		{"paused", func(b *store.Binding) { b.State = store.StatePaused }, false},
		{"not remote", func(b *store.Binding) { b.Builder.Mode = store.Mode("pane") }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := remoteBinding("zen")
			tc.mutate(&b)
			if got := f.matches(b); got != tc.want {
				t.Fatalf("matches = %v, want %v", got, tc.want)
			}
		})
	}
}

// notFound is a round file the round does not carry.
func notFound(kind string) *client.HTTPError {
	return &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
}

// roundClosedRemote is a remote whose round 1 the server reports closed, with
// a report file and no diff, log or stream. Callers override roundFileFunc to
// fail a download or roundBundleResp to drop the bundle.
func roundClosedRemote() *fakeRemote {
	return &fakeRemote{
		getBindingResp:  remote.BindingView{RoundState: remote.RoundClosed, ClosedRound: 1, ResultCommit: "c0ffee"},
		roundBundleResp: io.NopCloser(strings.NewReader("bundle")),
		roundFileFunc: func(_ context.Context, _, _ string, _ int, kind string) (io.ReadCloser, error) {
			if kind == "report" {
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			}
			return nil, notFound(kind)
		},
	}
}

// assertNoFetchTemps fails when a download temp file survives beside the
// round's files: the fetch's release or the apply's rename must have consumed
// every one of them.
func assertNoFetchTemps(t *testing.T, st *store.Store, name string, round int) {
	t.Helper()
	dir := filepath.Dir(st.ReportPath(name, round))
	left, err := filepath.Glob(filepath.Join(dir, "*.fetch.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("download temp files left behind: %v", left)
	}
}

// TestTickCatchesUpWithoutTheStateLock pins the round-close catch-up as a
// fetch without the lock: every download and the absorb see the state lock
// free, and the apply installs the report and queues it under the lock.
func TestTickCatchesUpWithoutTheStateLock(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := roundClosedRemote()
	fr.beforeCall = func(call string) {
		if strings.HasPrefix(call, "RoundFile:") || strings.HasPrefix(call, "RoundBundle:") {
			if !lockFreeWithin(t, st, 2*time.Second) {
				t.Errorf("%s ran while the state lock was held", call)
			}
		}
	}
	tr := &fakeTransport{beforeAbsorb: func() {
		if !lockFreeWithin(t, st, 2*time.Second) {
			t.Errorf("Absorb ran while the state lock was held")
		}
	}}
	rt := Runtime{Store: st, Remote: fr, Transport: tr, Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if _, err := os.Stat(st.ReportPath("api", 1)); err != nil {
		t.Fatalf("round 1 report not installed: %v", err)
	}
	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if !HasEntry(entries, 1, store.DirToPlanner, store.KindReport) {
		t.Fatalf("no round 1 report entry: %+v", entries)
	}
	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Round != 2 {
		t.Errorf("Round = %d, want 2", got.Round)
	}
	if got.Builder.LastKnown != "c0ffee" {
		t.Errorf("LastKnown = %q, want the view's result commit", got.Builder.LastKnown)
	}
	if n := countCalls(fr, "Ack:"); n != 1 {
		t.Errorf("Ack calls = %d, want 1", n)
	}
	if n := countCalls(fr, "RoundBundle:"); n != 1 {
		t.Errorf("RoundBundle calls = %d, want 1", n)
	}
	assertNoFetchTemps(t, st, "api", 1)
}

// TestCatchUpDiscardedWhenDoneMidFetch pins that a binding marked DONE while
// the catch-up is still downloading is left alone: the report stays a temp
// file, no ack goes out, and nothing is queued.
func TestCatchUpDiscardedWhenDoneMidFetch(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := roundClosedRemote()
	tr := &fakeTransport{beforeAbsorb: func() {
		if err := st.WithLock(func(tx *store.Tx) error {
			cur, err := tx.Load("api")
			if err != nil {
				return err
			}
			cur.State = store.StateDone
			return tx.Save(cur)
		}); err != nil {
			t.Fatalf("mark done mid-fetch: %v", err)
		}
	}}
	rt := Runtime{Store: st, Remote: fr, Transport: tr, Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateDone {
		t.Fatalf("state = %s, want done", got.State)
	}
	if _, err := os.Stat(st.ReportPath("api", 1)); err == nil {
		t.Fatalf("a report was installed for a discarded fetch")
	}
	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if HasEntry(entries, 1, store.DirToPlanner, store.KindReport) {
		t.Fatalf("a report entry exists for a discarded fetch: %+v", entries)
	}
	if n := countCalls(fr, "Ack:"); n != 0 {
		t.Fatalf("Ack calls = %d, want 0", n)
	}
	assertNoFetchTemps(t, st, "api", 1)
}

// TestCatchUpFetchAbortLeavesNothing pins that a non-404 download failure
// aborts the fetch before the bundle and leaves no report and no temp file.
func TestCatchUpFetchAbortLeavesNothing(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := roundClosedRemote()
	fr.roundFileFunc = func(_ context.Context, _, _ string, _ int, kind string) (io.ReadCloser, error) {
		switch kind {
		case "report":
			return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
		case "diff":
			return nil, &client.HTTPError{Status: 500, Body: remote.ErrorBody{Code: "boom", Message: "diff read failed"}}
		default:
			return nil, notFound(kind)
		}
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if _, err := os.Stat(st.ReportPath("api", 1)); err == nil {
		t.Fatalf("a report was installed from a fetch that aborted on the diff")
	}
	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if HasEntry(entries, 1, store.DirToPlanner, store.KindReport) {
		t.Fatalf("a report entry exists for an aborted fetch: %+v", entries)
	}
	if n := countCalls(fr, "Ack:"); n != 0 {
		t.Fatalf("Ack calls = %d, want 0", n)
	}
	if n := countCalls(fr, "RoundBundle:"); n != 0 {
		t.Fatalf("RoundBundle calls = %d, want 0", n)
	}
	assertNoFetchTemps(t, st, "api", 1)
}

// TestTickCatchUpMissingReportHalts pins the reportless close through the tick
// path: a report 404 on a round the server did not stop halts with today's
// message.
func TestTickCatchUpMissingReportHalts(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundClosed, ClosedRound: 1},
		roundFileFunc: func(_ context.Context, _, _ string, _ int, kind string) (io.ReadCloser, error) {
			return nil, notFound(kind)
		},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you", got.State)
	}
	if !strings.Contains(got.Halt, "closed round 1 without a report file") {
		t.Errorf("Halt = %q, want the existing reportless-close text", got.Halt)
	}
}

// TestTickAcksWithoutTheStateLock pins the round-close ack as a call outside
// the state lock: it runs after the apply committed its bookkeeping, the
// report is queued afterwards, and no download temp survives.
func TestTickAcksWithoutTheStateLock(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := roundClosedRemote()
	fr.roundBundleResp = nil
	fr.beforeCall = func(call string) {
		if !strings.HasPrefix(call, "Ack:") {
			return
		}
		got, err := st.Load("api")
		if err != nil {
			t.Fatalf("load during ack: %v", err)
		}
		if got.Builder.LastKnown != "c0ffee" {
			t.Errorf("LastKnown during ack = %q, want the committed result commit", got.Builder.LastKnown)
		}
		if !lockFreeWithin(t, st, 2*time.Second) {
			t.Errorf("%s ran while the state lock was held", call)
		}
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if !HasEntry(entries, 1, store.DirToPlanner, store.KindReport) {
		t.Fatalf("no round 1 report entry: %+v", entries)
	}
	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Round != 2 {
		t.Errorf("Round = %d, want 2", got.Round)
	}
	if n := countCalls(fr, "Ack:"); n != 1 {
		t.Errorf("Ack calls = %d, want 1", n)
	}
	assertNoFetchTemps(t, st, "api", 1)
}

// TestSyncRemoteAcksWithoutTheStateLock pins the same property on the
// no-daemon read path, which reaches the ack through a different call site:
// the ack runs with the state lock free, after the apply committed, and the
// report is queued afterwards.
func TestSyncRemoteAcksWithoutTheStateLock(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := roundClosedRemote()
	fr.roundBundleResp = nil
	fr.beforeCall = func(call string) {
		if !strings.HasPrefix(call, "Ack:") {
			return
		}
		got, err := st.Load("api")
		if err != nil {
			t.Fatalf("load during ack: %v", err)
		}
		if got.Builder.LastKnown != "c0ffee" {
			t.Errorf("LastKnown during ack = %q, want the committed result commit", got.Builder.LastKnown)
		}
		if !lockFreeWithin(t, st, 2*time.Second) {
			t.Errorf("%s ran while the state lock was held", call)
		}
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	synced, err := SyncRemote(ctx, rt)
	if err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}
	if synced != 1 {
		t.Errorf("synced = %d, want 1", synced)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if !HasEntry(entries, 1, store.DirToPlanner, store.KindReport) {
		t.Fatalf("no round 1 report entry: %+v", entries)
	}
	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Round != 2 {
		t.Errorf("Round = %d, want 2", got.Round)
	}
	if n := countCalls(fr, "Ack:"); n != 1 {
		t.Errorf("Ack calls = %d, want 1", n)
	}
	assertNoFetchTemps(t, st, "api", 1)
}
