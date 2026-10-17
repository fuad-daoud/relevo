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
	v.asJSON = fs.Bool("json", false, "print the document the reconcile produced")
	v.timeout = fs.Duration("timeout", dbSyncDefaultTimeout, "bound the reconcile")
	v.dryRun = fs.Bool("dry-run", false, "list what a reconcile would append or delete and append nothing")
	return v
}

// dbSyncReconcileDoc is what `db sync reconcile --json` prints.
type dbSyncReconcileDoc struct {
	DryRun bool   `json:"dry_run"`
	Report string `json:"report"`
}

// cmdDBSyncReconcile runs a reconcile, or previews one, through the daemon,
// which holds the log transport.
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
	shared, err := dialSyncVerb()
	if err != nil {
		return err
	}
	defer func() { _ = shared.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), *v.timeout)
	defer cancel()

	res, err := sendSyncVerb(ctx, shared, wire.SyncVerbReconcile,
		dbSyncVerbOptions{Timeout: *v.timeout, DryRun: *v.dryRun})
	if err != nil {
		return err
	}
	if *v.asJSON {
		return printDoc(dbSyncReconcileDoc{DryRun: *v.dryRun, Report: res.Report})
	}
	fmt.Print(res.Report)
	return nil
}
