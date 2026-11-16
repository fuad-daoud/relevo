package main

import (
	"flag"
	"io"
	"strings"
	"testing"
)

// This file runs no subcommand: it parses flags and validates them, so it
// reaches no harness and no network.

func TestDBSyncEnableHistoryFlagParses(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("enable", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	v := dbSyncEnableFlagSet(fs)
	if *v.history != "" {
		t.Fatalf("--history defaults to %q, want empty for full", *v.history)
	}
	if err := fs.Parse([]string{"--history", "none"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if *v.history != "none" {
		t.Errorf("--history = %q, want none", *v.history)
	}
}

// TestDBSyncEnableHistoryRules pins the client-side validation of --history:
// the mode must name a reading mode, and --history none cannot go beside --as,
// because an adoption continues as an installation whose history a none
// machine never reads. The verb refuses the same combination itself, and the
// message here carries its sentence so the two stay identical.
func TestDBSyncEnableHistoryRules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		history string
		as      string
		wantErr string
	}{
		{"empty is full", "", "", ""},
		{"full", "full", "", ""},
		{"none", "none", "", ""},
		{"unknown mode", "all", "", "not a reading mode"},
		{"none beside --as", "none", "inst-x", "--as cannot be used with --history none"},
		{"full beside --as", "full", "inst-x", ""},
		{"empty beside --as", "", "inst-x", ""},
	}
	for _, tc := range cases {
		err := dbSyncEnableHistory(tc.history, tc.as)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: history %q beside --as %q refused: %v", tc.name, tc.history, tc.as, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: history %q beside --as %q accepted, want a refusal naming %q", tc.name, tc.history, tc.as, tc.wantErr)
			continue
		}
		ce := requireCLIError(t, err, codeUsage, "")
		if !strings.Contains(ce.Error(), tc.wantErr) {
			t.Errorf("%s: refusal %q does not contain %q", tc.name, ce.Error(), tc.wantErr)
		}
	}
}

// TestDBSyncUsageNamesHistory pins that the usage names the --history mode and
// says a machine's mode changes only through disable then enable: the flag is
// new, and a usage line that drifts from the flag set is the failure a user
// hits first.
func TestDBSyncUsageNamesHistory(t *testing.T) {
	t.Parallel()
	for _, want := range []string{"[--history full|none]", "--history names how much", "relevo db sync disable", "relevo db sync enable --history"} {
		if !strings.Contains(dbSyncUsage, want) {
			t.Errorf("the usage text does not mention %q:\n%s", want, dbSyncUsage)
		}
	}
}
