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
				for _, want := range []string{"601 B", "600 B is left", "1.0 kB", "--yes"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("refusal %q lacks %q", err, want)
					}
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
