package housekeep

import "context"

// Deps carries the engine-owned dependencies housekeeping code needs, so the
// engine passes them explicitly instead of the package reaching back into it.
type Deps struct{}

// Prefixer is the narrow git surface ResolveSubdir needs: the repository
// prefix of the directory a bind started from. The engine's git client
// satisfies it.
type Prefixer interface {
	ShowPrefix(ctx context.Context, dir string) (string, error)
}
