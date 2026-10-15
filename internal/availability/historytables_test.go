package availability

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The row half of the histories: what each append point writes beside its
// document, and what the retention pass removes. Every case here writes through
// a real append point rather than through the helper, so what is pinned is the
// wiring the production paths take.

// withHistory binds a shared file to d's Deps, so a case can read the rows back
// by hand the way a peer would find them.
func withHistory(t *testing.T, d Deps) (Deps, *db.DB) {
	t.Helper()
	shared, err := db.OpenWith(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: "m1"})
	if err != nil {
		t.Fatalf("open the shared file: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	d.History = shared
	return d, shared
}

// itoa is a millisecond timestamp as the assertions below want it written.
func itoa(ms int64) string { return strconv.FormatInt(ms, 10) }

// eventRows reads every availability_event row as "kind provider origin at_ms",
// so a case says what was written rather than only how many rows.
func eventRows(t *testing.T, d *db.DB) []string {
	t.Helper()
	raw, err := db.OpenRawReadOnly(d.Path())
	if err != nil {
		t.Fatalf("open the file for reading: %v", err)
	}
	defer func() { _ = raw.Close() }()

	rows, err := raw.Query(`SELECT kind, provider, origin, at_ms FROM availability_event ORDER BY at_ms, id`)
	if err != nil {
		t.Fatalf("read availability_event: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var kind, provider, origin string
		var atMS int64
		if err := rows.Scan(&kind, &provider, &origin, &atMS); err != nil {
			t.Fatalf("read availability_event: %v", err)
		}
		out = append(out, kind+" "+provider+" "+origin+" "+itoa(atMS))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read availability_event: %v", err)
	}
	return out
}

// sampleRows reads every latency_sample row as "token origin at_ms".
func sampleRows(t *testing.T, d *db.DB) []string {
	t.Helper()
	raw, err := db.OpenRawReadOnly(d.Path())
	if err != nil {
		t.Fatalf("open the file for reading: %v", err)
	}
	defer func() { _ = raw.Close() }()

	rows, err := raw.Query(`SELECT token, origin, at_ms FROM latency_sample ORDER BY at_ms, id`)
	if err != nil {
		t.Fatalf("read latency_sample: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var token, origin string
		var atMS int64
		if err := rows.Scan(&token, &origin, &atMS); err != nil {
			t.Fatalf("read latency_sample: %v", err)
		}
		out = append(out, token+" "+origin+" "+itoa(atMS))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read latency_sample: %v", err)
	}
	return out
}

// A gate writes one row of its own, under its own installation, with the
// observation's fields, and the reader sees it there rather than in a document.
func TestAppendEntryLockedWritesOneEventRow(t *testing.T) {
	t.Parallel()
	d, shared := withHistory(t, testDeps(t))

	if _, err := Unavailable(d, testClaudeRef, time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	want := "rate_limited test m1 " + itoa(db.UnixMS(baseTime))
	got := eventRows(t, shared)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("availability_event rows = %v, want [%q]", got, want)
	}
	if h := readHistory(t, d); len(h.Events) != 1 {
		t.Fatalf("got %d history events, want the row the gate wrote", len(h.Events))
	}
}

// A clear that removed something records one row of its own, carrying the kind
// only a clear has and the oldest removal it removed.
func TestAvailableClearWritesOneEventRow(t *testing.T) {
	t.Parallel()
	d, shared := withHistory(t, testDeps(t))

	if _, err := Unavailable(d, testClaudeRef, time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	if _, removed, err := Available(d, "test", ClearedByMasterMind); err != nil || removed == 0 {
		t.Fatalf("Available = (%d, %v), want the gate cleared", removed, err)
	}

	got := eventRows(t, shared)
	// One row for the rate limit and one for the clear, both this machine's.
	if len(got) != 2 {
		t.Fatalf("availability_event rows = %v, want the gate and the clear", got)
	}
	if got[1] != "cleared test m1 "+itoa(db.UnixMS(baseTime)) {
		t.Fatalf("the clear's row = %q, want a cleared event under this machine's origin", got[1])
	}
}

// A probe writes one row of its own, with the figure the sample carries.
func TestProbeRecordsOneSampleRow(t *testing.T) {
	t.Parallel()
	d, shared := withHistory(t, testDeps(t))

	recordLatency(d, ProbeResult{
		Sample: Sample{At: baseTime, Token: testClaudeRef, Host: "laptop", TTFTMS: 120, TotalMS: 900},
		Name:   "claude",
	})

	want := testClaudeRef + " m1 " + itoa(db.UnixMS(baseTime))
	got := sampleRows(t, shared)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("latency_sample rows = %v, want [%q]", got, want)
	}
	if h, err := LoadLatency(d); err != nil || len(h.Samples) != 1 {
		t.Fatalf("latency history = %+v (%v), want the row the probe wrote", h, err)
	}
}

// The retention pass removes the rows outside the window and leaves the rest,
// for every origin rather than this machine's own.
func TestPruneHistoryDeletesAgedRows(t *testing.T) {
	t.Parallel()
	d, shared := withHistory(t, testDeps(t))

	aged := db.UnixMS(baseTime.Add(-31 * 24 * time.Hour))
	live := db.UnixMS(baseTime.Add(-time.Hour))
	rows := []struct {
		origin string
		at     int64
	}{{"m2", aged}, {"m1", live}}
	for _, r := range rows {
		event := db.AvailabilityEvent{
			ID: db.NewID(), AtMS: r.at, Kind: string(RateLimited),
			Provider: "test", Source: "relevo",
		}
		if err := shared.InsertAvailabilityEvent(r.origin, event); err != nil {
			t.Fatalf("seed availability_event: %v", err)
		}
		sample := db.LatencySample{
			ID: db.NewID(), AtMS: r.at, Token: testClaudeRef, TTFTMS: 1, TotalMS: 2,
		}
		if err := shared.InsertLatencySample(r.origin, sample); err != nil {
			t.Fatalf("seed latency_sample: %v", err)
		}
	}

	pruned, err := PruneHistory(d)
	if err != nil {
		t.Fatalf("PruneHistory: %v", err)
	}
	if pruned.Events != 1 || pruned.Samples != 1 {
		t.Fatalf("pruned = %+v, want one row of each table and no more", pruned)
	}
	// The peer-origin rows went too: a machine holds what a peer sent it and
	// shows a window, so its own origin is not the only one it ages out.
	if got := eventRows(t, shared); len(got) != 1 {
		t.Fatalf("availability_event rows after the prune = %v, want only the live row", got)
	}
	if got := sampleRows(t, shared); len(got) != 1 {
		t.Fatalf("latency_sample rows after the prune = %v, want only the live row", got)
	}
}

// A Deps with no shared file records nothing and prunes nothing: a machine with
// no database keeps no history at all, rather than a second copy somewhere the
// documents used to be.
func TestHistoryRowsAreOptional(t *testing.T) {
	t.Parallel()
	d := testDeps(t)
	d.History = nil

	if _, err := Unavailable(d, testClaudeRef, time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	recordLatency(d, ProbeResult{Sample: Sample{At: baseTime, Token: testClaudeRef}, Name: "claude"})
	if h := readHistory(t, d); len(h.Events) != 0 {
		t.Fatalf("got %d history events, want none from a machine with no shared file", len(h.Events))
	}
	if h, err := LoadLatency(d); err != nil || len(h.Samples) != 0 {
		t.Fatalf("latency history = %+v (%v), want none from a machine with no shared file", h, err)
	}
	pruned, err := PruneHistory(d)
	if err != nil || pruned != (db.HistoryPruned{}) {
		t.Fatalf("PruneHistory with no shared file = (%+v, %v), want nothing and no error", pruned, err)
	}
}
