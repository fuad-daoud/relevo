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
	Store        *store.Store
	Candidates   *candidate.Set
	Gates        db.KV
	Latency      db.KV
	Now          func() time.Time
	Roles        harness.RoleChecker
	RoleRegistry func() *roles.Registry
	Accounts     account.Set
}
