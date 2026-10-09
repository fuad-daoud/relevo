package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCheckSplitCapability(t *testing.T) {
	old := "ok v0.15.0-372-g4544fb5a\n"
	current := "ok v0.15.0-416-gea5423d4 split=1\n"

	tests := []struct {
		name        string
		out         string
		splitActive bool
		wantErr     bool
	}{
		{name: "no marker takes every candidate", out: old, splitActive: false},
		{name: "no marker takes a split candidate", out: current, splitActive: false},
		{name: "marker takes a split candidate", out: current, splitActive: true},
		{name: "marker refuses a pre-split candidate", out: old, splitActive: true, wantErr: true},
		{name: "marker refuses empty output", out: "", splitActive: true, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckSplitCapability(tc.out, tc.splitActive)
			if tc.wantErr && err == nil {
				t.Fatalf("CheckSplitCapability(%q, %t) = nil, want an error", tc.out, tc.splitActive)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("CheckSplitCapability(%q, %t) = %v, want nil", tc.out, tc.splitActive, err)
			}
		})
	}
}

func TestCheckSplitCapabilityRefusalNamesTheFix(t *testing.T) {
	err := CheckSplitCapability("ok v0.15.0-372-g4544fb5a\n", true)
	if err == nil {
		t.Fatal("refusal = nil, want an error")
	}
	if !strings.Contains(err.Error(), "split=1") {
		t.Errorf("refusal = %q, want it to name the missing token", err)
	}
}

// preflightStub writes an executable script standing in for a candidate
// binary's `daemon --preflight`: it prints out and exits code.
func preflightStub(t *testing.T, out string, code int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relevo")
	script := "echo " + shellQuote(out) + "; exit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func TestSplitGuardPreflight(t *testing.T) {
	old := preflightStub(t, "ok v0.15.0-372-g4544fb5a", 0)
	current := preflightStub(t, "ok v0.15.0-416-gea5423d4 split=1", 0)
	broken := preflightStub(t, "relevo: bad policy", 1)

	tests := []struct {
		name        string
		path        string
		splitActive bool
		wantErr     string
	}{
		{name: "no marker takes a blind candidate", path: old, splitActive: false},
		{name: "marker takes a split candidate", path: current, splitActive: true},
		{name: "marker refuses a blind candidate", path: old, splitActive: true, wantErr: "split=1"},
		{name: "a failing preflight carries its first line", path: broken, splitActive: true, wantErr: "bad policy"},
		{name: "a failing preflight carries its first line without a marker", path: broken, splitActive: false, wantErr: "bad policy"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := SplitGuardPreflight(tc.splitActive)(context.Background(), tc.path)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("preflight = %v, want nil", err)
			}
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("preflight = nil, want an error carrying %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("preflight = %q, want it to carry %q", err, tc.wantErr)
				}
			}
		})
	}
}
