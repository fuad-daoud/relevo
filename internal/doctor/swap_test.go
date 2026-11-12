package doctor

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
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

// TestSwapCheckWiring pins that the serve-scope build carries the swap row.
// SwapCheck's own table passes even when the append is dropped, so this
// drives appendServeScopeRows, the seam RunReport reaches on the doctor path.
func TestSwapCheckWiring(t *testing.T) {
	const header = "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n"
	swapRow := func(t *testing.T, env Env) *Check {
		t.Helper()
		rt := relevo.Runtime{Store: store.New(filepath.Join(t.TempDir(), "store"))}
		var rep Report
		appendServeScopeRows(&rep, rt, env)
		return findCheck(rep, "", "swap")
	}

	t.Run("unreadable", func(t *testing.T) {
		env := errReadEnv{Env: &fakeEnv{}, err: errors.New("permission denied")}
		got := swapRow(t, env)
		if got == nil {
			t.Fatal("no swap row: an unreadable /proc/swaps must still report")
		}
		if !got.ProbeFailed {
			t.Errorf("row = %+v, want a failed probe", *got)
		}
	})

	t.Run("present", func(t *testing.T) {
		env := &fakeEnv{fileContents: map[string]string{
			"/proc/swaps": header + "/dev/sda2                               partition\t8388604\t\t0\t\t-2\n",
		}}
		got := swapRow(t, env)
		if got == nil {
			t.Fatal("no swap row: readable swap content must report")
		}
		if got.ProbeFailed {
			t.Errorf("row = %+v, want a successful probe", *got)
		}
		if !strings.Contains(got.Detail, "swap active on /dev/sda2") {
			t.Errorf("Detail = %q, want it to name the swap device", got.Detail)
		}
	})
}
