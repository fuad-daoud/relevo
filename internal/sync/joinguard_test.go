package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// guardBucket is a bucket in which each named origin has published a log of the
// given size, plus one body of bodyBytes.
func guardBucket(t testing.TB, bodyBytes int, sizes map[string]int64) *blobstore.MemBlobStore {
	t.Helper()
	store := blobstore.NewMemBlobStore(nil)
	put := func(key string, body []byte) {
		if err := store.Put(context.Background(), key, bytes.NewReader(body), int64(len(body))); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	if bodyBytes > 0 {
		put(blobstore.Key("m2", strings.Repeat("ab", 32)), make([]byte, bodyBytes))
	}
	for origin, size := range sizes {
		body, _ := json.Marshal(LogSize{Entries: 1, Bytes: size, At: time.Unix(0, 0).UTC()})
		put(blobstore.SizeKey(origin), body)
	}
	return store
}

// guardedEnable runs an enable whose quota is 1000 bytes with 400 already used
// this month, against the given bucket.
func guardedEnable(t *testing.T, store blobstore.BlobStore, yes bool, now time.Time) (EnableResult, error) {
	t.Helper()
	shared, local := joinPair(t, "m1")
	passGate(t, shared)
	storeR2(t, local)
	if err := PutSettings(local, Settings{QuotaTursoSync: 1000}, now); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := addBytes(local, now, ByteCounters{TursoPush: 300, TursoPull: 100}); err != nil {
		t.Fatalf("addBytes: %v", err)
	}
	enabler := &Enabler{
		Request:   EnableRequest{Local: local, Shared: shared, URL: joinRemote, Token: []byte(joinToken), Yes: yes},
		Transport: synclog.NewMemTransport("m1"),
		Blobs:     func(R2Secrets) blobstore.BlobStore { return store },
		Now:       func() time.Time { return now },
	}
	return enabler.Enable()
}

func TestJoinGuardRefusesOverRemainder(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	cases := []struct {
		name    string
		sizes   map[string]int64
		refused bool
	}{
		{"under what is left", map[string]int64{"m2": 599}, false},
		{"exactly what is left", map[string]int64{"m2": 300, "m3": 300}, false},
		{"one byte over what is left", map[string]int64{"m2": 300, "m3": 301}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := guardedEnable(t, guardBucket(t, 5000, tc.sizes), false, now)
			if got := errors.Is(err, ErrJoinOverQuota); got != tc.refused {
				t.Fatalf("Enable = %v, refused = %v, want %v", err, got, tc.refused)
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
	res, err := guardedEnable(t, guardBucket(t, 0, map[string]int64{"m2": 5000}), true, time.Now().UTC())
	if err != nil {
		t.Fatalf("Enable with --yes: %v", err)
	}
	if !res.Guarded || res.Estimate.TursoBytes != 5000 {
		t.Errorf("result = %+v, want the estimate reported", res)
	}
}

func TestJoinGuardUnknownEstimateProceeds(t *testing.T) {
	t.Parallel()
	res, err := guardedEnable(t, guardBucket(t, 9_999_999, nil), false, time.Now().UTC())
	if err != nil {
		t.Fatalf("Enable with no published size: %v", err)
	}
	if !res.Guarded || res.Estimate.Known {
		t.Errorf("result = %+v, want a guarded join with an unknown estimate", res)
	}
	if res.Estimate.R2Bytes != 9_999_999 {
		t.Errorf("R2 bytes = %d, want the body total reported", res.Estimate.R2Bytes)
	}
}

func TestEstimateJoinIgnoresObjectsThatAreNeitherBodiesNorSizes(t *testing.T) {
	t.Parallel()
	store := guardBucket(t, 10, map[string]int64{"m2": 7})
	for key, body := range map[string]string{"m2/notes.txt": "xx", "m3/log-size.json": "not json", "loose": "y"} {
		if err := store.Put(context.Background(), key, strings.NewReader(body), int64(len(body))); err != nil {
			t.Fatal(err)
		}
	}
	est, err := EstimateJoin(context.Background(), store)
	if err != nil {
		t.Fatalf("EstimateJoin: %v", err)
	}
	if est != (JoinEstimate{R2Bytes: 10, TursoBytes: 7, Known: true}) {
		t.Errorf("estimate = %+v", est)
	}
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

// orderedLog records the order of the calls the join test cares about.
type orderedLog struct {
	*joinLog
	events []string
}

func (o *orderedLog) Append(e []synclog.Entry) ([]synclog.Entry, error) {
	out, err := o.joinLog.Append(e)
	if err == nil {
		o.events = append(o.events, "append")
	}
	return out, err
}

func (o *orderedLog) PublishMarks(m map[string]int, at time.Time) error {
	o.events = append(o.events, "publish")
	return o.joinLog.PublishMarks(m, at)
}

func TestJoinPublishesMarksAfterReconcile(t *testing.T) {
	for _, failing := range []bool{false, true} {
		t.Run(fmt.Sprintf("append fails %v", failing), func(t *testing.T) {
			shared, local := joinPair(t, "m1")
			passGate(t, shared)
			seedJoin(t, shared, joinBinding("b1", "m1"), joinBinding("b2", "m1"))
			log := &orderedLog{joinLog: &joinLog{MemTransport: synclog.NewMemTransport("m1")}}
			if failing {
				log.failAt, log.err = 1, errors.New("the worker died")
			}
			_, err := runEnable(t, shared, local, []byte(joinToken), log)
			if failing {
				if err == nil {
					t.Fatal("a join whose append failed succeeded")
				}
				if len(log.events) != 0 {
					t.Fatalf("events = %v, want no marks published for a join that did not reconcile", log.events)
				}
				return
			}
			if err != nil {
				t.Fatalf("Enable: %v", err)
			}
			want := []string{"append", "publish"}
			if fmt.Sprint(log.events) != fmt.Sprint(want) {
				t.Fatalf("events = %v, want %v", log.events, want)
			}
		})
	}
}

// joinNow pins the account tests mid-month, so rows naming "2026-10" are the
// month holding now.
var joinNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// accountJoin builds a join-mode enabler at joinNow with quota 1000, the given
// own counters, and a bucket holding est Turso bytes, with its log.
func accountJoin(t *testing.T, own ByteCounters, est int64) (*Enabler, *joinLog) {
	t.Helper()
	shared, local := joinPair(t, "m1")
	storeR2(t, local)
	if err := PutSettings(local, Settings{QuotaTursoSync: 1000}, joinNow); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := addBytes(local, joinNow, own); err != nil {
		t.Fatalf("addBytes: %v", err)
	}
	log := &joinLog{MemTransport: synclog.NewMemTransport("m1")}
	e := &Enabler{
		Request:   EnableRequest{Local: local, Shared: shared, URL: joinRemote, Token: []byte(joinToken), Join: true},
		Transport: log,
		Blobs:     func(R2Secrets) blobstore.BlobStore { return guardBucket(t, 0, map[string]int64{"m2": est}) },
		Now:       func() time.Time { return joinNow },
	}
	return e, log
}

// seedTraffic publishes one row per origin for the month holding joinNow, the
// way other machines' publishes leave them; the joiner's own handle never
// publishes, because a join reads before it ever writes.
func seedTraffic(t *testing.T, log *joinLog, rows map[string][2]int64) {
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
