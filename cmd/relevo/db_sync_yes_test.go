package main

import (
	"errors"
	"flag"
	"io"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

func TestDBSyncEnableYesFlagParses(t *testing.T) {
	t.Parallel()
	for args, want := range map[string]bool{"": false, "--yes": true} {
		fs := flag.NewFlagSet("enable", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		v := dbSyncEnableFlagSet(fs)
		var parsed []string
		if args != "" {
			parsed = []string{args}
		}
		if err := fs.Parse(parsed); err != nil {
			t.Fatalf("Parse(%q): %v", args, err)
		}
		if *v.yes != want {
			t.Errorf("--yes for %q = %v, want %v", args, *v.yes, want)
		}
	}
}

func TestDBSyncOverQuotaIsAUsageRefusalNamingYes(t *testing.T) {
	t.Parallel()
	const message = "sync: the join would use more Turso traffic than this month has left: pass --yes to join anyway"
	classified := dbSyncVerbRefusal(wire.SyncVerbEnable, &client.VerbRefusal{Code: wire.SyncCodeOverQuota, Message: message})
	var got *cliError
	if !errors.As(classified, &got) {
		t.Fatalf("classified = %v, want a cliError", classified)
	}
	if got.code != codeUsage {
		t.Errorf("code = %q, want %q", got.code, codeUsage)
	}
}
