package relevo

import (
	"os"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

// roundEndVerdict is what one exited round's end lands on: the close that
// finishes it, or the one-shot resume that hands it back. Unmarked always
// means no report and no marker; those two close before the verdict is asked.
type roundEndVerdict string

const (
	roundEndReport roundEndVerdict = "report"  // the report is the contract, whatever else is true
	roundEndMarker roundEndVerdict = "marker"  // marker without report: the noreport close
	roundEndNudge  roundEndVerdict = "nudge"   // one resume of the session the turn left
	roundEndFail   roundEndVerdict = "failure" // the runner-failure path, nudge skipped
)

// roundEndCase is one exited round's facts, all of them live reads at the
// tick that decides.
type roundEndCase struct {
	TurnEnd transcript.TurnEnd
	Code    string // the exit code as text, "unknown" when no trailer named one
	Report  bool
	Marker  bool
}

// classifyRoundEnd pins the end of an exited round with no usable work. An
// unclean verdict needs positive evidence, so an unreadable or empty stream
// never earns one and keeps the nudge path.
func classifyRoundEnd(c roundEndCase) roundEndVerdict {
	switch {
	case c.Report:
		return roundEndReport
	case c.Marker:
		return roundEndMarker
	}
	if c.TurnEnd == transcript.TurnEndUnclean {
		return roundEndFail
	}
	if c.Code == "0" {
		return roundEndNudge
	}
	return roundEndFail
}

// roundTurnEnd is one exited round's turn-end read: the round's stream run
// against its harness kind. A stream that never appeared, or one this tick
// cannot read, says nothing rather than failing the exit it observes.
func roundTurnEnd(rt Runtime, b store.Binding) transcript.TurnEnd {
	stream, err := os.ReadFile(rt.Store.StreamPath(b.Name, b.Round))
	if err != nil {
		return transcript.TurnEndUnknown
	}
	return transcript.TurnEndState(b.Builder.Kind, stream)
}
