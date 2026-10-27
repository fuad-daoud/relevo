package main

// The doctor row is pinned as a pure function of the readings: no test here
// opens a store, spawns a harness or reaches a network.

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/doctor"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

func TestSyncQuotaCheckNoRowWithoutEitherReading(t *testing.T) {
	t.Parallel()
	if _, ok := syncQuotaCheck(relevosync.QuotaReading{}, false, relevosync.ExportHour{}, false); ok {
		t.Error("a machine that answered nothing got a row")
	}
}

func TestSyncQuotaCheckOKNamesAccountAndPace(t *testing.T) {
	t.Parallel()
	q := relevosync.QuotaReading{
		Account:     relevosync.Account{Total: 1_000_000_000, Machines: 2},
		Own:         600_000_000,
		Quota:       10_000_000_000,
		Projected:   4_000_000_000,
		Projectable: true,
	}
	c, ok := syncQuotaCheck(q, true, relevosync.ExportHour{}, false)
	if !ok {
		t.Fatal("a read month got no row")
	}
	if c.Name != "sync quota" || c.Severity != doctor.SevOK {
		t.Errorf("row = %q, sev %v; want a calm sync quota row", c.Name, c.Severity)
	}
	if want := "account 1.0 GB of 10.0 GB (10.0%) across 2 machines, on pace for 4.0 GB"; c.Detail != want {
		t.Errorf("detail = %q, want %q", c.Detail, want)
	}
	if c.Fix != "relevo db sync status" {
		t.Errorf("fix = %q", c.Fix)
	}
	unprojected := q
	unprojected.Projected, unprojected.Projectable = 0, false
	calm, _ := syncQuotaCheck(unprojected, true, relevosync.ExportHour{}, false)
	if want := "account 1.0 GB of 10.0 GB (10.0%) across 2 machines"; calm.Detail != want {
		t.Errorf("unprojectable detail = %q, want %q", calm.Detail, want)
	}
}

func TestSyncQuotaCheckWarnNamesEveryReason(t *testing.T) {
	t.Parallel()
	q := relevosync.QuotaReading{
		Account:     relevosync.Account{Total: 8_500_000_000, Machines: 3},
		Quota:       10_000_000_000,
		Projected:   12_000_000_000,
		Projectable: true,
	}
	idle := relevosync.ExportHour{Hour: "2026-10-27T13", Exported: 3800, Idle: 3700}
	c, ok := syncQuotaCheck(q, true, idle, true)
	if !ok {
		t.Fatal("a warning month got no row")
	}
	if c.Severity != doctor.SevWarn {
		t.Fatalf("sev = %v, want a warning", c.Severity)
	}
	for _, want := range []string{
		"account has used 85.0% of this month's 10.0 GB quota",
		"on pace for 12.0 GB this month, over the 10.0 GB quota",
		"3700 entries exported in 2026-10-27T13Z with no round open",
	} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("detail %q lacks %q", c.Detail, want)
		}
	}
	if c.Fix != "relevo db sync status" {
		t.Errorf("fix = %q", c.Fix)
	}
}

// A machine that only booked exports still earns a row, and an hour under the
// line reads as calm.
func TestSyncQuotaCheckIdleAloneReadsCalm(t *testing.T) {
	t.Parallel()
	idle := relevosync.ExportHour{Hour: "2026-10-27T13", Exported: 40, Idle: 40}
	c, ok := syncQuotaCheck(relevosync.QuotaReading{}, false, idle, true)
	if !ok || c.Severity != doctor.SevOK {
		t.Fatalf("ok %v, sev %v; want a calm row for an under-line hour", ok, c.Severity)
	}
	if !strings.Contains(c.Detail, "40 entries exported in 2026-10-27T13Z") {
		t.Errorf("detail = %q", c.Detail)
	}
}
