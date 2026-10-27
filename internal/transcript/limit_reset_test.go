package transcript

import (
	"testing"
)

// TestLimitResetMS pins which muse line carries a structured reset: the
// failed run's quota instant wins, else the usage allowance window's, and
// anything else carries none.
func TestLimitResetMS(t *testing.T) {
	const quotaMS = 1792848178000
	const windowMS = 1792851778000

	for _, c := range []struct {
		name      string
		kind      string
		line      string
		wantMS    int64
		wantQuota bool
	}{
		{
			name:      "failed run quota instant",
			kind:      "muse",
			line:      museFailedQuota,
			wantMS:    quotaMS,
			wantQuota: true,
		},
		{
			name:      "failed run reason quota instant",
			kind:      "muse",
			line:      `{"payload_type":"run.terminal.failed","payload":{"reason":{"message":"tier limit","quota":{"limit_kind":"tierLimit","resets_at_ms":1792848178000}}}}`,
			wantMS:    quotaMS,
			wantQuota: true,
		},
		{
			name:      "error quota beats reason quota",
			kind:      "muse",
			line:      `{"payload_type":"run.terminal.failed","payload":{"error":{"message":"limited","quota":{"resets_at_ms":1792848178000}},"reason":{"message":"limited","quota":{"resets_at_ms":1792851778000}}}}`,
			wantMS:    quotaMS,
			wantQuota: true,
		},
		{
			name:      "usage allowance window instant",
			kind:      "muse",
			line:      `{"payload_type":"run.usage","payload":{"allowance":{"window":{"resets_at_ms":1792851778000,"used_percent":4},"weekly":{"resets_at_ms":1792972800000,"used_percent":1}},"models":[]}}`,
			wantMS:    windowMS,
			wantQuota: false,
		},
		{
			name: "failed run without a quota carries none",
			kind: "muse",
			line: `{"payload_type":"run.terminal.failed","payload":{"error":"boom"}}`,
		},
		{
			name: "failed run quota without an instant carries none",
			kind: "muse",
			line: `{"payload_type":"run.terminal.failed","payload":{"error":{"message":"limited","quota":{"limit_kind":"tierLimit"}}}}`,
		},
		{
			name: "failed run zero instant carries none",
			kind: "muse",
			line: `{"payload_type":"run.terminal.failed","payload":{"error":{"message":"limited","quota":{"resets_at_ms":0}}}}`,
		},
		{
			name: "usage without an allowance carries none",
			kind: "muse",
			line: `{"payload_type":"run.usage","payload":{"models":[]}}`,
		},
		{
			name: "usage with a null allowance carries none",
			kind: "muse",
			line: `{"payload_type":"run.usage","payload":{"allowance":null,"models":[]}}`,
		},
		{
			name: "completed terminal carries none",
			kind: "muse",
			line: museCompleted,
		},
		{
			name: "model deltas carry none",
			kind: "muse",
			line: `{"payload_type":"run.output.delta","payload":{"text":"Usage limit reached"}}`,
		},
		{
			name: "other kinds carry none",
			kind: "agy",
			line: museFailedQuota,
		},
		{
			name: "non-JSON carries none",
			kind: "muse",
			line: "error: Usage limit reached for your plan",
		},
		{
			name: "blank carries none",
			kind: "muse",
			line: "   \n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			gotMS, gotQuota := LimitResetMS(c.kind, []byte(c.line))
			if gotMS != c.wantMS || gotQuota != c.wantQuota {
				t.Errorf("LimitResetMS(%q, %q) = (%d, %v), want (%d, %v)",
					c.kind, c.line, gotMS, gotQuota, c.wantMS, c.wantQuota)
			}
		})
	}
}
