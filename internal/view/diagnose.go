package view

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/store"
)

// BuilderDiagnosis explains a broken binding: what was at stake when the
// builder went away.
//
// store.StateBroken is documented as "the builder process is gone". It
// covers two situations relevo does not otherwise distinguish -- a builder
// that exited after a clean round, and one that exited mid-round. The first
// needs no action; the second leaves work unaccounted for.
type BuilderDiagnosis struct {
	// RoundOpen reports that a round was handed to the builder and no report
	// came back, so that round's work is unaccounted for.
	RoundOpen bool
	// Reader reports that the binding's actor only leaves artifacts, so the
	// closed-round sentence names the neutral output word.
	Reader bool
}

// DiagnoseBuilder derives the diagnosis for a binding. Pure.
//
// RoundStartedAt is stamped only by Send at handoff and cleared only once the
// round's report is logged, so a zero value means no round is in flight.
func DiagnoseBuilder(b store.Binding) BuilderDiagnosis {
	return BuilderDiagnosis{
		RoundOpen: !b.RoundStartedAt.IsZero(),
		Reader:    b.Shape == store.ShapeReader,
	}
}

// Detail renders the sentence `relevo status` shows beneath a broken binding.
//
// round is the binding's current round. queueReport increments Round after
// logging a report, so a closed round's report belongs to round-1 -- and a
// binding that has never been sent has no delivered report to name at all.
func (d BuilderDiagnosis) Detail(round int) string {
	switch {
	case d.RoundOpen:
		return fmt.Sprintf("round %d was open -- that work is unaccounted for; rebind and resend the round", round)
	case round > 1:
		word := "report"
		if d.Reader {
			word = "output"
		}
		return fmt.Sprintf("round %d %s delivered; nothing outstanding -- unless you want another round", round-1, word)
	default:
		return "no round has been sent yet; nothing outstanding -- unless you want to send one"
	}
}
