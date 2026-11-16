package main

// `relevo db sync migrate`: move the remote to the format only this release
// reads. The operator confirms every machine already runs this release, because
// a machine still on the old release cannot read format 6, and a late row it
// writes can be lost. The migration runs once, on one machine; the machine's
// own outbox is untouched.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// dbSyncMigrateBound is the client's wait for one migrate answer. It clears
// the daemon's 30-minute worker bound for the migration call plus the dial
// slack, so the CLI never gives up before the worker does. The verb carries
// no timeout of its own: a zero Timeout leaves TimeoutMS unset and the owner
// runs under its own bound.
const dbSyncMigrateBound = 30*time.Minute + 2*verbDialSlack

// dbSyncMigrateFlagValues holds the pointers `db sync migrate` parses into.
type dbSyncMigrateFlagValues struct {
	confirm *bool
	asJSON  *bool
}

func dbSyncMigrateFlagSet(fs *flag.FlagSet) *dbSyncMigrateFlagValues {
	v := &dbSyncMigrateFlagValues{}
	v.confirm = fs.Bool("confirm-all-upgraded", false, "every machine already runs this release, so the remote may move to the new format")
	v.asJSON = fs.Bool("json", false, "print the document the migration produced")
	return v
}

// dbSyncMigrateDoc is the --json answer of a migrate: the counts the daemon
// reported, with the byte totals renamed to name their units. Origins mirrors
// the wire shape, so a reader of the document meets the same rows per origin.
type dbSyncMigrateDoc struct {
	AlreadyMigrated    bool                     `json:"already_migrated"`
	Rows               int                      `json:"rows"`
	Origins            []wire.SyncMigrateOrigin `json:"origins,omitempty"`
	ObjectsWritten     int                      `json:"objects_written"`
	ObjectsVerified    int                      `json:"objects_verified"`
	RowsSwept          int                      `json:"rows_swept"`
	SharedRows         int                      `json:"shared_rows"`
	R2PutBytes         int64                    `json:"r2_put_bytes"`
	R2GetBytes         int64                    `json:"r2_get_bytes"`
	TursoSentBytes     int64                    `json:"turso_sent_bytes"`
	TursoReceivedBytes int64                    `json:"turso_received_bytes"`
}

// dbSyncMigrateDocOf is the --json answer built from one verb result. It is a
// pure function over the result so a test pins the mapping with no daemon.
func dbSyncMigrateDocOf(res *wire.SyncResult) dbSyncMigrateDoc {
	m := res.Migrate
	if m == nil {
		m = &wire.SyncMigrate{}
	}
	return dbSyncMigrateDoc{
		AlreadyMigrated:    m.AlreadyMigrated,
		Rows:               m.Rows,
		Origins:            m.Origins,
		ObjectsWritten:     m.ObjectsCreated,
		ObjectsVerified:    m.ObjectsVerified,
		RowsSwept:          m.RowsSwept,
		SharedRows:         m.SharedRows,
		R2PutBytes:         m.R2Put,
		R2GetBytes:         m.R2Get,
		TursoSentBytes:     m.TursoSent,
		TursoReceivedBytes: m.TursoReceived,
	}
}

// cmdDBSyncMigrate runs the format migration through the daemon. The confirm
// gate runs before the dial, so a call without the flag is a usage refusal
// with no daemon to reach.
func cmdDBSyncMigrate(args []string) error {
	fs := flag.NewFlagSet("db sync migrate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := dbSyncMigrateFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fail(codeUsage, "relevo db sync migrate takes no arguments, got %d", fs.NArg())
	}
	if err := relevosync.MigrateGate(*v.confirm); err != nil {
		return fail(codeUsage, "%v", err)
	}

	shared, err := dialSyncVerb()
	if err != nil {
		return err
	}
	defer func() { _ = shared.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), dbSyncMigrateBound)
	defer cancel()

	res, err := sendSyncVerb(ctx, shared, wire.SyncVerbMigrate, dbSyncVerbOptions{ConfirmAllUpgraded: true})
	if err != nil {
		return err
	}
	if *v.asJSON {
		return printDoc(dbSyncMigrateDocOf(res))
	}
	fmt.Print(dbSyncMigrateText(res))
	return nil
}

// dbSyncMigrateText renders one migration's answer: one line per origin with
// its rows, objects and last sequence, then the totals and the bytes the
// providers bill. The idempotent answer says so instead of reporting zeros,
// which would read as a migration that moved nothing.
func dbSyncMigrateText(res *wire.SyncResult) string {
	m := res.Migrate
	if m == nil {
		m = &wire.SyncMigrate{}
	}
	if m.AlreadyMigrated {
		return "already at format 6; nothing to migrate\n"
	}
	var b strings.Builder
	for _, o := range m.Origins {
		fmt.Fprintf(&b, "%s: %d rows in %d objects, last seq %d\n", o.Origin, o.Rows, o.Objects, o.Seq)
	}
	fmt.Fprintf(&b, "migrated %d rows from %d origins into %d objects (%d verified), swept %d rows, %d shared rows\n",
		m.Rows, len(m.Origins), m.ObjectsCreated, m.ObjectsVerified, m.RowsSwept, m.SharedRows)
	fmt.Fprintf(&b, "turso sent %s, received %s; r2 put %s, get %s\n",
		relevosync.FormatTraffic(m.TursoSent), relevosync.FormatTraffic(m.TursoReceived),
		relevosync.FormatTraffic(m.R2Put), relevosync.FormatTraffic(m.R2Get))
	return b.String()
}
