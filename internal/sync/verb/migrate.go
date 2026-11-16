package verb

import (
	"context"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// migrate runs the format migration against the released replica: the gate
// first, so an unconfirmed call opens nothing and releases nothing, then the
// steady worker cancelled, the migrator opened, and the migration run. A second
// retry and a trigger leave the machine as a retry leaves it, so the next
// attempt opens a fresh worker at the new format.
func (v *Runner) migrate(ctx context.Context, verb *wire.SyncVerb) *wire.SyncResult {
	if err := relevosync.MigrateGate(verb.ConfirmAllUpgraded); err != nil {
		return Refusal(Classify(err), err)
	}
	if v.OpenMigrate == nil || v.Runner == nil {
		return Refusal(wire.SyncCodeInvalid,
			fmt.Errorf("sync: migrate: no migrate opener is installed: %w", db.ErrInvalid))
	}
	// The steady worker holds the replica file the migration rewrites, so the
	// latch is cleared and the worker cancelled before anything opens.
	if err := v.Runner.Retry(); err != nil {
		return Refusal(Classify(err), err)
	}
	migrator, err := v.OpenMigrate(ctx)
	if err != nil {
		return Refusal(Classify(err), err)
	}
	// A close that fails leaves the worker for the next attempt's open; the
	// migration's own counts stand either way.
	defer func() { _ = migrator.Close() }()
	res, err := migrator.Migrate()
	if err != nil {
		return Refusal(Classify(err), err)
	}
	if err := v.Runner.Retry(); err != nil {
		return Refusal(Classify(err), err)
	}
	if v.Trigger != nil {
		v.Trigger()
	}
	return &wire.SyncResult{OK: true, Migrate: toWireMigrate(res)}
}

// toWireMigrate converts one migration's answer into the shape the CLI prints.
func toWireMigrate(res synclog.MigrateResult) *wire.SyncMigrate {
	out := &wire.SyncMigrate{
		AlreadyMigrated: res.AlreadyMigrated,
		Rows:            res.Rows,
		ObjectsCreated:  res.ObjectsCreated,
		ObjectsVerified: res.ObjectsVerified,
		RowsSwept:       res.RowsSwept,
		SharedRows:      res.SharedRows,
		R2Put:           res.R2Put,
		R2Get:           res.R2Get,
		TursoSent:       res.TursoSent,
		TursoReceived:   res.TursoReceived,
	}
	for _, entry := range res.Origins {
		out.Origins = append(out.Origins, wire.SyncMigrateOrigin{
			Origin:     entry.Origin,
			Rows:       entry.Rows,
			Objects:    entry.Objects,
			Seq:        entry.Seq,
			Floor:      entry.Floor,
			LegacyUpto: entry.LegacyUpto,
			Label:      entry.Label,
		})
	}
	return out
}
