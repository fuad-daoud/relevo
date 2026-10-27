package sync

// The steady loop's publish of this machine's traffic and the account read it
// feeds: when a publish is due, what a success leaves behind, what a failure
// does not, and how the snapshot and the live counters fold into a total.
// They run against the in-memory log, so no case reaches a network.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// trafficSpy is the in-memory log with a count of the traffic publishes that
// reached it, so a case reads how often the loop published rather than
// inferring it from the rows the log holds afterwards.
type trafficSpy struct {
	*synclog.MemTransport
	publishes int
	sent      []synclog.TrafficRow
	fail      error
}

func newTrafficSpy(origin string) *trafficSpy {
	return &trafficSpy{MemTransport: synclog.NewMemTransport(origin)}
}

func (s *trafficSpy) PublishTraffic(row synclog.TrafficRow) (map[string]synclog.TrafficRow, error) {
	s.publishes++
	s.sent = append(s.sent, row)
	if s.fail != nil {
		return nil, s.fail
	}
	return s.MemTransport.PublishTraffic(row)
}

// trafficBlind is a client without the optional publisher: embedding the
// interface itself rather than the fake hides the methods a type assertion
// would otherwise reach, which is the client shape a transport predating
// traffic keeps.
type trafficBlind struct {
	synclog.LogTransport
}

// monthNow is the UTC month a publish names its row with.
func monthNow() string { return time.Now().UTC().Format("2006-01") }

// TestTrafficNotDueWithinTheHour pins the throttle's closed side: a machine
// that published a moment ago has already paid the write and the push, and a
// second publish inside the hour says nothing the first did not.
func TestTrafficNotDueWithinTheHour(t *testing.T) {
	t.Parallel()
	_, local := joinPair(t, "m1")
	runner := &Runner{Local: local}
	now := time.Now().UTC()
	if err := local.KVPut(KeyTrafficPublished, mustMarshal(t, now.Add(-time.Minute))); err != nil {
		t.Fatalf("write the traffic marker: %v", err)
	}
	if runner.trafficDue(now) {
		t.Error("a publish inside the hour reads as due, want it throttled")
	}
}

// TestTrafficDueAtTheHourAndWithoutAMarker pins the throttle's open side: one
// is due when the marker is absent, which is a machine that has never
// published, and again once trafficEvery has passed.
func TestTrafficDueAtTheHourAndWithoutAMarker(t *testing.T) {
	t.Parallel()
	_, local := joinPair(t, "m1")
	runner := &Runner{Local: local}
	now := time.Now().UTC()
	if !runner.trafficDue(now) {
		t.Error("a machine with no traffic marker reads as not due, want it due")
	}
	if err := local.KVPut(KeyTrafficPublished, mustMarshal(t, now.Add(-trafficEvery))); err != nil {
		t.Fatalf("write the traffic marker: %v", err)
	}
	if !runner.trafficDue(now) {
		t.Error("a publish exactly at the bound reads as not due, want it due")
	}
}

// TestTrafficDueWithAnUnparseableMarker pins which way a damaged marker is
// read: due rather than never due, the rule marksDue runs on -- the worst a
// bad row costs is a publish the throttle was going to skip.
func TestTrafficDueWithAnUnparseableMarker(t *testing.T) {
	t.Parallel()
	_, local := joinPair(t, "m1")
	runner := &Runner{Local: local}
	if err := local.KVPut(KeyTrafficPublished, []byte(`{"at":"not-a-time"}`)); err != nil {
		t.Fatalf("corrupt the traffic marker: %v", err)
	}
	if !runner.trafficDue(time.Now().UTC()) {
		t.Error("a damaged traffic marker held the publish back, want it due")
	}
}

// TestSteadyPublishesTrafficAndKeepsTheSnapshot pins the success path: the
// attempt's row is handed the log, the answer lands in the local snapshot,
// and only then does the marker move -- so the throttle and the snapshot
// always speak of the same publish.
func TestSteadyPublishesTrafficAndKeepsTheSnapshot(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	log := newTrafficSpy("m1")
	if _, err := log.MemTransport.OnLog("m2").PublishTraffic(
		synclog.TrafficRow{Month: monthNow(), Push: 40, Pull: 60, At: time.Now().UTC()}); err != nil {
		t.Fatalf("seed another origin's row: %v", err)
	}

	runner := &Runner{Client: log, Local: local}
	if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
		t.Fatalf("SyncOnce: %v", res.Err)
	}
	if log.publishes != 1 {
		t.Fatalf("the loop published traffic %d times, want once", log.publishes)
	}
	if got := log.sent[0]; got.Month != monthNow() || got.At.IsZero() {
		t.Errorf("the published row = %+v, want this month read at a real time", got)
	}

	snap, err := ReadAccount(local)
	if err != nil {
		t.Fatalf("read the account snapshot: %v", err)
	}
	if len(snap.Rows) != 2 || snap.Rows["m2"].Push != 40 || snap.ReadAt.IsZero() {
		t.Errorf("the snapshot = %+v, want both origins and a read time", snap)
	}
	if _, ok := markerIsSet(local, KeyTrafficPublished); !ok {
		t.Error("a successful publish left the traffic marker unset")
	}

	// The second attempt inside the window publishes nothing more: the row
	// is one per origin and the throttle bounds how often, not whether.
	if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
		t.Fatalf("the second attempt: %v", res.Err)
	}
	if log.publishes != 1 {
		t.Errorf("two attempts inside the hour published %d times, want one", log.publishes)
	}
}

// TestSteadyTrafficFailureMovesNeitherMarkerNorSnapshot pins the failure
// path: the publish is a report the attempt sends, not a step it can fail
// on, so a refused publish leaves the result as the exchange earned it and
// leaves the throttle where it was for the next attempt to retry.
func TestSteadyTrafficFailureMovesNeitherMarkerNorSnapshot(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	log := newTrafficSpy("m1")
	log.fail = errors.New("the remote would not answer")

	runner := &Runner{Client: log, Local: local}
	res := runner.SyncOnce(context.Background(), shared)
	if res.Err != nil {
		t.Errorf("an attempt whose traffic publish failed = %v, want the exchange still reported as it ran", res.Err)
	}
	if log.publishes != 1 {
		t.Errorf("the loop published traffic %d times, want one attempt", log.publishes)
	}
	if _, ok := markerIsSet(local, KeyTrafficPublished); ok {
		t.Error("a failed publish moved the traffic marker, want it held for the retry")
	}
	if _, ok := markerIsSet(local, KeyAccount); ok {
		t.Errorf("a failed publish wrote the snapshot %v, want the last good account kept", snapBody(t, local))
	}
}

// snapBody is the raw snapshot row, for the failure case's report.
func snapBody(t *testing.T, kv db.KV) string {
	t.Helper()
	body, ok, err := kv.KVGet(KeyAccount)
	if err != nil || !ok {
		return ""
	}
	return string(body)
}

// TestSteadySkipsATransportWithoutTrafficPublisher pins the silent skip: a
// client without the optional publisher is this machine's client rather than
// a fault of this attempt, so the publish is dropped with a debug line and no
// warning, and nothing about the attempt changes.
func TestSteadySkipsATransportWithoutTrafficPublisher(t *testing.T) {
	shared, local := joinPair(t, "m1")
	runner := &Runner{Client: trafficBlind{synclog.NewMemTransport("m1")}, Local: local}

	previous := slog.Default()
	var logged bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
		t.Fatalf("SyncOnce over a transport without the publisher: %v", res.Err)
	}
	if _, ok := markerIsSet(local, KeyTrafficPublished); ok {
		t.Error("a transport without the publisher moved the traffic marker")
	}
	if strings.Contains(logged.String(), "traffic") {
		t.Errorf("the skip was reported as %q, want it silent rather than a warning", logged.String())
	}
}

// TestSteadyPublishesTrafficWhenNoMarkMoved pins the cadence the marks
// throttle does not cover: the account total goes most wrong on a machine
// that exports heavily while importing nothing, so a traffic publish runs on
// every steady attempt whether or not the import moved a mark.
func TestSteadyPublishesTrafficWhenNoMarkMoved(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	log := newTrafficSpy("m1")

	runner := &Runner{Client: log, Local: local}
	if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
		t.Fatalf("SyncOnce: %v", res.Err)
	}
	if log.publishes != 1 {
		t.Errorf("an attempt that moved no mark published traffic %d times, want it published anyway",
			log.publishes)
	}
	if _, ok := markerIsSet(local, KeyMarksPublished); ok {
		t.Error("an attempt that moved no mark published marks too, want only the traffic published")
	}
}

// TestAccountTrafficReadsTheAccount pins the fold: this machine's share comes
// from its live counters rather than its own row, another origin's row
// counts only when it names this month, and the machine count speaks of the
// rows the total actually carries.
func TestAccountTrafficReadsTheAccount(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	readAt := now.Add(-time.Hour)
	snap := AccountSnapshot{ReadAt: readAt, Rows: map[string]synclog.TrafficRow{
		"self":  {Month: "2026-10", Push: 900, Pull: 900, At: readAt},
		"other": {Month: "2026-10", Push: 40, Pull: 60, At: readAt},
		"old":   {Month: "2026-09", Push: 1000, Pull: 1000, At: readAt},
	}}
	own := ByteCounters{TursoPush: 7, TursoPull: 3}

	a := AccountTraffic(snap, own, "self", now)
	if a.Total != 10+100 {
		t.Errorf("total = %d, want the live 10 plus this month's other row, not the own row or the old one: %+v", a.Total, a)
	}
	if a.Machines != 2 {
		t.Errorf("machines = %d, want this machine and the one counted row", a.Machines)
	}
	if a.Month != "2026-10" || a.ReadAt != readAt {
		t.Errorf("month %q read at %v, want 2026-10 at the snapshot's own time", a.Month, a.ReadAt)
	}

	// An empty or absent snapshot gives this machine alone.
	alone := AccountTraffic(AccountSnapshot{}, own, "self", now)
	if alone.Total != 10 || alone.Machines != 1 {
		t.Errorf("with no snapshot the account = %+v, want this machine alone", alone)
	}

	// Three origins of this month sum across all of them.
	three := AccountSnapshot{ReadAt: readAt, Rows: map[string]synclog.TrafficRow{
		"self":  {Month: "2026-10", Push: 900, Pull: 900, At: readAt},
		"other": {Month: "2026-10", Push: 40, Pull: 60, At: readAt},
		"third": {Month: "2026-10", Push: 1, Pull: 2, At: readAt},
	}}
	all := AccountTraffic(three, own, "self", now)
	if all.Total != 10+100+3 || all.Machines != 3 {
		t.Errorf("three origins of this month = %+v, want every row counted and this machine once", all)
	}
}

// TestReadAccountIsZeroWhenAbsent pins that a machine that has never
// published reads the zero snapshot rather than an error, the way ReadBytes
// reads an absent month.
func TestReadAccountIsZeroWhenAbsent(t *testing.T) {
	t.Parallel()
	_, local := joinPair(t, "m1")
	snap, err := ReadAccount(local)
	if err != nil {
		t.Fatalf("read an absent snapshot: %v", err)
	}
	if !snap.ReadAt.IsZero() || len(snap.Rows) != 0 {
		t.Errorf("the absent snapshot = %+v, want the zero value", snap)
	}
}
