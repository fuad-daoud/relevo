//go:build !modernc

package sync

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/db"
	turso "turso.tech/database/tursogo"
)

// OpenRemote builds the handle one open config describes, on the driver that
// carries the sync client. It is the only place in this package that names the
// driver's constructor, so this interface and the driver's own signatures cannot
// drift apart without a build failing -- the same argument Turso has for
// existing beside them.
//
// BootstrapIfEmpty is always set rather than left unset: the seed matrix has
// already decided, and a nil would hand the decision back to the driver's
// default, which is exactly the ambiguity the matrix exists to remove.
//
// The role is checked before the driver is reached, and that ordering is the
// whole point of the check: the driver opens the path it is given and turns a
// bare file into a member without saying so, so a refusal made afterwards would
// come after the file had already been rewritten.
//
// The token is converted to a string because that is the driver's field type.
// It goes into the config and into nothing else: no error on this path formats
// the config, so a driver error cannot carry the value out with it.
//
// The two thresholds are carried through rather than decided here, because the
// call they bound is not this function's: it is the first push and pull the
// handle makes afterwards. Only the enable's own open sets them, because only
// the enable's open moves a whole database for the first time; every later open
// moves one round of changes.
//
// ConvergeSidecars runs between the role check and the driver. The sidecar is
// the one file beside the database that the engine reads and nothing else does,
// so a torn one is invisible to every read this repository makes and fatal to
// every push and pull; converging it here is what puts the two in agreement
// before the driver is the one holding both.
func OpenRemote(ctx context.Context, cfg OpenConfig) (SyncClient, error) {
	if err := checkOpenRole(cfg); err != nil {
		return nil, err
	}
	// The path is converged before the driver is handed it, and here because
	// here is the only place that hands it over: a sidecar the engine cannot
	// parse refuses every push and pull from inside the engine, long after the
	// query path has stopped being able to tell. Naming it here is what makes
	// the removal happen before the refusal rather than after it.
	if repair, err := ConvergeSidecars(cfg.Path); err != nil {
		return nil, err
	} else if repair.Path != "" {
		slog.Warn("sync: the changes sidecar was incomplete and was removed; the next open rebuilds it",
			"path", repair.Path, "bytes", repair.Bytes, "orphan_table", repair.OrphanTable)
	}
	bootstrap := cfg.BootstrapIfEmpty
	newHandle := func(ctx context.Context) (*turso.TursoSyncDb, error) {
		return turso.NewTursoSyncDb(ctx, turso.TursoSyncDbConfig{
			Path:                    cfg.Path,
			RemoteUrl:               cfg.RemoteURL,
			Namespace:               cfg.Namespace,
			AuthToken:               string(cfg.AuthToken),
			ClientName:              cfg.ClientName,
			BootstrapIfEmpty:        &bootstrap,
			PullBytesThreshold:      cfg.PullBytesThreshold,
			PushOperationsThreshold: cfg.PushOperationsThreshold,
		})
	}
	handle, err := turso.NewTursoSyncDb(ctx, turso.TursoSyncDbConfig{
		Path:                    cfg.Path,
		RemoteUrl:               cfg.RemoteURL,
		Namespace:               cfg.Namespace,
		AuthToken:               string(cfg.AuthToken),
		ClientName:              cfg.ClientName,
		BootstrapIfEmpty:        &bootstrap,
		PullBytesThreshold:      cfg.PullBytesThreshold,
		PushOperationsThreshold: cfg.PushOperationsThreshold,
	})
	if err != nil {
		return nil, fmt.Errorf("sync: open the remote: %w", err)
	}
	// The client keeps the path and a way to rebuild itself, because the file it
	// was handed is the file whose log a stale revert watermark constrains. The
	// engine reads that watermark out of the sidecar when it opens, so undoing
	// one means opening again over the same config rather than retrying through
	// a handle that has already cached what is about to change.
	// The driver turns a bare file into a member inside the open above and says
	// nothing about it, so this is the one place that knows the file is one now.
	// Recording it is what makes the database package's own connections carry
	// their writes into the change set the push reads: turso captures per
	// connection, so a write on a connection without the pragma is a row no push
	// can send.
	db.MarkSyncMember(cfg.Path)
	client := NewTurso(handle)
	client.path = cfg.Path
	client.Reopen = func(ctx context.Context) (syncDatabase, error) { return newHandle(ctx) }
	return client, nil
}
