package relevo

import (
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainCheckPin is the CPU set a chain check runs on: its builder member's
// retained set, or nothing when the member is missing or remote. A remote
// member's tree is not on this host, so no local pin applies.
func chainCheckPin(tx *store.Tx, c db.ChainRow) string {
	member := chainCheckMember(c)
	if member == "" {
		return ""
	}
	b, err := tx.Load(member)
	if err != nil || b.Builder.Remote() {
		return ""
	}
	return housekeep.RoundPinText(b)
}

// chainCheckEnsureSet gives the check's member a CPU set when it has none,
// saving it on the member: a member that never pinned (scopes off, a pool too
// small, never started on this host) still runs its check pinned, so the
// census derives from the one stored field. A missing or remote member pins
// nothing.
func chainCheckEnsureSet(rt Runtime, tx *store.Tx, c db.ChainRow) error {
	member := chainCheckMember(c)
	if member == "" {
		return nil
	}
	b, err := tx.Load(member)
	if err != nil || b.Builder.Remote() {
		return nil
	}
	if len(b.RoundCPUs) > 0 {
		return nil
	}
	return tx.Save(housekeep.AssignRoundCPU(HousekeepDeps(rt), tx, b))
}
