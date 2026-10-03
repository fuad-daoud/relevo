package relevo

import (
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

// readerDeliverablePresent reports whether a reader round has produced its
// deliverable: either a regular file the runner wrote itself at reportPathFor,
// or a stream message carrying a relevo block. A length heuristic is never
// used: narration sentences and DSML text do not count as deliverables.
func readerDeliverablePresent(rt Runtime, b store.Binding) bool {
	if b.Shape != store.ShapeReader {
		return false
	}
	if fi, err := os.Lstat(reportPathFor(rt, b)); err == nil && fi.Mode().IsRegular() {
		return true
	}
	stream, _ := rt.Store.ReadFile(rt.Store.StreamPath(b.Name, b.Round))
	return transcript.LastWithBlock(lastStreamKind(b), stream) != ""
}

// readerUndeliveredReason builds the halt reason for a reader that ended
// without its deliverable. It names the binding, the output label, the
// continuation count and the show command to inspect the transcript.
func readerUndeliveredReason(rt Runtime, b store.Binding, continuations int) string {
	label := readerOutputLabel(rt, b)
	unit := "continuations"
	if continuations == 1 {
		unit = "continuation"
	}
	return fmt.Sprintf("%s: %s not delivered: runner ended without its relevo block after %d %s; see %s",
		b.Name, label, continuations, unit, showCommand(b.Name, b.Round, "transcript"))
}
