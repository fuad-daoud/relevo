package main

import "testing"

func TestDaemonStopOrUsageRoutesStopAndRefusesTheRest(t *testing.T) {
	tests := []struct {
		name             string
		positional       []string
		check, preflight bool
		wantStop         bool
		wantUsage        bool
	}{
		{name: "no positional runs the daemon"},
		{name: "stop alone stops", positional: []string{"stop"}, wantStop: true},
		{name: "stop with --check is usage", positional: []string{"stop"}, check: true, wantUsage: true},
		{name: "stop with --preflight is usage", positional: []string{"stop"}, preflight: true, wantUsage: true},
		{name: "unknown positional is usage", positional: []string{"start"}, wantUsage: true},
		{name: "stop with an extra word is usage", positional: []string{"stop", "now"}, wantUsage: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stop, err := daemonStopOrUsage(tc.positional, tc.check, tc.preflight)
			if tc.wantUsage {
				requireCLIError(t, err, codeUsage, "")
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if stop != tc.wantStop {
				t.Errorf("stop = %v, want %v", stop, tc.wantStop)
			}
		})
	}
}
