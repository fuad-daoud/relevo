//go:build unix

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// seedAttempt writes the daemon's last-attempt marker beside a tick marker
// that still says the last tick was fine, which is the machine the old status
// read as healthy.
func seedAttempt(t *testing.T, a relevosync.Attempt) {
	t.Helper()
	body, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("encode the attempt: %v", err)
	}
	if err := dialledLocal(t).KVPut(relevosync.KeyLastAttempt, body); err != nil {
		t.Fatalf("write the attempt marker: %v", err)
	}
}

func staleHealthyMachine() dbSyncStatusCase {
	return dbSyncStatusCase{
		name:      "stale times",
		enabled:   true,
		tickOK:    true,
		remote:    syncStatusRemote,
		namespace: syncStatusNamespace,
		token:     syncStatusSecret,
	}
}

func TestStatusShowsAFailedAttemptOnBothShapes(t *testing.T) {
	serveSyncStatus(t, staleHealthyMachine())
	end := time.Date(2026, 10, 4, 12, 5, 0, 0, time.UTC)
	seedAttempt(t, relevosync.Attempt{
		Start: end.Add(-time.Second), End: end, Exported: 3, Applied: 1,
		Error: "remote down\x1b[31m", Attention: true,
	})

	text, _, err := captureOutput(t, func() error { return cmdDBSyncStatus(nil) })
	if err != nil {
		t.Fatalf("cmdDBSyncStatus: %v", err)
	}
	for _, want := range []string{"last attempt failed at 2026-10-04T12:05:00Z", "remote down", "exported 3, applied 1", "needs attention"} {
		if !strings.Contains(string(text), want) {
			t.Errorf("status line %q is missing %q", string(text), want)
		}
	}
	if strings.Contains(string(text), "\x1b") {
		t.Errorf("status line %q carries a control sequence", string(text))
	}

	jsonOut, _, err := captureOutput(t, func() error { return cmdDBSyncStatus([]string{"--json"}) })
	if err != nil {
		t.Fatalf("cmdDBSyncStatus --json: %v", err)
	}
	var doc dbSyncStatusDoc
	if err := json.Unmarshal(jsonOut, &doc); err != nil {
		t.Fatalf("decode the document: %v", err)
	}
	if doc.LastAttempt == nil || doc.LastAttempt.Error == "" || !doc.LastAttempt.Attention ||
		doc.LastAttempt.Exported != 3 || doc.LastAttempt.Applied != 1 {
		t.Errorf("document last_attempt = %+v, want the failed attempt", doc.LastAttempt)
	}

	state, err := relevosync.ReadState(dialledLocal(t))
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if got := relevosync.Token(state); got != relevosync.TokenBehind {
		t.Errorf("token for a machine whose last attempt failed = %q, want %q", got, relevosync.TokenBehind)
	}
}

func TestStatusShowsACleanAttemptAndOmitsNone(t *testing.T) {
	serveSyncStatus(t, staleHealthyMachine())

	text, _, err := captureOutput(t, func() error { return cmdDBSyncStatus(nil) })
	if err != nil {
		t.Fatalf("cmdDBSyncStatus: %v", err)
	}
	if strings.Contains(string(text), "last attempt") {
		t.Errorf("a machine with no recorded attempt rendered one: %q", string(text))
	}

	end := time.Date(2026, 10, 4, 12, 5, 0, 0, time.UTC)
	seedAttempt(t, relevosync.Attempt{Start: end.Add(-time.Second), End: end, Exported: 2})
	text, _, err = captureOutput(t, func() error { return cmdDBSyncStatus(nil) })
	if err != nil {
		t.Fatalf("cmdDBSyncStatus: %v", err)
	}
	if !strings.Contains(string(text), "last attempt ok at 2026-10-04T12:05:00Z (exported 2, applied 0, pulls 0, pushes 0)") {
		t.Errorf("status line %q does not show the clean attempt", string(text))
	}
}

// seedAttemptRing writes the recent-attempt ring and the month's table
// breakdown the way steady attempts leave them.
func seedAttemptRing(t *testing.T) {
	t.Helper()
	local := dialledLocal(t)
	end := time.Date(2026, 10, 4, 12, 5, 0, 0, time.UTC)
	ring := []relevosync.Attempt{
		{Start: end.Add(-2 * time.Second), End: end.Add(-time.Second), Exported: 2, Pulls: 1, Pushes: 1, TursoPush: 2000, TursoPull: 60000},
		{Start: end.Add(-time.Second), End: end, Applied: 5, Pulls: 1, TursoPull: 120000},
	}
	body, err := json.Marshal(ring)
	if err != nil {
		t.Fatalf("encode the attempt ring: %v", err)
	}
	if err := local.KVPut(relevosync.KeyAttempts, body); err != nil {
		t.Fatalf("write the attempt ring: %v", err)
	}
	tables, err := json.Marshal(synclog.TableTallies{
		"binding": {ExportedEntries: 2, ExportedBytes: 2000, ImportedEntries: 5, ImportedBytes: 180000},
	})
	if err != nil {
		t.Fatalf("encode the table breakdown: %v", err)
	}
	if err := local.KVPut(relevosync.TablesKey(time.Now()), tables); err != nil {
		t.Fatalf("write the table breakdown: %v", err)
	}
}

// The ring and the breakdown reach both shapes: the document carries every
// attempt and table, and the line summarizes the rate and the heaviest table.
func TestStatusShowsAttemptRingAndTables(t *testing.T) {
	serveSyncStatus(t, staleHealthyMachine())
	seedAttemptRing(t)

	text, _, err := captureOutput(t, func() error { return cmdDBSyncStatus(nil) })
	if err != nil {
		t.Fatalf("cmdDBSyncStatus: %v", err)
	}
	for _, want := range []string{
		"2 pulls, 1 pushes, pull 180.0 kB, push 2.0 kB in the last 2 attempts",
		"tables: binding 7 rows 182.0 kB",
	} {
		if !strings.Contains(string(text), want) {
			t.Errorf("status line %q is missing %q", string(text), want)
		}
	}

	jsonOut, _, err := captureOutput(t, func() error { return cmdDBSyncStatus([]string{"--json"}) })
	if err != nil {
		t.Fatalf("cmdDBSyncStatus --json: %v", err)
	}
	var doc dbSyncStatusDoc
	if err := json.Unmarshal(jsonOut, &doc); err != nil {
		t.Fatalf("decode the document: %v", err)
	}
	if len(doc.Attempts) != 2 || doc.Attempts[0].Pulls != 1 || doc.Attempts[1].Applied != 5 {
		t.Errorf("document attempts = %+v, want the seeded ring", doc.Attempts)
	}
	if tally := doc.Tables["binding"]; tally.ExportedEntries != 2 || tally.ImportedBytes != 180000 {
		t.Errorf("document tables = %+v, want the seeded breakdown", doc.Tables)
	}
}
