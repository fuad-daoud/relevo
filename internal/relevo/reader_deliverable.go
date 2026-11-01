package relevo

import (
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/text"
	"github.com/fuad-daoud/relevo/internal/transcript"
	"github.com/fuad-daoud/relevo/internal/view"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// readerDeliverablePresent reports whether a reader round has produced its
// deliverable: either a regular file with content in it that the runner wrote
// itself at reportPathFor, or a stream message carrying a relevo block. A
// length heuristic is never used: narration sentences and DSML text do not
// count as deliverables, and neither does an output file that is empty, which
// the close would fall back to the stream for.
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
	// A chain member's close keeps the file-or-block rule: the chain engine
	// halts an undelivered member through the outcome parse, while the rule
	// below would strand the chain running behind an exited-without-report
	// binding halt.
	if _, merr := rt.Store.ChainByMember(b.Name); merr == nil {
		if present, empty := view.ReaderOutputPresent(reportPathFor(rt, b)); present && !empty {
			return true
		}
		stream, _ := rt.Store.ReadFile(rt.Store.StreamPath(b.Name, b.Round))
		return transcript.LastWithBlock(view.LastStreamKind(b), stream) != ""
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

// readerPlanBody is the plan a plan-writing reader round produced, read the way
// the close reads it: the file the runner wrote itself when it holds content,
// else the stream's last message.
func readerPlanBody(rt Runtime, b store.Binding) []byte {
	path := reportPathFor(rt, b)
	if present, empty := view.ReaderOutputPresent(path); present && !empty {
		if body, err := os.ReadFile(path); err == nil {
			return body
		}
	}
	stream, _ := rt.Store.ReadFile(rt.Store.StreamPath(b.Name, b.Round))
	texts := transcript.Texts(view.LastStreamKind(b), stream)
	if len(texts) == 0 {
		return nil
	}
	return []byte(texts[len(texts)-1])
}

// readerPlanNudgeDue reports whether an exited plan-writing reader owes its one
// continuation: it ended cleanly with a session to resume and an output that
// holds no plan steps, and the prompt has not been nudged yet.
func readerPlanNudgeDue(rt Runtime, b store.Binding, entries []store.LogEntry, codeText string) bool {
	if b.Shape != store.ShapeReader || codeText != "0" || b.Builder.StreamSessionID == "" {
		return false
	}
	if nudgesSincePlan(entries, b.Round) >= planNudgeLimit || !memberWritesPlan(rt, b) {
		return false
	}
	return !planHasSteps(readerPlanBody(rt, b))
}

// readerCandidateBodies returns the bodies a reader round's deliverable is read
// from: the file the runner wrote itself at the output path, when it is a
// regular file with content in it, and the stream's last block-carrying message
// (when present). An empty file is not a body -- the close falls back to the
// stream for it. A message that carries no block at all is not a candidate
// either: narration is not one.
func readerCandidateBodies(rt Runtime, b store.Binding) [][]byte {
	var bodies [][]byte
	path := reportPathFor(rt, b)
	if present, empty := view.ReaderOutputPresent(path); present && !empty {
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
	if text := transcript.LastWithBlock(view.LastStreamKind(b), stream); text != "" {
		return text
	}
	texts := transcript.Texts(view.LastStreamKind(b), stream)
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
	info, ok := rt.RoleRegistry().ActorInfo(b.WriterRole())
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

func readerUndeliveredReason(rt Runtime, b store.Binding, continuations int) string {
	label := readerOutputLabel(rt, b)
	unit := "continuations"
	if continuations == 1 {
		unit = "continuation"
	}
	return fmt.Sprintf("%s: %s not delivered: runner ended without its relevo block after %d %s; see %s",
		b.Name, label, continuations, unit, text.ShowCommand(b.Name, b.Round, "transcript"))
}

// readerStreamTail reads the status block from a non-chain reader's final
// message when the output file it wrote carries none: the file is the artifact
// and the block closes the message, so the outcome is read from where the
// prompt told the reader to put it.
func readerStreamTail(rt Runtime, b store.Binding) (reporttail.Tail, bool) {
	if b.Shape != store.ShapeReader {
		return reporttail.Tail{}, false
	}
	text := readerBlockText(rt, b)
	if text == "" {
		return reporttail.Tail{}, false
	}
	tail, ok, _ := reporttail.ParseWithReason([]byte(text))
	return tail, ok
}
