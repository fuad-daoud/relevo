//go:build unix

package owner

import "github.com/fuad-daoud/relevo/internal/db/wire"

// The scope of one connection, decided once by its handshake.
//
// An owner serves two files -- the shared database, and the machine-local one
// beside it -- so which file a request reaches is the first thing a connection
// has to settle. It settles it here, from the hello, and never changes it: a
// connection that started on one file and continued on the other would put half
// a transaction in each, which is exactly the split the owner exists to keep.

// claimScope fixes which of the owner's files this connection speaks to.
//
// A client asking for the local file against an owner that serves none is
// refused rather than answered from the shared file. A silent downgrade there
// would hand the caller the very rows the split keeps off the shared file --
// a token, a sync marker -- under the name it asked for, which is the one
// outcome the split exists to prevent. Any other scope, including none at all,
// is served the shared file, so a client predating the field behaves exactly as
// it did before the scope existed.
func (c *conn) claimScope(scope string) error {
	if scope != wire.ScopeLocal {
		return nil
	}
	if !c.s.hasLocal() {
		return c.refuse(wire.RefuseNoLocal, "this owner serves no machine-local file")
	}
	c.local = true
	return nil
}
