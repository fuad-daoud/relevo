package syncworker

// The traffic verb over a real backend: what a publish writes, what it answers
// with, and which rows it refuses to read back. They run against a local
// replica file the way the marks tests do, so CI reaches no network while the
// SQL underneath is the same shape a sync connection takes.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// trafficAt is when a publish says it read its counters, pinned so a row read
// back is about the write rather than about a clock.
var trafficAt = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// trafficRow is this machine's month as a request carries it.
var trafficRow = TrafficRow{Month: "2026-10", Push: 111, Pull: 222, At: trafficAt}

// trafficBody renders the JSON shape a traffic meta row stores, for the rows a
// case seeds that no verb in this slice writes.
func trafficBody(month string, push, pull int64, at string) string {
	body, err := json.Marshal(map[string]any{"month": month, "push": push, "pull": pull, "at": at})
	if err != nil {
		panic(err)
	}
	return string(body)
}

// seedTraffic writes one traffic meta row directly, which is how a case puts
// another machine's row into the replica.
func seedTraffic(t *testing.T, path, key, value string) {
	t.Helper()
	stmt := "INSERT INTO meta (key, value) VALUES ('" + key + "', '" + value + "')"
	if err := execOnErr(t, path, stmt); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}

// trafficKeys reads the traffic rows the replica holds, keyed by origin, once
// the worker is gone: a second handle reading beside a worker still writing
// is a race.
func trafficKeys(t *testing.T, path string) map[string]TrafficRow {
	t.Helper()
	rows, err := openAt(t, path).Query("SELECT key, value FROM meta WHERE key LIKE ?", trafficPrefix+"%")
	if err != nil {
		t.Fatalf("read the traffic rows: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]TrafficRow)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatalf("scan a traffic row: %v", err)
		}
		var row TrafficRow
		if err := json.Unmarshal([]byte(value), &row); err != nil {
			t.Fatalf("decode the traffic row %s: %v", key, err)
		}
		out[strings.TrimPrefix(key, trafficPrefix)] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the traffic rows: %v", err)
	}
	return out
}

// TestPublishTrafficWritesOnlyOwnRow pins the rule the verb exists to hold: a
// publish writes traffic.<this origin> and no other key, and pushes what it
// wrote. The origin comes from the handshake rather than the request, so a
// caller on this machine cannot move another machine's row.
func TestPublishTrafficWritesOnlyOwnRow(t *testing.T) {
	path := tempReplica(t)
	driver := &localDriver{path: path}
	b := backendFor(t, driver, "origin-a")
	s := startWorker(t, b)
	if resp := s.ask(helloRequest("h")); !resp.OK {
		t.Fatalf("hello: %+v", resp)
	}

	before := driver.pushes
	resp := s.ask(Request{ID: "t1", Verb: VerbPublishTraffic, Traffic: &trafficRow})
	if !resp.OK {
		t.Fatalf("publish_traffic = %+v, want it carried out", resp)
	}
	if driver.pushes != before+1 {
		t.Errorf("a publish pushed %d times, want once: the remote learns the row from a push",
			driver.pushes-before)
	}

	if resp := s.ask(Request{ID: "bye", Verb: VerbShutdown}); !resp.OK {
		t.Fatalf("shutdown: %+v", resp)
	}
	if err := s.wait(); err != nil {
		t.Fatalf("the worker ended: %v", err)
	}
	rows := trafficKeys(t, path)
	if len(rows) != 1 {
		t.Fatalf("the replica holds %d traffic rows, want exactly one: %+v", len(rows), rows)
	}
	if got := rows["origin-a"]; got != trafficRow {
		t.Errorf("traffic.origin-a = %+v, want the row the publish carried", got)
	}
	if _, ok := rows["origin-b"]; ok {
		t.Error("a publish wrote traffic.origin-b, want only the handshaken origin's row")
	}
}

// TestPublishTrafficOverwritesItsOwnRow pins the UPDATE path: one row per
// origin, and a publish in a new month simply replaces what the last one
// wrote rather than adding a row or failing on the existing one.
func TestPublishTrafficOverwritesItsOwnRow(t *testing.T) {
	path := tempReplica(t)
	driver := &localDriver{path: path}
	b := backendFor(t, driver, "origin-a")
	s := startWorker(t, b)
	if resp := s.ask(helloRequest("h")); !resp.OK {
		t.Fatalf("hello: %+v", resp)
	}
	if resp := s.ask(Request{ID: "t1", Verb: VerbPublishTraffic, Traffic: &trafficRow}); !resp.OK {
		t.Fatalf("publish_traffic: %+v", resp)
	}

	second := TrafficRow{Month: "2026-11", Push: 33, Pull: 44, At: trafficAt}
	if resp := s.ask(Request{ID: "t2", Verb: VerbPublishTraffic, Traffic: &second}); !resp.OK {
		t.Fatalf("the second publish_traffic = %+v, want it carried out", resp)
	}
	if resp := s.ask(Request{ID: "bye", Verb: VerbShutdown}); !resp.OK {
		t.Fatalf("shutdown: %+v", resp)
	}
	if err := s.wait(); err != nil {
		t.Fatalf("the worker ended: %v", err)
	}
	rows := trafficKeys(t, path)
	if len(rows) != 1 {
		t.Fatalf("the replica holds %d traffic rows after two publishes, want one overwritten: %+v",
			len(rows), rows)
	}
	if got := rows["origin-a"]; got != second {
		t.Errorf("traffic.origin-a = %+v, want the second publish's row", got)
	}
}

// TestPublishTrafficAnswersWithEveryOriginRow pins the account answer: the
// publish replies with every origin's row as this replica holds it, including
// a row another machine wrote that this worker never saw in a request.
func TestPublishTrafficAnswersWithEveryOriginRow(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	seedTraffic(t, path, trafficPrefix+"origin-z",
		trafficBody("2026-10", 5, 6, "2026-10-05T00:00:00Z"))

	s := startWorker(t, b)
	if resp := s.ask(helloRequest("h")); !resp.OK {
		t.Fatalf("hello: %+v", resp)
	}
	resp := s.ask(Request{ID: "t1", Verb: VerbPublishTraffic, Traffic: &trafficRow})
	if !resp.OK {
		t.Fatalf("publish_traffic = %+v, want it carried out", resp)
	}
	held := resp.Traffic
	if len(held) != 2 {
		t.Fatalf("the publish answered with %d rows, want both origins: %+v", len(held), held)
	}
	if z := held["origin-z"]; z.Push != 5 || z.Pull != 6 || z.Month != "2026-10" {
		t.Errorf("the answer's origin-z row = %+v, want the row seeded into meta", z)
	}
	if own := held["origin-a"]; own != trafficRow {
		t.Errorf("the answer's own row = %+v, want the row just published", own)
	}
}

// TestPublishTrafficSkipsMalformedRows pins that a row this build cannot read
// is skipped and the rest are still answered: the rows are written by other
// machines, and one bad row must not stop every reader from learning the
// total -- the rule readMarkRows runs on for marks.
func TestPublishTrafficSkipsMalformedRows(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	seedTraffic(t, path, trafficPrefix+"bad-json", `not json at all`)
	seedTraffic(t, path, trafficPrefix+"bad-month",
		trafficBody("October", 1, 1, "2026-10-05T00:00:00Z"))
	seedTraffic(t, path, trafficPrefix+"neg-push",
		trafficBody("2026-10", -3, 1, "2026-10-05T00:00:00Z"))
	seedTraffic(t, path, trafficPrefix+"no-time",
		trafficBody("2026-10", 1, 1, ""))
	seedTraffic(t, path, trafficPrefix+"origin-y",
		trafficBody("2026-10", 7, 8, "2026-10-05T00:00:00Z"))

	s := startWorker(t, b)
	if resp := s.ask(helloRequest("h")); !resp.OK {
		t.Fatalf("hello: %+v", resp)
	}
	resp := s.ask(Request{ID: "t1", Verb: VerbPublishTraffic, Traffic: &trafficRow})
	if !resp.OK {
		t.Fatalf("publish_traffic = %+v, want it carried out", resp)
	}
	held := resp.Traffic
	if len(held) != 2 {
		t.Fatalf("the publish answered with %d rows, want the two well-formed ones: %+v", len(held), held)
	}
	for _, bad := range []string{"bad-json", "bad-month", "neg-push", "no-time"} {
		if _, ok := held[bad]; ok {
			t.Errorf("the answer carries the malformed row %q, want it skipped", bad)
		}
	}
	if y := held["origin-y"]; y.Push != 7 || y.Pull != 8 {
		t.Errorf("the answer's origin-y row = %+v, want the row seeded into meta", y)
	}
}

// TestPublishTrafficRefusesWhatItCannotPlace pins the refusals that keep a
// meaningless row off the remote: a request with no row names nothing to
// write, a month that is not "2006-01" belongs to no bill, and a negative
// count is not a measurement.
func TestPublishTrafficRefusesWhatItCannotPlace(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	s := startWorker(t, b)
	if resp := s.ask(helloRequest("h")); !resp.OK {
		t.Fatalf("hello: %+v", resp)
	}

	cases := map[string]Request{
		"no row at all":   {ID: "a", Verb: VerbPublishTraffic},
		"a bad month":     {ID: "b", Verb: VerbPublishTraffic, Traffic: &TrafficRow{Month: "2026-13", Push: 1, Pull: 1, At: trafficAt}},
		"a negative push": {ID: "c", Verb: VerbPublishTraffic, Traffic: &TrafficRow{Month: "2026-10", Push: -1, Pull: 1, At: trafficAt}},
		"a negative pull": {ID: "d", Verb: VerbPublishTraffic, Traffic: &TrafficRow{Month: "2026-10", Push: 1, Pull: -1, At: trafficAt}},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			resp := s.ask(req)
			if resp.OK {
				t.Fatalf("%s was accepted, want a refusal", name)
			}
			if !contains(resp.Error, "not the sync pipe protocol") {
				t.Errorf("refusal %q does not name the protocol", resp.Error)
			}
		})
	}
	if resp := s.ask(Request{ID: "bye", Verb: VerbShutdown}); !resp.OK {
		t.Fatalf("shutdown: %+v", resp)
	}
	if err := s.wait(); err != nil {
		t.Fatalf("the worker ended: %v", err)
	}
	if rows := trafficKeys(t, path); len(rows) != 0 {
		t.Errorf("refused publishes wrote %v, want no traffic row written", rows)
	}
}

// TestPublishTrafficRefusedWithoutHello pins that the verb needs the
// handshake: before it the worker holds no origin, and the row would be
// keyed by a machine nobody shook hands as.
func TestPublishTrafficRefusedWithoutHello(t *testing.T) {
	b := NewTursoBackend(&localDriver{path: tempReplica(t)})
	s := startWorker(t, b)
	resp := s.ask(Request{ID: "a", Verb: VerbPublishTraffic, Traffic: &trafficRow})
	if resp.OK {
		t.Errorf("publish_traffic before hello = %+v, want a refusal", resp)
	}
	if resp.Error != ErrNoHello.Error() {
		t.Errorf("publish_traffic before hello = %q, want %q", resp.Error, ErrNoHello)
	}
}

// TestPublishTrafficReportsNoReplica pins the second gate the marks verb
// reports: a backend that never opened a replica refuses rather than writing
// a row nowhere and answering as if the account had learned it.
func TestPublishTrafficReportsNoReplica(t *testing.T) {
	b := NewTursoBackend(&localDriver{path: tempReplica(t)})
	if _, err := b.PublishTraffic(trafficRow); !errorsIsNoReplica(err) {
		t.Errorf("PublishTraffic without a replica = %v, want a no-replica refusal", err)
	}
}
