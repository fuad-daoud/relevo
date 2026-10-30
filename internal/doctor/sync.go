package doctor

// doctor's `sync quota` row: the account's month of Turso traffic and the last
// complete hour's idle exports, read from the machine's local file with no
// remote in reach. The row exists when either answer does; a read error means
// the fact was never established, and an unestablished fact is no row.

import (
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/relevo"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// syncQuotaRow reads the meter and builds the row. The read is the same
// no-network local read `relevo status` makes, so the two surfaces cannot
// disagree about the same file. The meter itself lives in cmd/relevo and
// arrives as a parameter.
func syncQuotaRow(rt relevo.Runtime, meter func(rt relevo.Runtime) (relevosync.QuotaReading, bool, relevosync.ExportHour, bool)) (Check, bool) {
	q, qok, idle, iok := meter(rt)
	return syncQuotaCheck(q, qok, idle, iok)
}

// syncQuotaCheck is the row as a function of the readings and nothing else.
// A warning names every reason that holds, not only the first, because a
// reader deciding what to cut back wants the whole picture of the month.
func syncQuotaCheck(q relevosync.QuotaReading, qok bool, idle relevosync.ExportHour, iok bool) (Check, bool) {
	if !qok && !iok {
		return Check{}, false
	}
	c := Check{Name: "sync quota", Severity: SevOK, Fix: "relevo db sync status"}
	var reasons []string
	if qok && q.OverWarn() {
		reasons = append(reasons, fmt.Sprintf("account has used %.1f%% of this month's %s quota",
			q.UsedShare()*100, relevosync.FormatTraffic(q.Quota)))
	}
	if qok && q.PaceOver() {
		reasons = append(reasons, fmt.Sprintf("on pace for %s this month, over the %s quota",
			relevosync.FormatTraffic(q.Projected), relevosync.FormatTraffic(q.Quota)))
	}
	if iok && idle.OverIdle() {
		reasons = append(reasons, fmt.Sprintf("%d entries exported in %sZ with no round open", idle.Idle, idle.Hour))
	}
	if len(reasons) > 0 {
		c.Severity = SevWarn
		c.Detail = strings.Join(reasons, "; ")
		return c, true
	}
	if qok {
		c.Detail = fmt.Sprintf("account %s of %s (%.1f%%) across %s",
			relevosync.FormatTraffic(q.Account.Total), relevosync.FormatTraffic(q.Quota),
			q.UsedShare()*100, relevosync.Machines(q.Account.Machines))
		if q.Projectable {
			c.Detail += fmt.Sprintf(", on pace for %s", relevosync.FormatTraffic(q.Projected))
		}
		return c, true
	}
	c.Detail = fmt.Sprintf("%d entries exported in %sZ with no round open, under the line", idle.Idle, idle.Hour)
	return c, true
}
