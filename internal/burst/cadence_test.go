package burst

import (
	"reflect"
	"testing"
	"time"
)

func TestDue(t *testing.T) {
	t0 := reportTime(8, 0)
	tests := []struct {
		name       string
		rows       []NodeRow
		interval   time.Duration
		lastReport time.Time
		lastGood   time.Time
		waiting    bool
		reason     string
		at         time.Time
		want       bool
	}{
		{
			name: "a report is due once the interval has passed", interval: 30 * time.Second,
			lastReport: t0, waiting: true, at: t0.Add(30 * time.Second), want: true,
		},
		{
			name: "one second short of the interval is not due", interval: 30 * time.Second,
			lastReport: t0, waiting: true, at: t0.Add(29 * time.Second), want: false,
		},
		{
			name: "a held node keeps the short interval while nothing waits", rows: []NodeRow{buildRow(nil)},
			interval: 30 * time.Second, lastReport: t0, at: t0.Add(30 * time.Second), want: true,
		},
		{
			name: "an idle head heartbeats: the interval does not apply", interval: 30 * time.Second,
			lastReport: t0, at: t0.Add(30 * time.Second), want: false,
		},
		{
			name: "an idle head reports at five minutes", interval: 30 * time.Second,
			lastReport: t0, at: t0.Add(HeartbeatInterval), want: true,
		},
		{
			name: "a gone node holds nothing", rows: []NodeRow{buildRow(func(r *NodeRow) { r.Gone = true })},
			interval: 30 * time.Second, lastReport: t0, at: t0.Add(30 * time.Second), want: false,
		},
		{
			name: "a forward reports at once, out of cycle", interval: 30 * time.Second,
			lastReport: t0, waiting: true, reason: ReportReasonForwarded, at: t0.Add(time.Second), want: true,
		},
		{
			name: "a close reports at once, out of cycle", interval: 30 * time.Second,
			lastReport: t0, reason: ReportReasonClosed, at: t0.Add(time.Second), want: true,
		},
		{
			name: "the interval reason is not an out-of-cycle trigger", interval: 30 * time.Second,
			lastReport: t0, waiting: true, reason: ReportReasonInterval, at: t0.Add(10 * time.Second), want: false,
		},
		{
			name: "the startup reason is not an out-of-cycle trigger", interval: 30 * time.Second,
			lastReport: t0, waiting: true, reason: ReportReasonStartup, at: t0.Add(10 * time.Second), want: false,
		},
		{
			name: "a table with no report yet is due at once", at: t0, want: true,
		},
		{
			// The attempt one second before the ordinary interval would
			// expire is the out-of-cycle report of a forward that failed:
			// measuring from LastGood instead would call the retry due at
			// 30 s, exactly a fast retry.
			name: "a failed call waits a full interval from its attempt", interval: 30 * time.Second,
			lastReport: t0.Add(29 * time.Second), lastGood: t0, waiting: true,
			at: t0.Add(30 * time.Second), want: false,
		},
		{
			name: "the retry is due a full interval after the failed attempt", interval: 30 * time.Second,
			lastReport: t0.Add(29 * time.Second), lastGood: t0, waiting: true,
			at: t0.Add(59 * time.Second), want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table := NodeTable{Nodes: tt.rows, Interval: tt.interval, LastReport: tt.lastReport, LastGood: tt.lastGood}
			if got := table.Due(tt.at, tt.waiting, tt.reason); got != tt.want {
				t.Errorf("Due(%v, waiting=%v, reason=%q) = %v, want %v", tt.at, tt.waiting, tt.reason, got, tt.want)
			}
		})
	}
}

// TestRecordFailureKeepsTheLastGoodList pins both halves of a failed call:
// the attempt is recorded — and it is the mark Due measures from, so the
// retry waits a full interval — while the rows, the last-good mark, the
// blocks and the interval stay exactly as the last good response left them.
func TestRecordFailureKeepsTheLastGoodList(t *testing.T) {
	at := reportTime(8, 0)
	preparing := &Preparing{Slots: 2, Since: reportTime(7, 50)}
	denied := &Denied{Reason: "budget", RetryAfter: "1h"}
	table := &NodeTable{}
	table.Merge(at, mergeResp("45s", func(r *ReportResponse) {
		r.Preparing = preparing
		r.Denied = denied
	}, outNode(nil)))
	good := *table

	failed := at.Add(44 * time.Second)
	table.RecordFailure(failed, "burst report: exit 1")
	if !table.LastReport.Equal(failed) {
		t.Errorf("last_report = %v, want the failed attempt's time", table.LastReport)
	}
	if table.LastErr != "burst report: exit 1" {
		t.Errorf("last_err = %q, want the failure kept for the status block", table.LastErr)
	}
	if !table.LastGood.Equal(good.LastGood) || table.Interval != good.Interval {
		t.Errorf("last_good/interval = %v/%v, want the last good response's %v/%v", table.LastGood, table.Interval, good.LastGood, good.Interval)
	}
	if !reflect.DeepEqual(table.Nodes, good.Nodes) || !reflect.DeepEqual(table.Preparing, preparing) || !reflect.DeepEqual(table.Denied, denied) {
		t.Errorf("table = %+v, want the last good list kept", table)
	}
	// One second after the failed attempt the ordinary interval from the
	// last good response would already have expired; the retry waits a full
	// interval from the attempt instead.
	if table.Due(at.Add(45*time.Second), true, ReportReasonInterval) {
		t.Errorf("Due 1s after the failed attempt, want no fast retry")
	}
	if !table.Due(failed.Add(45*time.Second), true, ReportReasonInterval) {
		t.Errorf("Due 45s after the failed attempt, want the full interval retried")
	}
}
