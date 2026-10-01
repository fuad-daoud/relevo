package relevo

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainCloseWF is the close of a workflow chain's member round: the facts the
// workflow engine needs to build the step_closed its state awaits.
type chainCloseWF struct {
	Body    []byte
	Path    string
	Outcome string
	Stopped bool
	// Note is the close's own note from the runner, folded into a non-done
	// builder's one-line reason when its report names no source.
	Note string
	// Round is the round that closed. The close's caller advances the binding
	// before the chain moves, so the closing binding's own round is no longer
	// the closed one by then; the event must name the round the step awaited.
	Round int
}

// chainEventFromCloseWF maps one member close onto the workflow event the
// engine reads. The step and member come from the chain state's Awaiting; the
// closing binding's actor comes from its chain_member row. A stopped close is
// the engine's stopped event. Otherwise it parses the actor's declared
// outcomes, keys its artifacts and closes the step halted when a declared
// artifact is missing.
func chainEventFromCloseWF(rt Runtime, tx *store.Tx, c db.ChainRow, b store.Binding, wf chainCloseWF) (workflow.Event, error) {
	st, err := chainWorkflowState(c)
	if err != nil {
		return workflow.Event{}, err
	}
	actor := chainFlowActor(tx, c, b.Name)
	if actor == "" {
		actor = BindingRole(b)
	}
	round := wf.Round
	if round == 0 {
		round = b.Round
	}
	ev := workflow.Event{Step: st.Awaiting.Step, Member: actor, Round: round}
	if wf.Stopped {
		ev.Kind = workflow.EventStopped
		return ev, nil
	}
	ev.Kind = workflow.EventStepClosed

	info, _ := rt.RoleRegistry().ActorInfo(actor)
	outs := info.Outputs
	bodies := chainOutcomeBodies(rt, b, round, wf.Body)
	values, reason := workflow.ParseOutcomes(outs, bodies...)
	if reason != "" {
		ev.Status = reporttail.OutcomeHalted
		ev.Reason = reason
		return ev, nil
	}
	ev.Outcomes = values
	// A writer's report status is the round's own outcome. A reader's output is
	// a summary whose block the close already stripped, so its saved outcome
	// says nothing about the step: the reader routes on the status its own
	// block carries -- done when it carries outcomes without one.
	if b.Shape == store.ShapeReader {
		ev.Status = chainReaderStatus(bodies, outs)
	} else {
		ev.Status = wf.Outcome
		// A builder close that is not done carries why in one line from its
		// report tail, under the plan the chain halted on; the caller-rendered
		// wording is what the engine's unmatched-run halt uses verbatim.
		if wf.Outcome != reporttail.OutcomeDone {
			tail, _, _ := reporttail.ParseWithReason(wf.Body)
			ev.Reason = chain.BuilderHaltReason(tail, wf.Note, wf.Outcome)
			ev.HaltReason = fmt.Sprintf("builder halted on plan %d: %s", c.Plan, ev.Reason)
		}
	}

	artifacts := chainCloseArtifacts(rt, b, wf.Path, outs)
	if miss := workflow.MissingArtifact(outs, chainArtifactSizes(rt, artifacts)); miss != "" {
		ev.Status = reporttail.OutcomeHalted
		ev.Reason = miss
		return ev, nil
	}
	ev.Artifacts = artifacts
	return ev, nil
}

// chainCloseArtifacts keys a closing round's artifacts: a writer exposes its
// report and its round diff; a reader exposes its saved output, plus every
// artifact the actor declares, all pointing at the saved output.
func chainCloseArtifacts(rt Runtime, b store.Binding, path string, outs workflow.Outputs) map[string][]string {
	art := map[string][]string{}
	if b.Shape == store.ShapeReader {
		if path != "" {
			art["output"] = []string{path}
			for _, name := range outs.Artifacts() {
				art[name] = []string{path}
			}
		}
		return art
	}
	if path != "" {
		art["report"] = []string{path}
	}
	art["diff"] = []string{rt.Store.DiffPath(b.Name, b.Round)}
	return art
}

// chainArtifactSizes measures every artifact key a close produced, so a
// declared artifact that is missing or empty closes the step halted.
func chainArtifactSizes(rt Runtime, art map[string][]string) map[string]int64 {
	sizes := make(map[string]int64, len(art))
	for name, keys := range art {
		var total int64
		for _, key := range keys {
			if s, _, ok, err := rt.Store.StatFile(key); err == nil && ok {
				total += s
			} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
				continue
			}
		}
		sizes[name] = total
	}
	return sizes
}
