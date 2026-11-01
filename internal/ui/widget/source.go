package widget

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/view"
)

// Source is everything the ui reads state through.
type Source interface {
	Status(ctx context.Context) (view.Report, error)
	// Runtime resolves a row key to the runtime that owns it and the bare
	// binding name inside that runtime's store. ok is false when the key
	// cannot be resolved (server: malformed key or unknown owner); a
	// mastermind source resolves every key.
	Runtime(key string) (rt relevo.Runtime, name string, ok bool)
	// Base is the runtime for fleet-wide reads that are not per row: the
	// database behind scope all and a hist row's tabs. On a mastermind it is
	// the mastermind's own runtime. On the server it carries no DB, so scope
	// all is refused there with the existing "no database" notice.
	Base() relevo.Runtime
	// MarkViewed stamps key's viewed mark. A mastermind source writes through
	// its own store; a server source is a no-op -- the mark is a record field
	// on whichever client machine holds the binding, and a server never writes
	// into a client's database. Errors are swallowed: a stamp must never fail
	// a read-only screen.
	MarkViewed(key string)
}
