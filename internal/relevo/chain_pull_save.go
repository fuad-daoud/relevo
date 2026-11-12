package relevo

import (
	"reflect"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// clearSettledPollFacts drops the poll's queue and live facts once the round
// has left the queue and stopped running. A queued or running round's facts are
// the poll's: the chain view carries neither, so clearing them here would undo
// the poll's write on every tick and resync the shared row.
func clearSettledPollFacts(b *store.Binding, state remote.RoundState) {
	if state == remote.RoundQueued || state == remote.RoundRunning {
		return
	}
	b.Builder.RemoteQueue = nil
	b.Builder.RemoteLive = nil
}

// chainPullSaveIfChanged saves a pull's member only when the pass moved it:
// an unchanged record is dropped rather than rewritten with a new stamp, so a
// repeated pass over an unchanged view writes nothing. The snapshot and the
// mutable copy are compared with the UpdatedAt stamp Save writes dropped on
// both sides, so any moved field still saves. Save is handed the stamp the record
// holds: its own check compares the whole row, so a blank stamp reads as a change
// and rewrites a row in which only machine-local fields moved.
func chainPullSaveIfChanged(tx *store.Tx, snap, cur store.Binding) error {
	compared := cur
	snap.UpdatedAt = time.Time{}
	compared.UpdatedAt = time.Time{}
	if reflect.DeepEqual(snap, compared) {
		return nil
	}
	return tx.Save(cur)
}

// chainPullPutIfChanged is chainPullSaveIfChanged for the chain row the pull
// rebuilds from the view.
func chainPullPutIfChanged(tx *store.Tx, snap, row db.ChainRow) error {
	snap.UpdatedAt = time.Time{}
	row.UpdatedAt = time.Time{}
	if reflect.DeepEqual(snap, row) {
		return nil
	}
	return tx.ChainPut(row)
}
