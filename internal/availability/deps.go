package availability

import (
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// Deps is what the gate, probe and limit code reads from its caller: the store
// that serialises writes, the configured candidates, the gate and latency
// records, the clock, the roles checker and the account pools. A nil field
// means that facility is not configured and reads as empty.
type Deps struct {
	Store      *store.Store
	Candidates *candidate.Set
	// Accounts is the configured login pool. Empty on every host with no
	// accounts, where a gate is a bare group and nothing here changes.
	Accounts     account.Set
	Gates        db.KV
	Latency      db.KV
	Now          func() time.Time
	Roles        harness.RoleChecker
	RoleRegistry func() *roles.Registry
	// Local is the machine-local file the gates document records its write
	// stamps and its export signal in, when the ledger is bound to that file
	// itself. Nil when it is not: the serve-wide ledger lives under its own
	// prefix beside the server's other records and is nobody's gates document,
	// so a write there is stamped nowhere and signals nothing.
	Local *db.DB
	// SyncNow asks for an attempt now rather than at the end of the export
	// debounce, because a gate a person just recorded is worth the other
	// machines seeing in seconds rather than in a quarter of a minute. Nil is
	// the normal case for a caller with no daemon behind it: the gate is written
	// and the next ordinary trigger exports it.
	SyncNow func()
}
