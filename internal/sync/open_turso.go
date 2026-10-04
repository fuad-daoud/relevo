//go:build !modernc

package sync

import (
	"context"
	"fmt"

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
// The token is converted to a string because that is the driver's field type.
// It goes into the config and into nothing else: no error on this path formats
// the config, so a driver error cannot carry the value out with it.
func OpenRemote(ctx context.Context, cfg OpenConfig) (SyncClient, error) {
	bootstrap := cfg.BootstrapIfEmpty
	handle, err := turso.NewTursoSyncDb(ctx, turso.TursoSyncDbConfig{
		Path:             cfg.Path,
		RemoteUrl:        cfg.RemoteURL,
		Namespace:        cfg.Namespace,
		AuthToken:        string(cfg.AuthToken),
		ClientName:       cfg.ClientName,
		BootstrapIfEmpty: &bootstrap,
	})
	if err != nil {
		return nil, fmt.Errorf("sync: open the remote: %w", err)
	}
	return NewTurso(handle), nil
}
