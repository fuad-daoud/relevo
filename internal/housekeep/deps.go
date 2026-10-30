package housekeep

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/store"
)

// Deps carries the engine-owned dependencies housekeeping code needs, so the
// engine passes them explicitly instead of the package reaching back into it.
type Deps struct {
	// Store confines every temp entry under TmpRoot, so a planted name or
	// link never escapes the root.
	Store *store.Store
	// TmpRoot is the state directory whose per-process entries the rounds and
	// checks create. Empty means no per-process dirs: callers remove nothing.
	TmpRoot string
}

// Prefixer is the narrow git surface ResolveSubdir needs: the repository
// prefix of the directory a bind started from. The engine's git client
// satisfies it.
type Prefixer interface {
	ShowPrefix(ctx context.Context, dir string) (string, error)
}
