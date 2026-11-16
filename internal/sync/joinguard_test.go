package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// guardBucket is a bucket holding the given segments, one object per size, the
// R2 figure the guard estimates from the listing alone.
func guardBucket(t testing.TB, segments map[string][]int64) *blobstore.MemBlobStore {
	t.Helper()
	store := blobstore.NewMemBlobStore(nil)
	for origin, sizes := range segments {
		for i, size := range sizes {
			body := make([]byte, size)
			if err := store.Put(context.Background(), blobstore.SegmentKey(origin, i), bytes.NewReader(body), size); err != nil {
				t.Fatalf("put segment %s/%d: %v", origin, i, err)
			}
		}
	}
	return store
}

// guardEnabler builds a join-mode enabler with quota 1000 and 400 already used
// this month, guarding the given bucket; the caller hands guardJoined the
// transport the Turso figure is read from.
func guardEnabler(t *testing.T, store blobstore.BlobStore, yes bool, now time.Time) (*Enabler, *joinLog) {
	t.Helper()
	shared, local := joinPair(t, "m1")
	storeR2(t, local)
	if err := PutSettings(local, Settings{QuotaTursoSync: 1000}, now); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := addBytes(local, now, ByteCounters{TursoPush: 300, TursoPull: 100}); err != nil {
		t.Fatalf("addBytes: %v", err)
	}
	log := &joinLog{MemTransport: synclog.NewMemTransport("m1")}
	e := &Enabler{
		Request: EnableRequest{Local: local, Shared: shared, URL: joinRemote, Token: []byte(joinToken), Join: true, Yes: yes},
		Blobs:   func(R2Secrets) blobstore.BlobStore { return store },
		Now:     func() time.Time { return now },
	}
	return e, log
}

// guardedEnable runs a join's guard with quota 1000 and 400 already used this
// month, the Turso figure read from a worker reporting turso, against a bucket
// holding the given segments.
func guardedEnable(t *testing.T, store blobstore.BlobStore, turso int64, yes bool, now time.Time) (JoinEstimate, bool, error) {
	t.Helper()
	e, log := guardEnabler(t, store, yes, now)
	return e.guardJoined(&meteredLog{joinLog: log, step: turso})
}

// failingStats is a transport whose readings refuse, the way a worker that went
// away answers.
type failingStats struct {
	synclog.LogTransport
	err error
}

func (f failingStats) Stats() (synclog.Stats, error) { return synclog.Stats{}, f.err }

func TestJoinGuardRefusesOverRemainder(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	cases := []struct {
		name    string
		turso   int64
		refused bool
	}{
		{"under what is left", 599, false},
		{"exactly what is left", 600, false},
		{"one byte over what is left", 601, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := guardedEnable(t, guardBucket(t, map[string][]int64{"m2": {5000}}), tc.turso, false, now)
			if got := errors.Is(err, ErrJoinOverQuota); got != tc.refused {
				t.Fatalf("guardJoined = %v, refused = %v, want %v", err, got, tc.refused)
			}
			if tc.refused {
				for _, want := range []string{"601 B", "600 B is left for this machine", "1.0 kB", "--yes", "the account's traffic cannot be seen from this machine"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("refusal %q lacks %q", err, want)
					}
				}
				if strings.Contains(err.Error(), "account quota") {
					t.Errorf("refusal %q quotes an account figure it cannot see", err)
				}
			}
		})
	}
}

func TestJoinGuardYesProceeds(t *testing.T) {
	t.Parallel()
	est, guarded, err := guardedEnable(t, guardBucket(t, map[string][]int64{"m2": {10}}), 5000, true, time.Now().UTC())
	if err != nil {
		t.Fatalf("guardJoined with --yes: %v", err)
	}
	if !guarded || est.TursoBytes != 5000 || !est.Known {
		t.Errorf("estimate = %+v, guarded %v, want the known 5000 reported", est, guarded)
	}
	if est.Segments != 1 || est.SegmentBytes != 10 {
		t.Errorf("estimate = %+v, want the one 10-byte segment reported", est)
	}
}

func TestJoinGuardUnknownEstimateProceeds(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e, _ := guardEnabler(t, guardBucket(t, map[string][]int64{"m2": {9_999_999}}), false, now)
	est, guarded, err := e.guardJoined(failingStats{err: errors.New("the worker went away")})
	if err != nil {
		t.Fatalf("guardJoined with no Turso reading: %v", err)
	}
	if !guarded || est.Known {
		t.Errorf("estimate = %+v, guarded %v, want a guarded join with an unknown estimate", est, guarded)
	}
	if est.Segments != 1 || est.SegmentBytes != 9_999_999 {
		t.Errorf("estimate = %+v, want the one segment reported", est)
	}
}

func TestJoinGuardReadableZeroIsKnown(t *testing.T) {
	t.Parallel()
	est, guarded, err := guardedEnable(t, guardBucket(t, map[string][]int64{"m2": {10}}), 0, false, time.Now().UTC())
	if err != nil {
		t.Fatalf("guardJoined with a zero Turso reading: %v", err)
	}
	if !guarded || !est.Known || est.TursoBytes != 0 {
		t.Errorf("estimate = %+v, guarded %v, want a guarded join with a known zero Turso figure", est, guarded)
	}
}

// noGetStore fails the test on any fetch: the estimate reads the listing alone.
type noGetStore struct {
	blobstore.BlobStore
	t *testing.T
}

func (s noGetStore) Get(ctx context.Context, key string, w io.Writer) (int64, error) {
	s.t.Errorf("Get(%s): the estimate must not fetch objects", key)
	return 0, errors.New("Get called")
}

// listFailStore lists nothing: its listing refuses, the way a bucket the join
// cannot read answers.
type listFailStore struct {
	blobstore.BlobStore
	err error
}

func (s listFailStore) List(context.Context, string) ([]blobstore.Object, error) {
	return nil, s.err
}

// A refused listing reaches the join estimate as an error, never as a zero
// segment count: the guard must report that nothing was learned.
func TestEstimateJoinReturnsAListingError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("the bucket cannot be listed")
	est, err := EstimateJoin(context.Background(), listFailStore{BlobStore: blobstore.NewMemBlobStore(nil), err: sentinel})
	if !errors.Is(err, sentinel) {
		t.Fatalf("EstimateJoin = %+v, %v, want the listing error", est, err)
	}
	if est != (JoinEstimate{}) {
		t.Errorf("estimate = %+v, want the zero estimate with the error", est)
	}
}

// The estimate counts the bucket's segments from the listing alone: bodies, old
// size objects and every other key are ignored by shape, and nothing is fetched.
func TestEstimateJoinCountsSegmentsFromTheListing(t *testing.T) {
	t.Parallel()
	base := time.Unix(0, 0).UTC()
	cur := base
	store := blobstore.NewMemBlobStore(func() time.Time {
		cur = cur.Add(time.Second)
		return cur
	})
	put := func(key string, size int64) {
		t.Helper()
		if err := store.Put(context.Background(), key, bytes.NewReader(make([]byte, size)), size); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	put(blobstore.SegmentKey("m2", 0), 100)
	put(blobstore.SegmentKey("m2", 1), 200)
	put(blobstore.SegmentKey("m3", 5), 300)
	wantLatest := base.Add(3 * time.Second)
	put(blobstore.Key("m2", strings.Repeat("ab", 32)), 7)
	put("m2/log-size.json", 9)
	put("m2/notes.txt", 2)
	put("loose", 1)
	est, err := EstimateJoin(context.Background(), noGetStore{BlobStore: store, t: t})
	if err != nil {
		t.Fatalf("EstimateJoin: %v", err)
	}
	if est.Segments != 3 || est.SegmentBytes != 600 {
		t.Errorf("estimate = %+v, want 3 segments of 600 bytes", est)
	}
	if !est.Latest.Equal(wantLatest) {
		t.Errorf("latest = %v, want %v", est.Latest, wantLatest)
	}
	if est.Known || est.TursoBytes != 0 {
		t.Errorf("estimate = %+v, want the Turso half left unset", est)
	}
	if store.Gets != 0 {
		t.Errorf("Gets = %d, want the estimate to fetch nothing", store.Gets)
	}
}

// A 100k-key listing is one List the store answers whole: the estimate counts
// every segment and no size object, without fetching.
func TestEstimateJoinCountsA100kKeyListing(t *testing.T) {
	t.Parallel()
	store := blobstore.NewMemBlobStore(nil)
	const n = 100_000
	var want int64
	for i := range n {
		size := int64(i%251 + 1)
		want += size
		if err := store.Put(context.Background(), blobstore.SegmentKey("m2", i), bytes.NewReader(make([]byte, size)), size); err != nil {
			t.Fatalf("put segment %d: %v", i, err)
		}
	}
	for i := range 100 {
		origin := fmt.Sprintf("old-%d", i)
		body := []byte(`{"entries":1,"bytes":5000}`)
		if err := store.Put(context.Background(), origin+"/log-size.json", bytes.NewReader(body), int64(len(body))); err != nil {
			t.Fatalf("put size %d: %v", i, err)
		}
	}
	start := time.Now()
	est, err := EstimateJoin(context.Background(), store)
	took := time.Since(start)
	if err != nil {
		t.Fatalf("EstimateJoin: %v", err)
	}
	if est.Segments != n || est.SegmentBytes != want {
		t.Errorf("segments = %d, bytes = %d, want %d segments of %d bytes", est.Segments, est.SegmentBytes, n, want)
	}
	if est.Known || est.TursoBytes != 0 {
		t.Errorf("estimate = %+v, want the Turso half left unset", est)
	}
	if store.Gets != 0 {
		t.Errorf("Gets = %d, want the estimate to fetch nothing", store.Gets)
	}
	t.Logf("100k-key listing estimated in %s", took)
}

// meteredLog reports cumulative byte totals that grow on every reading, as a
// worker's do, and refuses an append when told to.
type meteredLog struct {
	*joinLog
	step int64
	last int64
}

func (m *meteredLog) Stats() (synclog.Stats, error) {
	s, err := m.joinLog.Stats()
	m.last += m.step
	s.TursoReceived = m.last
	return s, err
}

func TestJoinBytesCountedAcrossWorkerRestart(t *testing.T) {
	const total = 300
	shared, local := joinPair(t, "m1")
	passGate(t, shared)
	statements := make([]string, 0, total)
	for i := 0; i < total; i++ {
		statements = append(statements, joinBinding(fmt.Sprintf("b%03d", i), "m1"))
	}
	seedJoin(t, shared, statements...)

	first := &meteredLog{joinLog: &joinLog{MemTransport: synclog.NewMemTransport("m1"), failAt: 2, err: errors.New("the worker died")}, step: 100}
	if _, err := runEnable(t, shared, local, []byte(joinToken), first); err == nil {
		t.Fatal("a join whose worker died succeeded")
	}
	month, err := ReadBytes(local, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if month.TursoPull != first.last || first.last <= 100 {
		t.Fatalf("after the dead worker, counted %d of the %d it reported, want all of it and more than the bootstrap", month.TursoPull, first.last)
	}

	second := &meteredLog{joinLog: &joinLog{MemTransport: first.MemTransport}, step: 100}
	if _, err := runEnable(t, shared, local, nil, second); err != nil {
		t.Fatalf("the resumed join: %v", err)
	}
	month, err = ReadBytes(local, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if want := first.last + second.last; month.TursoPull != want {
		t.Errorf("counted %d, want %d: both workers' bytes", month.TursoPull, want)
	}
}

// joinNow pins the account tests mid-month, so rows naming "2026-10" are the
// month holding now.
var joinNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// accountJoin builds a join-mode enabler at joinNow with quota 1000 and the
// given own counters, the Turso figure read from a worker reporting est, with
// its log.
func accountJoin(t *testing.T, own ByteCounters, est int64) (*Enabler, *meteredLog) {
	t.Helper()
	shared, local := joinPair(t, "m1")
	storeR2(t, local)
	if err := PutSettings(local, Settings{QuotaTursoSync: 1000}, joinNow); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := addBytes(local, joinNow, own); err != nil {
		t.Fatalf("addBytes: %v", err)
	}
	log := &meteredLog{joinLog: &joinLog{MemTransport: synclog.NewMemTransport("m1")}, step: est}
	e := &Enabler{
		Request: EnableRequest{Local: local, Shared: shared, URL: joinRemote, Token: []byte(joinToken), Join: true},
		Blobs:   func(R2Secrets) blobstore.BlobStore { return guardBucket(t, map[string][]int64{"m2": {10}}) },
		Now:     func() time.Time { return joinNow },
	}
	return e, log
}

// seedTraffic publishes one row per origin for the month holding joinNow, the
// way other machines' publishes leave them; the joiner's own handle never
// publishes, because a join reads before it ever writes.
func seedTraffic(t *testing.T, log *meteredLog, rows map[string][2]int64) {
	t.Helper()
	month := joinNow.UTC().Format("2006-01")
	for origin, counts := range rows {
		if _, err := log.OnLog(origin).PublishTraffic(synclog.TrafficRow{Month: month, Push: counts[0], Pull: counts[1], At: joinNow}); err != nil {
			t.Fatalf("publish %s: %v", origin, err)
		}
	}
}

// The account judgement reads the transport's rows, not this machine's: own
// use is 100, other origins' rows total 800, and an estimate of 601 sits
// between the two remainders -- refused on the account total, allowed on local
// figures alone. Exactly the account remainder still fits, and an unknown
// estimate proves nothing.
func TestGuardJoinJudgesTheAccountNotTheMachine(t *testing.T) {
	t.Parallel()
	month := joinNow.UTC().Format("2006-01")
	rows := map[string]synclog.TrafficRow{
		"m2": {Month: month, Push: 400, Pull: 100},
		"m3": {Month: month, Push: 200, Pull: 100},
	}
	own := ByteCounters{TursoPush: 60, TursoPull: 40}
	for _, tc := range []struct {
		name    string
		est     JoinEstimate
		refused bool
	}{
		{"under the account remainder", JoinEstimate{TursoBytes: 99, Known: true}, false},
		{"exactly the account remainder", JoinEstimate{TursoBytes: 100, Known: true}, false},
		{"one byte over the account remainder", JoinEstimate{TursoBytes: 101, Known: true}, true},
		{"between the two remainders", JoinEstimate{TursoBytes: 601, Known: true}, true},
		{"unknown proves nothing", JoinEstimate{TursoBytes: 5000}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := GuardJoinAccount(tc.est, 1000, rows, own, "m1", joinNow)
			if got := errors.Is(err, ErrJoinOverQuota); got != tc.refused {
				t.Fatalf("GuardJoinAccount(%v) = %v, refused = %v, want %v", tc.est, err, got, tc.refused)
			}
			if tc.refused {
				for _, want := range []string{"is left of this month's 1.0 kB account quota", "across 3 machines", "--yes"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("refusal %q lacks %q", err, want)
					}
				}
			}
		})
	}
	if err := GuardJoin(JoinEstimate{TursoBytes: 601, Known: true}, 1000, 100); err != nil {
		t.Errorf("the local judgement refuses the 601 estimate: %v", err)
	}
}

// The note says plainly that the account cannot be seen, with the newest local
// snapshot's month and age when one exists: two stale rows name 2026-09 as the
// newest. With no snapshot there is neither month nor age, and no figure the
// refusal could be quoting as the account's.
func TestNoAccountNoteNamesTheNewestSnapshot(t *testing.T) {
	t.Parallel()
	present := AccountSnapshot{
		ReadAt: joinNow.Add(-26 * time.Hour),
		Rows: map[string]synclog.TrafficRow{
			"m2": {Month: "2026-09", Push: 1},
			"m3": {Month: "2026-08", Push: 2},
		},
	}
	note := noAccountNote(present, joinNow)
	for _, want := range []string{"the account's traffic cannot be seen", "2026-09", "26h0m0s"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q lacks %q", note, want)
		}
	}
	if strings.Contains(note, "2026-08") {
		t.Errorf("note %q names a row that is not the newest", note)
	}

	future := noAccountNote(AccountSnapshot{
		ReadAt: joinNow.Add(time.Hour),
		Rows: map[string]synclog.TrafficRow{
			"m2": {Month: "2026-09", Push: 1},
		},
	}, joinNow)
	if !strings.Contains(future, "read 0s ago") {
		t.Errorf("note %q lacks the clamped zero age", future)
	}
	if strings.Contains(future, "read -") {
		t.Errorf("note %q carries a negative duration", future)
	}
	for _, want := range []string{"the account's traffic cannot be seen", "2026-09"} {
		if !strings.Contains(future, want) {
			t.Errorf("note %q lacks %q", future, want)
		}
	}

	bare := noAccountNote(AccountSnapshot{}, joinNow)
	if !strings.Contains(bare, "the account's traffic cannot be seen") {
		t.Errorf("note %q states no plain no-visibility sentence", bare)
	}
	for _, want := range []string{"snapshot", "2026", "account quota", "is left"} {
		if strings.Contains(bare, want) {
			t.Errorf("note %q carries %q with no snapshot to quote", bare, want)
		}
	}
}

// A busy account refuses the join naming the account figure: own use is 100,
// the other origins' rows total 800, and the 601 estimate sits between the two
// remainders. The guard reads only: the account markers stay untouched.
func TestGuardJoinedRefusesABusyAccount(t *testing.T) {
	t.Parallel()
	e, log := accountJoin(t, ByteCounters{TursoPush: 60, TursoPull: 40}, 601)
	seedTraffic(t, log, map[string][2]int64{"m2": {400, 100}, "m3": {200, 100}})
	est, guarded, err := e.guardJoined(log)
	if !errors.Is(err, ErrJoinOverQuota) {
		t.Fatalf("guardJoined = %+v, %v, want the over-remainder refusal", est, err)
	}
	if !guarded || est.TursoBytes != 601 {
		t.Errorf("result = %+v, guarded %v, want the 601 estimate reported guarded", est, guarded)
	}
	for _, want := range []string{"601 B", "100 B is left", "1.0 kB account quota", "across 3 machines", "--yes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
	for _, key := range []string{KeyAccount, KeyTrafficPublished} {
		if body, ok, kerr := e.Request.Local.KVGet(key); kerr != nil || ok {
			t.Errorf("marker %s = %q, %v, want it untouched by the guard", key, body, kerr)
		}
	}
}

// An empty transport names no account, so the join is judged on local figures
// with the note that the account could not be seen: the 601 estimate tops the
// 600 left for this machine, and no account figure is quoted.
func TestGuardJoinedFallsBackWithoutAccountRows(t *testing.T) {
	t.Parallel()
	e, log := accountJoin(t, ByteCounters{TursoPush: 300, TursoPull: 100}, 601)
	_, _, err := e.guardJoined(log)
	if !errors.Is(err, ErrJoinOverQuota) {
		t.Fatalf("guardJoined = %v, want the local over-remainder refusal", err)
	}
	for _, want := range []string{"600 B is left for this machine", "the account's traffic cannot be seen from this machine"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "account quota") {
		t.Errorf("refusal %q quotes an account figure it cannot see", err)
	}
}

// The fallback names the newest local snapshot's month and age when one
// exists: the rows are two stale months, so 2026-09 is the newest.
func TestGuardJoinedFallsBackNamingTheNewestSnapshot(t *testing.T) {
	t.Parallel()
	e, log := accountJoin(t, ByteCounters{TursoPush: 300, TursoPull: 100}, 601)
	seedMeterSnapshot(t, e.Request.Local, AccountSnapshot{
		ReadAt: joinNow.Add(-26 * time.Hour),
		Rows: map[string]synclog.TrafficRow{
			"m2": {Month: "2026-09", Push: 1},
			"m3": {Month: "2026-08", Push: 2},
		},
	})
	_, _, err := e.guardJoined(log)
	if !errors.Is(err, ErrJoinOverQuota) {
		t.Fatalf("guardJoined = %v, want the local over-remainder refusal", err)
	}
	for _, want := range []string{"the account's traffic cannot be seen", "2026-09", "26h0m0s"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
}

// A snapshot that will not parse reads as none: the refusal still says the
// account cannot be seen, and names no month.
func TestGuardJoinedFallsBackPastAnUnreadableSnapshot(t *testing.T) {
	t.Parallel()
	e, log := accountJoin(t, ByteCounters{TursoPush: 300, TursoPull: 100}, 601)
	if err := e.Request.Local.KVPut(KeyAccount, []byte(`{"read_at":"not-a-time","rows":{}}`)); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	_, _, err := e.guardJoined(log)
	if !errors.Is(err, ErrJoinOverQuota) {
		t.Fatalf("guardJoined = %v, want the local over-remainder refusal", err)
	}
	if !strings.Contains(err.Error(), "the account's traffic cannot be seen") {
		t.Errorf("refusal %q states no plain no-visibility sentence", err)
	}
	if strings.Contains(err.Error(), "snapshot") {
		t.Errorf("refusal %q names a snapshot it could not read", err)
	}
}

// Rows from another month are no account for this one: last month's rows leave
// the join judged on local figures with the note.
func TestGuardJoinedIgnoresRowsFromAnotherMonth(t *testing.T) {
	t.Parallel()
	e, log := accountJoin(t, ByteCounters{TursoPush: 300, TursoPull: 100}, 601)
	if _, err := log.OnLog("m2").PublishTraffic(synclog.TrafficRow{Month: "2026-09", Push: 10_000, Pull: 0, At: joinNow}); err != nil {
		t.Fatalf("publish traffic: %v", err)
	}
	_, _, err := e.guardJoined(log)
	if !errors.Is(err, ErrJoinOverQuota) {
		t.Fatalf("guardJoined = %v, want the local over-remainder refusal", err)
	}
	if !strings.Contains(err.Error(), "the account's traffic cannot be seen") {
		t.Errorf("refusal %q states no plain no-visibility sentence", err)
	}
}

// failTraffic answers the read with a failure, the way a log that went away
// mid-join does.
type failTraffic struct {
	synclog.LogTransport
	err error
}

func (f failTraffic) ReadTraffic() (map[string]synclog.TrafficRow, error) { return nil, f.err }

// A read error and a transport without the reader fall back rather than fail:
// the 100 estimate fits the local remainder, so the join proceeds.
func TestGuardJoinedFallsBackRatherThanFail(t *testing.T) {
	t.Parallel()
	t.Run("read error", func(t *testing.T) {
		t.Parallel()
		e, log := accountJoin(t, ByteCounters{TursoPush: 60, TursoPull: 40}, 100)
		if _, _, err := e.guardJoined(failTraffic{LogTransport: log, err: errors.New("the log went away")}); err != nil {
			t.Errorf("guardJoined = %v, want the fallback to judge locally and allow", err)
		}
	})
	t.Run("no reader", func(t *testing.T) {
		t.Parallel()
		e, _ := accountJoin(t, ByteCounters{TursoPush: 60, TursoPull: 40}, 100)
		if _, _, err := e.guardJoined(&runningLog{err: errors.New("not a log")}); err != nil {
			t.Errorf("guardJoined = %v, want the fallback to judge locally and allow", err)
		}
	})
	t.Run("no transport", func(t *testing.T) {
		t.Parallel()
		e, _ := accountJoin(t, ByteCounters{TursoPush: 60, TursoPull: 40}, 100)
		if _, _, err := e.guardJoined(nil); err != nil {
			t.Errorf("guardJoined = %v, want the fallback to judge locally and allow", err)
		}
	})
}

// --yes joins past the guard's refusal, whichever figures it was read against.
func TestGuardJoinedYesJoinsPastABusyAccount(t *testing.T) {
	t.Parallel()
	e, log := accountJoin(t, ByteCounters{TursoPush: 60, TursoPull: 40}, 601)
	e.Request.Yes = true
	seedTraffic(t, log, map[string][2]int64{"m2": {400, 100}, "m3": {200, 100}})
	if _, _, err := e.guardJoined(log); err != nil {
		t.Errorf("guardJoined with --yes = %v, want the join to proceed", err)
	}
}

// stuckUntilUnlocked blocks its read until the test releases it, so the bound
// fires while the sender is still waiting.
type stuckUntilUnlocked struct {
	synclog.LogTransport
	release <-chan struct{}
}

func (s stuckUntilUnlocked) ReadTraffic() (map[string]synclog.TrafficRow, error) {
	<-s.release
	return nil, errors.New("released")
}

// A read that never answers must give up rather than hang the join on it.
func TestReadTrafficBoundedGivesUp(t *testing.T) {
	t.Parallel()
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	if _, err := readTrafficBounded(stuckUntilUnlocked{release: unblock}, 50*time.Millisecond); err == nil {
		t.Error("a read that never answers was let through")
	}
}
