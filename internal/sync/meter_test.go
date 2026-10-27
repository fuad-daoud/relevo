package sync

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// meterNow is a mid-month instant: October 2026 has 31 days, so a projection
// from it is a ratio a reader can check by hand.
var meterNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// seedMeterBytes writes this machine's month counters the way the steady loop
// leaves them.
func seedMeterBytes(t *testing.T, local Local, at time.Time, c ByteCounters) {
	t.Helper()
	body, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("encode the byte counters: %v", err)
	}
	if err := local.KVPut(BytesKey(at), body); err != nil {
		t.Fatalf("write the byte counters: %v", err)
	}
}

// seedMeterSnapshot writes the account snapshot a publish keeps.
func seedMeterSnapshot(t *testing.T, local Local, snap AccountSnapshot) {
	t.Helper()
	body, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("encode the account snapshot: %v", err)
	}
	if err := local.KVPut(KeyAccount, body); err != nil {
		t.Fatalf("write the account snapshot: %v", err)
	}
}

// A machine that has never synced has no month row and no snapshot row to
// fold: the meter reports nothing rather than a zero the reader would have to
// interpret.
func TestReadQuotaSilentWhenNothingMovedAndNoMonthRow(t *testing.T) {
	t.Parallel()
	_, local := joinPair(t, "m1")
	seedMeterSnapshot(t, local, AccountSnapshot{
		ReadAt: meterNow.Add(-24 * time.Hour),
		Rows:   map[string]synclog.TrafficRow{"m2": {Month: "2026-09", Push: 5_000_000_000}},
	})
	q, ok, err := ReadQuota(local, "m1", meterNow)
	if err != nil || ok {
		t.Fatalf("reading = %+v, ok %v, err %v; want nothing to report", q, ok, err)
	}
}

// The same file with a row for the current month is something to report, even
// with no bytes of this machine's own: those bytes were spent by a machine on
// this account.
func TestReadQuotaReportsCurrentMonthRowWithoutOwnBytes(t *testing.T) {
	t.Parallel()
	_, local := joinPair(t, "m1")
	seedMeterSnapshot(t, local, AccountSnapshot{
		ReadAt: meterNow.Add(-time.Hour),
		Rows:   map[string]synclog.TrafficRow{"m2": {Month: "2026-10", Push: 3_000_000_000, Pull: 1_000_000_000}},
	})
	q, ok, err := ReadQuota(local, "m1", meterNow)
	if err != nil || !ok {
		t.Fatalf("ok %v, err %v; want the month's published row reported", ok, err)
	}
	if q.Own != 0 || q.Account.Total != 4_000_000_000 {
		t.Errorf("own %d, total %d; want 0 and 4e9", q.Own, q.Account.Total)
	}
}

// Own bytes alone give a reading whose account is this one machine: the
// fold starts at the live counters and counts no row twice.
func TestReadQuotaOwnBytesOnlyMachinesOne(t *testing.T) {
	t.Parallel()
	_, local := joinPair(t, "m1")
	seedMeterBytes(t, local, meterNow, ByteCounters{TursoPush: 4_000_000_000, TursoPull: 3_000_000_000, R2Put: 99})
	q, ok, err := ReadQuota(local, "m1", meterNow)
	if err != nil || !ok {
		t.Fatalf("ok %v, err %v; want the month's bytes reported", ok, err)
	}
	if q.Own != 7_000_000_000 {
		t.Errorf("own = %d, want push plus pull 7e9", q.Own)
	}
	if q.Account.Total != 7_000_000_000 || q.Account.Machines != 1 {
		t.Errorf("account = %+v, want total 7e9 across one machine", q.Account)
	}
	if q.Quota != 10_000_000_000 {
		t.Errorf("quota = %d, want the default allowance", q.Quota)
	}
}

// Another machine's current-month row joins the account total, and its
// previous-month row does not: the meter measures this month, not the
// installation's whole history.
func TestReadQuotaFoldsOnlyCurrentMonthRows(t *testing.T) {
	t.Parallel()
	_, local := joinPair(t, "m1")
	seedMeterBytes(t, local, meterNow, ByteCounters{TursoPush: 1_000_000_000})
	seedMeterSnapshot(t, local, AccountSnapshot{
		ReadAt: meterNow.Add(-time.Hour),
		Rows: map[string]synclog.TrafficRow{
			"m2": {Month: "2026-10", Push: 2_000_000_000, Pull: 500_000_000},
			"m3": {Month: "2026-09", Push: 8_000_000_000},
		},
	})
	q, ok, err := ReadQuota(local, "m1", meterNow)
	if err != nil || !ok {
		t.Fatalf("ok %v, err %v", ok, err)
	}
	if q.Account.Total != 3_500_000_000 || q.Account.Machines != 2 {
		t.Errorf("account = %+v, want 3.5e9 across 2 machines (own 1e9 plus m2's 2.5e9, not m3's September)", q.Account)
	}
}

// The pace needs a day of the month before it means anything: at 23 hours
// elapsed the factor would scale an early burst wildly, at 25 it is a
// projection.
func TestReadQuotaProjectableOnlyAfterADay(t *testing.T) {
	t.Parallel()
	_, local := joinPair(t, "m1")
	early := time.Date(2026, 10, 1, 23, 0, 0, 0, time.UTC)
	later := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
	seedMeterBytes(t, local, early, ByteCounters{TursoPush: 1_000_000_000})
	q, ok, err := ReadQuota(local, "m1", early)
	if err != nil || !ok {
		t.Fatalf("ok %v, err %v; traffic is there even at 23 hours", ok, err)
	}
	if q.Projectable || q.Projected != 0 {
		t.Errorf("at 23 hours: projectable %v, projected %d; want neither", q.Projectable, q.Projected)
	}
	q, _, err = ReadQuota(local, "m1", later)
	if err != nil {
		t.Fatalf("ReadQuota: %v", err)
	}
	// 1e9 at 25 hours of a 744-hour October projects to 1e9*744/25 = 29.76e9.
	want := int64(29_760_000_000)
	if !q.Projectable || q.Projected < want-1_000_000 || q.Projected > want+1_000_000 {
		t.Errorf("at 25 hours: projectable %v, projected %d; want about %d", q.Projectable, q.Projected, want)
	}
}

// A mid-month projection worked by hand: 3 GB ten days into a 31-day month
// carries the month to 3e9 * 31/10 = 9.3 GB.
func TestReadQuotaMidMonthProjectionByHand(t *testing.T) {
	t.Parallel()
	_, local := joinPair(t, "m1")
	at := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	seedMeterBytes(t, local, at, ByteCounters{TursoPush: 3_000_000_000})
	q, ok, err := ReadQuota(local, "m1", at)
	if err != nil || !ok {
		t.Fatalf("ok %v, err %v", ok, err)
	}
	if !q.Projectable || q.Projected < 9_299_000_000 || q.Projected > 9_301_000_000 {
		t.Errorf("projected = %d (projectable %v), want about 9.3 GB", q.Projected, q.Projectable)
	}
}

// The warning edges sit where the constants say: at exactly 80% the account
// is over the warn share, at exactly 50% of the quota the pace is worth a
// notice, and a projection that just reaches the quota is over.
func TestQuotaThresholdEdges(t *testing.T) {
	t.Parallel()
	over := QuotaReading{Account: Account{Total: 8_000_000_000}, Quota: 10_000_000_000, Projectable: true, Projected: 4_000_000_000}
	if !over.OverWarn() {
		t.Error("exactly 80% used is not over the warn share")
	}
	if over.PaceOver() || over.PaceNotice() {
		t.Error("a 40% pace is neither over nor notice-worthy")
	}
	calm := QuotaReading{Account: Account{Total: 7_000_000_000}, Quota: 10_000_000_000, Projectable: true, Projected: 4_900_000_000}
	if calm.OverWarn() {
		t.Error("70% used warns: the threshold reads wrong")
	}
	if calm.PaceNotice() {
		t.Error("a 49% pace earns no notice")
	}
	halfPace := QuotaReading{Account: Account{Total: 1_000_000_000}, Quota: 10_000_000_000, Projectable: true, Projected: 5_000_000_000}
	if !halfPace.PaceNotice() {
		t.Error("exactly 50% pace earns no notice")
	}
	if halfPace.PaceOver() || halfPace.OverWarn() {
		t.Error("a 50% pace with 10% used is neither over-quota pace nor over-warn use")
	}
	reached := QuotaReading{Account: Account{Total: 1_000_000_000}, Quota: 10_000_000_000, Projectable: true, Projected: 10_000_000_000}
	if !reached.PaceOver() {
		t.Error("a projection that reaches the quota is not over")
	}
}

// A pace nobody asked for: no quota means no warnings, and no division.
func TestQuotaNoQuotaWarnsNothing(t *testing.T) {
	t.Parallel()
	q := QuotaReading{Account: Account{Total: 99_000_000_000}, Quota: 0, Projected: 200_000_000_000, Projectable: true}
	if q.UsedShare() != 0 || q.PaceShare() != 0 {
		t.Errorf("shares = %v, %v; want zero without a quota", q.UsedShare(), q.PaceShare())
	}
	if q.OverWarn() || q.PaceOver() || q.PaceNotice() {
		t.Error("a machine with no quota warns against nothing")
	}
}

// The account clause in its three shapes: full with pace, unprojectable, and
// the snapshot never read -- which is a machine that has not looked at the
// account at all, not one whose account is idle.
func TestFormatAccountShapes(t *testing.T) {
	t.Parallel()
	readAt := time.Date(2026, 10, 27, 14, 5, 0, 0, time.UTC)
	full := QuotaReading{
		Account:     Account{Total: 22_200_000_000, Machines: 3, ReadAt: readAt},
		Quota:       10_000_000_000,
		Projected:   30_100_000_000,
		Projectable: true,
	}
	if got, want := FormatAccount(full), "account: turso sync 22.2 GB of 10.0 GB (222.0%) across 3 machines, on pace for 30.1 GB, as of 14:05Z"; got != want {
		t.Errorf("full = %q, want %q", got, want)
	}
	early := full
	early.Projectable = false
	early.Projected = 0
	if got, want := FormatAccount(early), "account: turso sync 22.2 GB of 10.0 GB (222.0%) across 3 machines, as of 14:05Z"; got != want {
		t.Errorf("unprojectable = %q, want %q", got, want)
	}
	never := QuotaReading{Account: Account{Total: 22_200_000_000, Machines: 3}}
	if got, want := FormatAccount(never), "account: not read yet"; got != want {
		t.Errorf("never read = %q, want %q", got, want)
	}
}
