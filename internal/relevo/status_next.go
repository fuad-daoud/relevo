package relevo

import (
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// setNextInputs fills the two row facts the statusline's next move needs from
// the log in hand: the newest to-mastermind question's first line, and the
// staged prompt path of the round the newest payload belongs to.
func setNextInputs(rt Runtime, row *view.BindingStatus, entries []store.LogEntry) {
	p := row.LastPayload
	if p == nil || p.Direction != store.DirToMasterMind {
		return
	}
	switch p.Kind {
	case store.KindQuestion:
		row.Question = questionFirstLine(rt)(row.Name, p.Round)
	case store.KindReport:
		for i := len(entries) - 1; i >= 0; i-- {
			if e := entries[i]; store.IsPromptKind(e.Kind) && e.Round == p.Round && e.Path != "" {
				row.PromptPath = e.Path
				return
			}
		}
	}
}
