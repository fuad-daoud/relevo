package sync

// The quota meter: one reading of the account's month of Turso traffic against
// its quota, folded from the rows the sync loop already keeps. Every surface
// that warns about the month quotes this one reading so they cannot disagree,
// and it reaches nothing but the machine's local file, so it answers with the
// network out.

import "time"

const (
	// quotaWarnShare is the share of the month's quota whose use makes doctor
	// warn: past it the remainder is smaller than the traffic the last days of
	// a month reliably spend, so the overrun is already decided.
	quotaWarnShare = 0.80
	// paceNoticeShare is the share the month must be on pace to reach before
	// status says so: past half, the pace -- not the usage so far -- already
	// decides where the month ends.
	paceNoticeShare = 0.50
	// paceMinElapsed is the floor under a projection. The projection scales
	// traffic by monthLength/elapsed, so a few early hours scale one busy
	// morning into a whole month and the figure swings wildly tick to tick;
	// one full day is the first window that carries a normal day's work.
	paceMinElapsed = 24 * time.Hour
)

// QuotaReading is the month read account-wide: the account's traffic, this
// machine's share of it, the quota it is spent against, and the month's pace.
type QuotaReading struct {
	Account Account
	// Own is this machine's TursoPush plus TursoPull for the month. It is kept
	// apart from the account total because the total counts rows published by
	// machines still ahead of or behind this one, and a reader comparing the
	// two needs to see them separately.
	Own int64
	// Quota is this month's Turso sync allowance.
	Quota int64
	// Projected is the account total carried to the month's end at the pace so
	// far: Total * monthLength / elapsed.
	Projected int64
	// Projectable is whether enough of the month has elapsed for Projected to
	// mean anything.
	Projectable bool
}

// ReadQuota folds the machine's settings, byte counters and account snapshot
// into one reading. It reports false -- nothing to say -- when this machine
// moved no bytes this month and the snapshot holds no row for the month: a
// machine that has never synced, or one with sync off since before the month
// began. A machine with sync off but traffic earlier in the month still
// reports, because those bytes were spent.
//
// It takes the local handle rather than a bare kv because the quota lives in
// the machine's sync section, which only the local file carries.
func ReadQuota(local Local, origin string, now time.Time) (QuotaReading, bool, error) {
	settings, err := ReadSettings(local)
	if err != nil {
		return QuotaReading{}, false, err
	}
	counters, err := ReadBytes(local, now)
	if err != nil {
		return QuotaReading{}, false, err
	}
	snap, err := ReadAccount(local)
	if err != nil {
		return QuotaReading{}, false, err
	}
	if counters.Zero() && !snapshotHasMonth(snap, now) {
		return QuotaReading{}, false, nil
	}
	q := QuotaReading{
		Account: AccountTraffic(snap, counters, origin, now),
		Own:     counters.TursoPush + counters.TursoPull,
		Quota:   settings.TursoSyncQuota(),
	}
	start := monthStart(now)
	elapsed := now.UTC().Sub(start)
	if elapsed >= paceMinElapsed {
		q.Projectable = true
		// float64 carries the product: a month's traffic times a month's
		// length in nanoseconds overflows int64.
		q.Projected = int64(float64(q.Account.Total) * float64(monthStart(now.AddDate(0, 1, 0)).Sub(start)) / float64(elapsed))
	}
	return q, true, nil
}

// monthStart is the instant the UTC month holding t began.
func monthStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// snapshotHasMonth reports whether the snapshot holds a published row for the
// month holding now. A row of any size counts: it says another machine spent
// traffic this month whether or not this one did.
func snapshotHasMonth(snap AccountSnapshot, now time.Time) bool {
	month := now.UTC().Format("2006-01")
	for _, row := range snap.Rows {
		if row.Month == month {
			return true
		}
	}
	return false
}

// UsedShare is the account's spend against the quota. No quota is no share: a
// quota of zero or less reads as zero rather than dividing by it, because a
// number nobody set warns about nothing.
func (q QuotaReading) UsedShare() float64 {
	if q.Quota <= 0 {
		return 0
	}
	return float64(q.Account.Total) / float64(q.Quota)
}

// PaceShare is where the month's pace lands against the quota, guarded the
// same way UsedShare is.
func (q QuotaReading) PaceShare() float64 {
	if q.Quota <= 0 {
		return 0
	}
	return float64(q.Projected) / float64(q.Quota)
}

// OverWarn is whether the account has used at least quotaWarnShare of the
// month's quota.
func (q QuotaReading) OverWarn() bool { return q.Quota > 0 && q.UsedShare() >= quotaWarnShare }

// PaceOver is whether the month, carried at its pace to the month's end,
// reaches the quota.
func (q QuotaReading) PaceOver() bool {
	return q.Projectable && q.Quota > 0 && q.Projected >= q.Quota
}

// PaceNotice is whether the month is on pace for at least paceNoticeShare of
// its quota.
func (q QuotaReading) PaceNotice() bool {
	return q.Projectable && q.Quota > 0 && q.PaceShare() >= paceNoticeShare
}

// FormatTraffic renders a meter byte count the way the providers bill them --
// powers of 1000 -- so every sentence about traffic on every surface quotes
// the units the quota is set in.
func FormatTraffic(n int64) string { return humanBytes(n) }
