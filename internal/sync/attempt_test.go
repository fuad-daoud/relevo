package sync

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// One attempt reports the transfers it ran: the pulls and pushes the worker's
// totals moved between the attempt's start and end, beside the bytes. The last
// attempt and the ring carry the same record, so the line and the document
// agree on what just happened.
func TestSyncOnceRecordsTransfersAndRing(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	client := &readings{MemTransport: synclog.NewMemTransport("m1"), totals: []synclog.Stats{
		{TursoSent: 1000, TursoReceived: 200, TursoPushes: 2, TursoPulls: 1},
		{TursoSent: 1200, TursoReceived: 500, TursoPushes: 2, TursoPulls: 2},
	}}
	runner := &Runner{Client: client, Local: local}
	for range 2 {
		if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
			t.Fatalf("SyncOnce: %v", res.Err)
		}
	}

	var last Attempt
	if err := marker(local, KeyLastAttempt, &last); err != nil {
		t.Fatalf("read the last attempt: %v", err)
	}
	want := Transfers{Pulls: 1, Pushes: 0, TursoPush: 200, TursoPull: 300}
	got := Transfers{Pulls: last.Pulls, Pushes: last.Pushes, TursoPush: last.TursoPush, TursoPull: last.TursoPull}
	if got != want {
		t.Errorf("last attempt transfers = %+v, want %+v", got, want)
	}

	ring, err := ReadAttempts(local)
	if err != nil {
		t.Fatalf("ReadAttempts: %v", err)
	}
	if len(ring) != 2 {
		t.Fatalf("ring holds %d attempts, want 2", len(ring))
	}
	first := Transfers{Pulls: ring[0].Pulls, Pushes: ring[0].Pushes, TursoPush: ring[0].TursoPush, TursoPull: ring[0].TursoPull}
	if want := (Transfers{Pulls: 1, Pushes: 2, TursoPush: 1000, TursoPull: 200}); first != want {
		t.Errorf("first attempt transfers = %+v, want %+v", first, want)
	}
	if ring[1].Start.Before(ring[0].Start) {
		t.Error("the ring is not oldest first")
	}
}

// The ring keeps the newest records: a machine that ticks for days reports
// the recent rate, not the first attempts it ever ran.
func TestAttemptRingCapsAtTwenty(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	var totals []synclog.Stats
	for n := range maxAttemptRecords + 2 {
		totals = append(totals, synclog.Stats{
			TursoSent:     int64(1000 * (n + 1)),
			TursoReceived: int64(1000 * (n + 1)),
			TursoPulls:    n + 1,
		})
	}
	client := &readings{MemTransport: synclog.NewMemTransport("m1"), totals: totals}
	runner := &Runner{Client: client, Local: local}
	for range maxAttemptRecords + 2 {
		if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
			t.Fatalf("SyncOnce: %v", res.Err)
		}
	}
	ring, err := ReadAttempts(local)
	if err != nil {
		t.Fatalf("ReadAttempts: %v", err)
	}
	if len(ring) != maxAttemptRecords {
		t.Fatalf("ring holds %d attempts, want the newest %d", len(ring), maxAttemptRecords)
	}
	// The first two aged out: the oldest kept moved the third reading's delta.
	if ring[0].TursoPull != 1000 || ring[0].Pulls != 1 {
		t.Errorf("oldest kept = pulls %d pull %d, want the third attempt's 1/1000", ring[0].Pulls, ring[0].TursoPull)
	}
}

// An attempt's breakdown lands in the month's row, merged across attempts, so
// one status read names the tables the whole month moved.
func TestCountTablesFoldsMonthBreakdown(t *testing.T) {
	t.Parallel()
	_, local := joinPair(t, "m1")
	runner := &Runner{Local: local}
	runner.countTables(synclog.TableTallies{
		"binding": {ExportedEntries: 2, ExportedBytes: 100},
	})
	runner.countTables(synclog.TableTallies{
		"binding": {ImportedEntries: 1, ImportedBytes: 50},
		"round":   {ExportedEntries: 1, ExportedBytes: 25},
	})
	runner.countTables(nil)
	got, err := ReadTables(local, time.Now())
	if err != nil {
		t.Fatalf("ReadTables: %v", err)
	}
	if want := (synclog.TableTally{ExportedEntries: 2, ExportedBytes: 100, ImportedEntries: 1, ImportedBytes: 50}); got["binding"] != want {
		t.Errorf("binding = %+v, want %+v", got["binding"], want)
	}
	if want := (synclog.TableTally{ExportedEntries: 1, ExportedBytes: 25}); got["round"] != want {
		t.Errorf("round = %+v, want %+v", got["round"], want)
	}
	if len(got) != 2 {
		t.Errorf("tables = %v, want two", got)
	}
}

// The attempts clause reads as the rate it is: counts beside bytes, over the
// attempts averaged.
func TestFormatAttempts(t *testing.T) {
	t.Parallel()
	got := FormatAttempts([]Attempt{
		{Pulls: 19, Pushes: 2, TursoPush: 200_000, TursoPull: 60_000_000},
		{Pulls: 1, Pushes: 1, TursoPush: 1_000_000, TursoPull: 0},
	})
	want := "20 pulls, 3 pushes, pull 60.0 MB, push 1.2 MB in the last 2 attempts"
	if got != want {
		t.Errorf("FormatAttempts = %q, want %q", got, want)
	}
	if got := FormatAttempts([]Attempt{{Pulls: 1}}); !strings.HasSuffix(got, "in the last 1 attempt") {
		t.Errorf("singular FormatAttempts = %q", got)
	}
}

// The tables clause names the heaviest tables first and says how many more
// the document carries, so the line stays short on a busy month.
func TestFormatTables(t *testing.T) {
	t.Parallel()
	got := FormatTables(synclog.TableTallies{
		"binding": {ExportedEntries: 100, ExportedBytes: 900_000, ImportedEntries: 20, ImportedBytes: 300_000},
		"round":   {ExportedEntries: 45, ExportedBytes: 300_000},
		"doc":     {ImportedEntries: 5, ImportedBytes: 100},
		"config":  {ExportedEntries: 1, ExportedBytes: 50},
	})
	want := "tables: binding 120 rows 1.2 MB, round 45 rows 300.0 kB, doc 5 rows 100 B, +1 more"
	if got != want {
		t.Errorf("FormatTables = %q, want %q", got, want)
	}
}

// A drain loop's merged report keeps every page's breakdown, as it keeps every
// page's counts: reporting only the last page's tables would hide the rest.
func TestMergeImportCarriesTables(t *testing.T) {
	t.Parallel()
	var total synclog.ImportResult
	mergeImport(&total, synclog.ImportResult{Applied: 2, Tables: synclog.TableTallies{
		"binding": {ImportedEntries: 2, ImportedBytes: 200},
	}})
	mergeImport(&total, synclog.ImportResult{Applied: 1, Tables: synclog.TableTallies{
		"binding": {ImportedEntries: 1, ImportedBytes: 100},
		"round":   {ImportedEntries: 1, ImportedBytes: 50},
	}})
	if total.Applied != 3 {
		t.Errorf("applied = %d, want 3", total.Applied)
	}
	if got := total.Tables["binding"]; got.ImportedEntries != 3 || got.ImportedBytes != 300 {
		t.Errorf("binding = %+v, want 3 entries/300 bytes across both pages", got)
	}
	if got := total.Tables["round"]; got.ImportedEntries != 1 {
		t.Errorf("round = %+v, want the second page's entry", got)
	}
}
