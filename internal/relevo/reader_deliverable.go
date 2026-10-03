package relevo

import (
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/transcript"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// readerDeliverablePresent reports whether a reader round has produced its
// deliverable: either a regular file the runner wrote itself at reportPathFor,
// or a stream message carrying a relevo block. A length heuristic is never
// used: narration sentences and DSML text do not count as deliverables.
//
// For an actor that declares outcome keys, presence is not enough: the body
// must carry a value one of them names, read with the same reader the outcome
// parse uses, so a block that carries nothing the round declares cannot be
// closed as delivered. A block-carrying message whose fences were lost is read
// the same way, so losing the fences is not by itself an undelivered round.
func readerDeliverablePresent(rt Runtime, b store.Binding) bool {
	if b.Shape != store.ShapeReader {
		return false
	}
	// A chain member's close keeps the historic file-or-block rule. The
	// chain engine halts an undelivered member through the outcome parse,
	// while the tightened rule below would strand the chain running behind
	// an exited-without-report binding halt, which is strictly worse.
	if _, merr := rt.Store.ChainByMember(b.Name); merr == nil {
		if fi, ferr := os.Lstat(reportPathFor(rt, b)); ferr == nil && fi.Mode().IsRegular() {
			return true
		}
		stream, _ := rt.Store.ReadFile(rt.Store.StreamPath(b.Name, b.Round))
		return transcript.LastWithBlock(lastStreamKind(b), stream) != ""
	}
	bodies := readerCandidateBodies(rt, b)
	if len(bodies) == 0 {
		return false
	}
	outs := readerDeclaredOutcomes(rt, b)
	if len(outs.Outcomes()) == 0 {
		return true
	}
	for _, body := range bodies {
		if readerCarriesOutcome(outs, body) {
			return true
		}
	}
	return false
}

// readerCandidateBodies returns the bodies a reader round's deliverable is read
// from: the regular file the runner wrote itself at the output path (when present),
// and the stream's last block-carrying message (when present). A message that
// carries no block at all is not a candidate -- narration is not one.
func readerCandidateBodies(rt Runtime, b store.Binding) [][]byte {
	var bodies [][]byte
	path := reportPathFor(rt, b)
	if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() {
		if body, rerr := os.ReadFile(path); rerr == nil {
			bodies = append(bodies, body)
		}
	}
	if text := readerBlockText(rt, b); text != "" {
		bodies = append(bodies, []byte(text))
	}
	return bodies
}

// readerBlockText is the stream message a reader round's block is read from:
// the last one carrying a fenced `relevo` block, else the last one carrying a
// bare `relevo` line whose fences were lost. The fenced message wins, so a
// recap after a fenced block cannot hide it, and the tolerance sits behind the
// fence exactly as it does in the outcome parse.
func readerBlockText(rt Runtime, b store.Binding) string {
	stream, _ := rt.Store.ReadFile(rt.Store.StreamPath(b.Name, b.Round))
	if text := transcript.LastWithBlock(lastStreamKind(b), stream); text != "" {
		return text
	}
	texts := transcript.Texts(lastStreamKind(b), stream)
	for i := len(texts) - 1; i >= 0; i-- {
		if reporttail.HasFencelessBlock([]byte(texts[i])) {
			return texts[i]
		}
	}
	return ""
}

// readerDeclaredOutcomes is the actor's declared output keys, nil when the
// actor is unknown. It is the same declaration the outcome parse validates
// against, so the two cannot disagree about what the round owes.
func readerDeclaredOutcomes(rt Runtime, b store.Binding) workflow.Outputs {
	info, ok := rt.RoleRegistry().ActorInfo(bindingRole(b))
	if !ok {
		return nil
	}
	return info.Outputs
}

// readerCarriesOutcome reports whether body names a value of one of the actor's
// declared outcomes. An actor that declares no outcome keeps the presence-only
// rule: it has no value the deliverable could be required to carry.
func readerCarriesOutcome(outs workflow.Outputs, body []byte) bool {
	keys := outs.Outcomes()
	if len(keys) == 0 {
		return true
	}
	for _, key := range keys {
		if _, ok := reporttail.BlockValue(body, key); ok {
			return true
		}
	}
	return false
}

// readerLostFences reports whether the round's body opens a block with a bare
// `relevo` line that names no readable value: the fences were lost, and only
// the reader can put them back, so the continuation says exactly that.
func readerLostFences(rt Runtime, b store.Binding) bool {
	outs := readerDeclaredOutcomes(rt, b)
	if len(outs.Outcomes()) == 0 {
		return false
	}
	for _, body := range readerCandidateBodies(rt, b) {
		if reporttail.HasFencelessBlock(body) && !readerCarriesOutcome(outs, body) {
			return true
		}
	}
	return false
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
