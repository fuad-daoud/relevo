package main

// The meter surface is pinned as pure functions plus one fill from a local
// kv the test builds: no subcommand here spawns a harness or reaches a
// network.

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// TestSyncMeterNoticeLines pins each of the four notices and the silence that
// replaces them: the pace that has passed the quota wins over the share still
// climbing, which wins over the pace worth mentioning, and idle exports come
// last. A pace-over reading here is also over the warn share, so the first
// case fails the moment the order is not first-wins.
func TestSyncMeterNoticeLines(t *testing.T) {
	t.Parallel()
	paceOver := relevosync.QuotaReading{
		Account:     relevosync.Account{Total: 22_200_000_000, Machines: 3},
		Quota:       10_000_000_000,
		Projected:   30_100_000_000,
		Projectable: true,
	}
	overWarn := paceOver
	overWarn.Account.Total = 8_500_000_000
	overWarn.Projected, overWarn.Projectable = 0, false
	paceNotice := relevosync.QuotaReading{
		Account:     relevosync.Account{Total: 2_000_000_000, Machines: 2},
		Quota:       10_000_000_000,
		Projected:   6_000_000_000,
		Projectable: true,
	}
	idleOver := relevosync.ExportHour{Hour: "2026-10-27T13", Exported: 3700, Idle: 3700}
	calm := relevosync.QuotaReading{Account: relevosync.Account{Total: 1_000_000_000}, Quota: 10_000_000_000}
	for _, tc := range []struct {
		name string
		q    relevosync.QuotaReading
		qok  bool
		idle relevosync.ExportHour
		iok  bool
		want string
	}{
		{"pace over beats over warn", paceOver, true, relevosync.ExportHour{}, false,
			"sync: the account's Turso traffic is on pace for 30.1 GB this month, over its 10.0 GB quota -- relevo db sync status"},
		{"over warn", overWarn, true, relevosync.ExportHour{}, false,
			"sync: the account has used 85.0% of this month's 10.0 GB Turso quota -- relevo db sync status"},
		{"pace notice", paceNotice, true, relevosync.ExportHour{}, false,
			"sync: the account's Turso traffic is on pace for 6.0 GB this month, 60.0% of its 10.0 GB quota -- relevo db sync status"},
		{"idle exports", calm, true, idleOver, true,
			"sync: 3700 entries were exported in 2026-10-27T13Z with no round open -- relevo db sync status"},
		{"calm reads as nothing", calm, true, relevosync.ExportHour{Hour: "2026-10-27T13", Idle: relevosync.IdleExportsWarn}, true, ""},
		{"unread readings say nothing", paceOver, false, idleOver, false, ""},
	} {
		if got := syncMeterNotice(tc.q, tc.qok, tc.idle, tc.iok); got != tc.want {
			t.Errorf("%s: notice = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestSyncMeterSilentOnUnreadableRows pins the rule that the meter never
// fails a status: a row that will not parse reads as no answer, and no answer
// prints nothing.
func TestSyncMeterSilentOnUnreadableRows(t *testing.T) {
	t.Parallel()
	st := store.New(t.TempDir())
	d, err := st.DB()
	if err != nil {
		t.Fatalf("Store.DB: %v", err)
	}
	local, err := relevosync.LocalHandle(d)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	now := time.Now()
	if err := local.KVPut(relevosync.BytesKey(now), []byte(`"not the byte counters"`)); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	if err := local.KVPut(relevosync.KeyExportsHours, []byte(`"not the hours"`)); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	q, qok, idle, iok := syncMeter(st, now)
	if qok || iok {
		t.Fatalf("unreadable rows answered: qok %v (%+v), iok %v (%+v)", qok, q, iok, idle)
	}
	if notice := syncMeterNotice(q, qok, idle, iok); notice != "" {
		t.Errorf("notice = %q, want nothing from a file that could not be read", notice)
	}
}

// TestSyncMeterReportsTheSeededMonth pins the happy read: the month's bytes,
// the hour's idle exports, and the notice the readings decide. The snapshot
// row is zero so the only over line is the idle one, whatever day the test
// runs on.
func TestSyncMeterReportsTheSeededMonth(t *testing.T) {
	t.Parallel()
	st := store.New(t.TempDir())
	d, err := st.DB()
	if err != nil {
		t.Fatalf("Store.DB: %v", err)
	}
	local, err := relevosync.LocalHandle(d)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	now := time.Now()
	body, err := json.Marshal(relevosync.AccountSnapshot{
		ReadAt: now.Add(-time.Hour),
		Rows:   map[string]synclog.TrafficRow{d.Origin(): {Month: now.UTC().Format("2006-01")}},
	})
	if err != nil {
		t.Fatalf("encode the snapshot: %v", err)
	}
	if err := local.KVPut(relevosync.KeyAccount, body); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	hour := now.UTC().Add(-time.Hour).Format("2006-01-02T15")
	hours, err := json.Marshal([]relevosync.ExportHour{{Hour: hour, Exported: 3800, Idle: 3700}})
	if err != nil {
		t.Fatalf("encode the hours: %v", err)
	}
	if err := local.KVPut(relevosync.KeyExportsHours, hours); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	q, qok, idle, iok := syncMeter(st, now)
	if !qok || !iok {
		t.Fatalf("qok %v, iok %v; want both answers", qok, iok)
	}
	if idle.Exported != 3800 || idle.Idle != 3700 {
		t.Errorf("idle hour = %+v, want the seeded counts", idle)
	}
	if q.OverWarn() || q.PaceOver() || q.PaceNotice() {
		t.Errorf("a zero month warned: %+v", q)
	}
	want := "sync: 3700 entries were exported in " + hour + "Z with no round open -- relevo db sync status"
	if got := syncMeterNotice(q, qok, idle, iok); got != want {
		t.Errorf("notice = %q, want %q", got, want)
	}
}

// TestDBSyncStatusRendersAccountAndIdleHour fills the real status document
// from a seeded local kv and pins both outputs: the text line carries the
// machine clause, the account clause, the idle clause and a warning, and the
// JSON carries the fields a consumer reads. The machine's own snapshot row
// sits beside the live counters without being counted twice.
func TestDBSyncStatusRendersAccountAndIdleHour(t *testing.T) {
	t.Parallel()
	shared, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: "m1"})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local, err := relevosync.LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	now := time.Now()
	month := now.UTC().Format("2006-01")
	readAt := time.Date(now.Year(), now.Month(), now.Day(), 14, 5, 0, 0, time.UTC)
	counters, err := json.Marshal(relevosync.ByteCounters{TursoPush: 7_000_000_000})
	if err != nil {
		t.Fatalf("encode the counters: %v", err)
	}
	if err := local.KVPut(relevosync.BytesKey(now), counters); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	snap, err := json.Marshal(relevosync.AccountSnapshot{
		ReadAt: readAt,
		Rows: map[string]synclog.TrafficRow{
			"m1": {Month: month, Push: 7_000_000_000},
			"m2": {Month: month, Push: 10_000_000_000, Pull: 5_000_000_000},
			"m3": {Month: "2000-01", Push: 9_000_000_000},
		},
	})
	if err != nil {
		t.Fatalf("encode the snapshot: %v", err)
	}
	if err := local.KVPut(relevosync.KeyAccount, snap); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	hour := now.UTC().Add(-time.Hour).Format("2006-01-02T15")
	hours, err := json.Marshal([]relevosync.ExportHour{{Hour: hour, Exported: 3800, Idle: 3700}})
	if err != nil {
		t.Fatalf("encode the hours: %v", err)
	}
	if err := local.KVPut(relevosync.KeyExportsHours, hours); err != nil {
		t.Fatalf("KVPut: %v", err)
	}

	doc, err := dbSyncStatusRead(local, shared.Origin())
	if err != nil {
		t.Fatalf("dbSyncStatusRead: %v", err)
	}
	if doc.Account == nil || doc.Account.Total != 22_000_000_000 || doc.Account.Machines != 2 {
		t.Fatalf("account = %+v, want 22e9 across m1's live counters and m2's row", doc.Account)
	}
	if doc.IdleExports == nil || !doc.IdleExports.Over {
		t.Fatalf("idle exports = %+v, want the over hour", doc.IdleExports)
	}

	line := dbSyncStatusLine(doc)
	for _, want := range []string{
		"this machine: turso sync 7.0 GB",
		"account: turso sync 22.0 GB of 10.0 GB (220.0%) across 2 machines",
		" · 3700 entries exported in " + hour + "Z with no round open (more than 50 an hour is unexpected)",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("status line %q lacks %q", line, want)
		}
	}
	if doc.Account.PaceOver {
		if !strings.Contains(line, " · warning: the account's Turso traffic is on pace for") {
			t.Errorf("status line %q lacks the pace warning", line)
		}
	} else if !strings.Contains(line, " · warning: the account has used 220.0% of this month's 10.0 GB Turso quota") {
		t.Errorf("status line %q lacks the used-share warning", line)
	}

	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(body)
	for _, want := range []string{
		`"month":"` + month + `"`,
		`"total":22000000000`,
		`"machines":2`,
		`"own":7000000000`,
		`"quota":10000000000`,
		`"read_at":"` + statusStamp(readAt) + `"`,
		`"over_warn":true`,
		`"idle_exports":{"hour":"` + hour + `","exported":3800,"idle":3700,"over":true}`,
		`"bytes"`, `"quota_turso_sync"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("document %s lacks %s", got, want)
		}
	}
	if want := `"pace_over":`; !strings.Contains(got, want+boolLit(doc.Account.PaceOver)) {
		t.Errorf("document %s lacks %s%s", got, want, boolLit(doc.Account.PaceOver))
	}
}

func boolLit(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
