package main

import (
	"flag"
	"io"
	"strings"
	"testing"
)

// This file runs no subcommand: it parses flags and renders documents, so it
// reaches no harness and no network.

func TestDBSyncEnableAsFlagParses(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("enable", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	v := dbSyncEnableFlagSet(fs)
	if err := fs.Parse([]string{"--as", "inst-x", "--yes"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if *v.adoptAs != "inst-x" || !*v.yes {
		t.Errorf("--as = %q, --yes = %v, want inst-x and true", *v.adoptAs, *v.yes)
	}
}

// TestDBSyncAdoptIntakeRules pins the flag rules of an adoption: it is the one
// enable that cannot be undone, so it needs --yes, and a code joins as a new
// installation so it cannot also name one.
func TestDBSyncAdoptIntakeRules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		as      string
		yes     bool
		joining bool
		wantErr string
	}{
		{"no --as is not an adoption", "", false, false, ""},
		{"--as without --yes", "inst-x", false, false, "pass --yes"},
		{"--as with a code", "inst-x", true, true, "invite code joins"},
		{"--as with --yes", "inst-x", true, false, ""},
	}
	for _, tc := range cases {
		err := dbSyncAdoptIntake(tc.as, tc.yes, tc.joining)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: err = %v, want it to contain %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestDBSyncStatusSaysAnotherMachineAdoptedTheIdentity(t *testing.T) {
	t.Parallel()
	line := dbSyncStatusLine(dbSyncStatusDoc{Enabled: true, IdentityTaken: "inst-x"})
	for _, want := range []string{"another machine adopted installation inst-x", "relevo db sync disable"} {
		if !strings.Contains(line, want) {
			t.Errorf("status line %q does not contain %q", line, want)
		}
	}
	if !strings.Contains(dbSyncAdoptedLine("inst-x"), "restart the relevo daemon") {
		t.Error("the adopted line does not tell the owner to restart the daemon")
	}
}
