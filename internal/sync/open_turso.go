//go:build !modernc

package sync

import (
	"context"
	"fmt"

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
func OpenRemote(ctx context.Context, cfg OpenConfig) (SyncClient, error) {
	if err := checkOpenRole(cfg); err != nil {
		return nil, err
	}
	bootstrap := cfg.BootstrapIfEmpty
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
	// The open is what turns a bare file into a member, so it is the one place
	// that knows the file is one now. Recording it is what makes the database
	// package's own connections capture their changes: turso captures per
	// connection, so a pool opened before this call would go on writing rows the
	// push cannot see.
	db.MarkSyncMember(cfg.Path)
	return NewTurso(handle), nil
}
