package relevo

import (
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

const resumedIdleNotePrefix = "resumed session idle:"

// noteResumedIdle logs one line when a resumed session's exploring label
// turns on, so the round's report shows the idle stretch once per episode.
func noteResumedIdle(tx *store.Tx, before, after store.Binding, now time.Time) error {
	if !before.ExploringSince.IsZero() || after.ExploringSince.IsZero() ||
		after.Progress == nil || after.Progress.ResumedAt.IsZero() {
		return nil
	}
	return tx.AppendLog(after.Name, store.LogEntry{
		TS: now, Round: after.Round, Direction: store.DirToMasterMind, Kind: store.KindSwitch, Confirmed: true,
		Note: fmt.Sprintf("%s worktree unchanged for %s since the resume",
			resumedIdleNotePrefix, view.AgeText(now.Sub(after.Progress.TreeAt))),
	})
}
