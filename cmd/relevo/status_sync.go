package main

// The sync meter on `relevo status`: the account's month of Turso traffic and
// the last complete hour's idle exports, read off the machine's local file.
// The rule this file exists to hold is that the meter never fails a status --
// the same rule the daemon notice runs on for its record.

import (
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// syncMeter reads one quota reading and one idle-export hour off the local
// file beside the machine database. Any read error prints nothing: a status
// must never fail because the meter could not be read, so each half answers
// with its own ok and a failed read simply says nothing.
func syncMeter(st *store.Store, now time.Time) (relevosync.QuotaReading, bool, relevosync.ExportHour, bool) {
	d, err := st.DB()
	if err != nil {
		return relevosync.QuotaReading{}, false, relevosync.ExportHour{}, false
	}
	local, err := relevosync.LocalHandle(d)
	if err != nil {
		return relevosync.QuotaReading{}, false, relevosync.ExportHour{}, false
	}
	q, qok, err := relevosync.ReadQuota(local, d.Origin(), now)
	if err != nil {
		q, qok = relevosync.QuotaReading{}, false
	}
	idle, iok, err := relevosync.IdleExports(local, now)
	if err != nil {
		idle, iok = relevosync.ExportHour{}, false
	}
	return q, qok, idle, iok
}

// syncMeterNotice is the status line for the meter, as a function of the
// readings and nothing else. The first thing over decides the notice: a pace
// that has passed the quota outranks a share still climbing, which outranks a
// pace worth mentioning, and idle exports come last. Calm reads as "".
func syncMeterNotice(q relevosync.QuotaReading, qok bool, idle relevosync.ExportHour, iok bool) string {
	switch {
	case qok && q.PaceOver():
		return fmt.Sprintf("sync: the account's Turso traffic is on pace for %s this month, over its %s quota -- relevo db sync status",
			relevosync.FormatTraffic(q.Projected), relevosync.FormatTraffic(q.Quota))
	case qok && q.OverWarn():
		return fmt.Sprintf("sync: the account has used %.1f%% of this month's %s Turso quota -- relevo db sync status",
			q.UsedShare()*100, relevosync.FormatTraffic(q.Quota))
	case qok && q.PaceNotice():
		return fmt.Sprintf("sync: the account's Turso traffic is on pace for %s this month, %.1f%% of its %s quota -- relevo db sync status",
			relevosync.FormatTraffic(q.Projected), q.PaceShare()*100, relevosync.FormatTraffic(q.Quota))
	case iok && idle.OverIdle():
		return fmt.Sprintf("sync: %d entries were exported in %sZ with no round open -- relevo db sync status",
			idle.Idle, idle.Hour)
	}
	return ""
}
