package relevo

import (
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// chainPullQueueEntry records the queue wait a pulled round had on its server,
// so the mirror's own history shows it.
//
// A round the server never queued ships no stamp and writes nothing: no wait
// to record, and an entry saying so would state a fact the server never gave.
//
// The round's prompt entry is written first, so this runs after it: a queue
// record with no prompt beside it reads as a round nobody was handed.
func chainPullQueueEntry(rt Runtime, tx *store.Tx, b store.Binding, r int, facts remote.ClosedRoundView) error {
	if facts.QueuedAt.IsZero() || facts.RoundStartedAt.IsZero() {
		return nil
	}
	entries, err := tx.ReadLog(b.Name)
	if err != nil {
		return err
	}
	if store.HasKind(entries, r, store.DirToMasterMind, store.KindQueue) {
		return nil
	}
	wait := facts.RoundStartedAt.Sub(facts.QueuedAt).Round(time.Second)
	if wait < 0 {
		wait = 0
	}
	server := b.Builder.Server
	note := fmt.Sprintf("queued on %s (%s in the queue)", server, view.AgeText(wait))
	return tx.AppendLog(b.Name, store.LogEntry{
		TS: facts.QueuedAt.UTC(), Round: r, Direction: store.DirToMasterMind,
		Kind: store.KindQueue, Note: note, Confirmed: true,
	})
}
