package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// dbSyncReconcileFlagValues holds the pointers `db sync reconcile` parses into.
type dbSyncReconcileFlagValues struct {
	asJSON  *bool
	timeout *time.Duration
	dryRun  *bool
}

func dbSyncReconcileFlagSet(fs *flag.FlagSet) *dbSyncReconcileFlagValues {
	v := &dbSyncReconcileFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the preview produced")
	v.timeout = fs.Duration("timeout", dbSyncDefaultTimeout, "bound the preview")
	v.dryRun = fs.Bool("dry-run", false, "list what a reconcile would append or delete and append nothing; required")
	return v
}

// dbSyncReconcileDoc is what `db sync reconcile --json` prints.
type dbSyncReconcileDoc struct {
	DryRun bool   `json:"dry_run"`
	Report string `json:"report"`
}

// cmdDBSyncReconcile previews a reconcile through the daemon, which holds the
// log transport. Only the dry run exists, so a call without --dry-run is
// refused before it reaches the socket.
func cmdDBSyncReconcile(args []string) error {
	fs := flag.NewFlagSet("db sync reconcile", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := dbSyncReconcileFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fail(codeUsage, "relevo db sync reconcile takes no arguments, got %d", fs.NArg())
	}
	if !*v.dryRun {
		return fail(codeUsage, "relevo db sync reconcile only previews: pass --dry-run")
	}

	shared, err := dialSyncVerb()
	if err != nil {
		return err
	}
	defer func() { _ = shared.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), *v.timeout)
	defer cancel()

	res, err := sendSyncVerb(ctx, shared, wire.SyncVerbReconcile,
		dbSyncVerbOptions{Timeout: *v.timeout, DryRun: true})
	if err != nil {
		return err
	}
	if *v.asJSON {
		return printDoc(dbSyncReconcileDoc{DryRun: true, Report: res.Report})
	}
	fmt.Print(res.Report)
	return nil
}
