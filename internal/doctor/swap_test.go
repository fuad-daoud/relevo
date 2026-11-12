package doctor

import (
	"errors"
	"strings"
	"testing"
)

// TestSwapCheck pins the swap row's table: swap present, zram only, both,
// neither (header-only and empty), and unreadable as a failed probe.
func TestSwapCheck(t *testing.T) {
	const header = "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n"
	tests := []struct {
		name      string
		raw       string
		readErr   error
		wantSev   Severity
		wantProbe bool
		wantFix   bool
		wantDet   []string
	}{
		{
			name:    "swap present",
			raw:     header + "/dev/sda3                               partition\t8388604\t\t0\t\t-2\n",
			wantSev: SevOK,
			wantDet: []string{"swap active on /dev/sda3"},
		},
		{
			name:    "zram only",
			raw:     header + "/dev/zram0                              partition\t16777212\t0\t\t100\n",
			wantSev: SevOK,
			wantDet: []string{"zram swap active on /dev/zram0"},
		},
		{
			name:    "both names zram",
			raw:     header + "/dev/sda3                               partition\t8388604\t\t0\t\t-2\n/dev/zram0                              partition\t16777212\t0\t\t100\n",
			wantSev: SevOK,
			wantDet: []string{"zram swap active on /dev/zram0"},
		},
		{
			name:    "header only warns with the fix",
			raw:     header,
			wantSev: SevWarn,
			wantFix: true,
			wantDet: []string{"no swap/zram: a burst that exceeds memory has nowhere to spill"},
		},
		{
			name:    "empty warns with the fix",
			raw:     "",
			wantSev: SevWarn,
			wantFix: true,
			wantDet: []string{"no swap/zram: a burst that exceeds memory has nowhere to spill"},
		},
		{
			name:      "unreadable is a failed probe",
			readErr:   errors.New("permission denied"),
			wantSev:   SevWarn,
			wantProbe: true,
			wantDet:   []string{"cannot read /proc/swaps", "permission denied"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SwapCheck([]byte(tc.raw), tc.readErr)
			if got.Name != "swap" {
				t.Errorf("Name = %q, want swap", got.Name)
			}
			if got.Severity != tc.wantSev || got.ProbeFailed != tc.wantProbe {
				t.Errorf("row = %+v, want severity %v probeFailed %v", got, tc.wantSev, tc.wantProbe)
			}
			for _, want := range tc.wantDet {
				if !strings.Contains(got.Detail, want) {
					t.Errorf("Detail = %q, want it to contain %q", got.Detail, want)
				}
			}
			if tc.wantFix && got.Fix == "" {
				t.Error("Fix is empty, want the swapfile one-liner")
			}
			if !tc.wantFix && got.Fix != "" {
				t.Errorf("Fix = %q, want empty", got.Fix)
			}
		})
	}
}

// TestSwapCheckSkipsShortLines pins that a malformed short line is not an
// entry: header plus garbage still warns.
func TestSwapCheckSkipsShortLines(t *testing.T) {
	raw := "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n/dev/sda3\n\n"
	got := SwapCheck([]byte(raw), nil)
	if got.Severity != SevWarn {
		t.Errorf("row = %+v, want a warn: a one-field line is not a swap entry", got)
	}
}
