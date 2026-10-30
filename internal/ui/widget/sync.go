package widget

import (
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// SyncSnapshot is the whole read side of the :sync view: what the machine is
// set to be, what the remote last reported, and which rows are waiting on a
// human. Every field is a value already read; nothing here can open a handle.
//
// The zero Snapshot is what a view renders when no read has answered yet, and
// it is also what a read that found nothing describes: sync off, no remote, no
// token, no measurement, one installation (this one) and nothing unlinked.
type SyncSnapshot struct {
	// State is the machine-local state the statusline token is derived from,
	// and Token is that derivation itself -- the view renders S2's mapping and
	// does not repeat its order.
	State relevosync.State
	Token string
	// Settings is the machine-local sync section: where the remote is and how
	// eager this machine is.
	Settings relevosync.Settings
	// TokenSet is whether a token is stored. The value is never here.
	TokenSet bool
	// Measured says whether a completed attempt has ever stamped the exchange
	// times. A machine that has measured nothing shows placeholders rather than
	// zeros, because a zero backlog and no backlog read identically and mean
	// opposite things.
	Measured bool
	// Attention is the message the attention marker carries, when one does.
	Attention string
	// LastExport and LastImport are when the steady pipeline last finished each
	// half of the exchange, zero when it never has.
	LastExport time.Time
	LastImport time.Time
	// Trouble is what the last import reported besides applied entries: an
	// origin a newer writer held, a batch a refusal dropped, a sequence gap.
	Trouble relevosync.Trouble
	// RemoteURL is the configured remote, empty when this machine has none.
	RemoteURL string
	// Installation is the directory: one row per installation that has written
	// here, and OwnID is this machine's own id, so a row that is not this
	// machine's can be shown as read-only attribution.
	Installation []db.Installation
	OwnID        string
	// Unlinked is every remote binding row carrying no link, listed for
	// re-bind. Nothing backfills them: they are here until someone binds them.
	Unlinked []db.Record
	// SharedBytes is the size of the shared file on disk, which is what the
	// first-upload estimate is a multiple of.
	SharedBytes int64
	// Reachable is whether the last test connection reached the remote, and
	// Unknown says no test has run yet. The two are separate because "never
	// tried" and "tried and failed" are different states and one chip cannot
	// say both.
	Reachable bool
	Unknown   bool
}
