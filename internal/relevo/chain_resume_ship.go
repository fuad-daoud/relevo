package relevo

import (
	"context"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
)

// shipResumedRound hands a resumed chain's staged round to its remote awaited
// member. A resume stages a remote member's round, so the unlocked pending-send
// step ships it outside the state lock, exactly as a start ships its plan 1; a
// failed ship records the halt and returns nil. A local awaited member, and a
// step whose awaited run names no member, have nothing to ship.
func shipResumedRound(ctx context.Context, rt Runtime, name string, c db.ChainRow) error {
	member, _, ok := chainAwaited(rt.Store, c)
	if !ok {
		return nil
	}
	b, err := rt.Store.Load(member)
	if err != nil || !b.Builder.Remote() {
		return nil
	}
	if err := chainSendPending(ctx, rt); err != nil {
		return fmt.Errorf("chain %s resumed, but its remote member could not be handed its round: %w", name, err)
	}
	return nil
}
