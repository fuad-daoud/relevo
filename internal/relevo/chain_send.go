package relevo

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/capture"
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// RoundOpenError is returned when a chain member's round is still open: its
// prompt entry exists with no report entry, so a new round would overwrite the
// one in flight. It carries the member to stop, so the CLI names
// `relevo stop <member>` instead of an internal failure.
type RoundOpenError struct {
	Member string
	Round  int
}

// Error renders the message: the round number and the stop command the
// member's own name completes.
func (e *RoundOpenError) Error() string {
	return fmt.Sprintf("%s: round %d is still open; relevo stop %s ends it", e.Member, e.Round, e.Member)
}

// sendChainRound starts one round for a chain member. It is Send's in-lock
// core as a reusable helper: the caller holds the state lock and passes its
// tx, and the member's new binding and prompt entry are written in the same
// critical section as the naming chain row.
//
// It requests no verify: a chain member round is always verify-free, because
// the chain's own reviewer replaces the verify consult.
//
// A startRound failure leaves the member NEEDS YOU, as Send's spawn failure
// does, and the error returns wrapped with the member's name; the caller then
// halts the chain in the same critical section.
func sendChainRound(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, text string) (store.Binding, error) {
	cur, err := guardSendChainRound(ctx, rt, tx, b)
	if err != nil {
		return b, err
	}
	var prompt, baseline, baselineHead, repoBaseline string
	var pending []store.LogEntry
	cur, prompt, baseline, baselineHead, repoBaseline, pending, err = stageSendChainPlan(ctx, rt, tx, cur, text)
	if err != nil {
		return b, err
	}
	cur, err = startOrQueueSendChainRound(ctx, rt, tx, b, cur, prompt, baseline, baselineHead, repoBaseline, pending)
	if err != nil {
		return b, err
	}
	return cur, nil
}

// guardSendChainRound checks the member can open its round: the binding
// loads, no round is open, no report is pending, and no previous process is
// still alive.
func guardSendChainRound(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, error) {
	name := b.Name
	cur, err := tx.Load(name)
	if err != nil {
		return b, fmt.Errorf("%s: %w", name, err)
	}

	entries, err := tx.ReadLog(name)
	if err != nil {
		return b, fmt.Errorf("%s: %w", name, err)
	}
	if roundOpenIn(entries, cur.Round) {
		return b, &RoundOpenError{Member: name, Round: cur.Round}
	}
	if path, found := pendingRoundFile(rt, name, cur.Round); found {
		return b, fmt.Errorf("%s: round %d: %s exists: %w", name, cur.Round, filepath.Base(path), ErrReportPending)
	}
	if cur.Builder.PID != 0 {
		if rt.Runner == nil {
			return b, fmt.Errorf("%s: %w", name, spawn.ErrRunnerUnavailable)
		}
		alive, err := rt.Runner.Alive(ctx, handleOf(cur.Builder))
		if err != nil {
			return b, fmt.Errorf("%s: check previous process %d: %w", name, cur.Builder.PID, err)
		}
		if alive {
			return b, fmt.Errorf("%s (pid %d): %w", name, cur.Builder.PID, ErrBuilderBusy)
		}
	}
	return cur, nil
}

// stageSendChainPlan stages the round's plan file and captures its baselines:
// the worktree tree, its head, and the repository baseline the round diffs
// against.
func stageSendChainPlan(ctx context.Context, rt Runtime, tx *store.Tx, cur store.Binding, text string) (store.Binding, string, string, string, string, []store.LogEntry, error) {
	name := cur.Name
	planPath := rt.Store.PromptPath(name, cur.Round)
	reportPath := rt.Store.ReportPath(name, cur.Round)
	donePath := rt.Store.DonePath(name, cur.Round)
	if err := StagePlan(planPath, []byte(text)); err != nil {
		return cur, "", "", "", "", nil, fmt.Errorf("%s: stage plan at %s: %w", name, planPath, err)
	}

	baseline, baselineHead := capture.Baseline(ctx, captureDeps(rt), cur)
	repoBaseline := escapeRepoBaseline(ctx, rt, cur, baseline)
	prompt := roundPrompt(rt, tx, cur, planPath, reportPath, donePath)

	var pending []store.LogEntry
	next, res, _, err := repickForRound(rt, cur, false, false)
	if err != nil {
		return cur, "", "", "", "", nil, fmt.Errorf("%s: %w", name, err)
	}
	if res != nil {
		pending = append(pending, resolve.PickEntry(rt.Now().UTC(), next.Round, next.WriterRole(), *res))
	}
	return next, prompt, baseline, baselineHead, repoBaseline, pending, nil
}

// startOrQueueSendChainRound starts the staged round, or queues it when the
// member is served: like Send(Defer) it records the round as queued and lets
// the server's admit start it, so serve.max_builders and the tier cap decide.
func startOrQueueSendChainRound(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, cur store.Binding, prompt, baseline, baselineHead, repoBaseline string, pending []store.LogEntry) (store.Binding, error) {
	name := b.Name
	planPath := rt.Store.PromptPath(cur.Name, cur.Round)
	served := cur.Owner != ""
	if !served {
		// A reader round runs in a throwaway scratch worktree, never in b.CWD:
		// create it from the round's captured baseline before spawning, as
		// Send does.
		if cur.Shape == store.ShapeReader {
			if _, err := housekeep.CreateScratchFrom(ctx, HousekeepDeps(rt), cur, cur.Round, baselineHead, baseline); err != nil {
				return b, fmt.Errorf("%s: %w", name, err)
			}
		}

		started, err := startRound(ctx, rt, tx, cur, prompt, false)
		if err != nil {
			// The plan is staged and the round is open; nothing was started.
			// NEEDS YOU says so in status, as Send's spawn failure does.
			cur.State = store.StateNeedsYou
			cur.Halt = spawnHaltText(err)
			cur.HaltAt = rt.Now().UTC()
			if saveErr := tx.SaveWithLog(cur, pending...); saveErr != nil {
				return b, fmt.Errorf("%s: %w; saving NEEDS YOU failed: %w", name, err, saveErr)
			}
			return b, fmt.Errorf("%s: %w", name, err)
		}
		cur = started
	}

	pending = append(pending, store.LogEntry{
		TS: rt.Now().UTC(), Round: cur.Round,
		Direction: store.DirToBuilder, Kind: store.KindPrompt,
		Path: planPath, Confirmed: true,
		Tier: string(resolve.EffectiveTier(cur)),
		Note: chain.StepNote(tx, cur.Name),
	})

	cur.RoundBaselineTree = baseline
	cur.RoundBaselineHead = baselineHead
	cur.RoundRepoBaselineTree = repoBaseline
	cur.RoundClosedTree = ""
	if served {
		cur.QueuedAt = rt.Now().UTC()
	} else {
		cur.RoundStartedAt = rt.Now().UTC()
	}
	cur.FinishPending = true
	cur.State = store.StateActive
	// Verify off: the chain's reviewer replaces the verify consult on every
	// member round.
	cur.RoundVerify = false
	if err := tx.SaveWithLog(cur, pending...); err != nil {
		return b, fmt.Errorf("%s: %w", name, err)
	}
	return cur, nil
}

// chainMemberBlock is the block a chain member's final message must end with
// for the chain to parse, generated from the member's actor outcomes.
func chainMemberBlock(rt Runtime, tx *store.Tx, b store.Binding) (string, bool) {
	if tx != nil {
		if _, err := tx.ChainByMember(b.Name); err != nil {
			return "", false
		}
	} else if rt.Store != nil {
		if _, err := rt.Store.ChainByMember(b.Name); err != nil {
			return "", false
		}
	}
	actor := b.WriterRole()
	info, ok := rt.RoleRegistry().ActorInfo(actor)
	if !ok {
		return "", false
	}
	outcomes := info.Outputs.Outcomes()
	if len(outcomes) == 0 {
		return "", false
	}
	lines := make([]string, 0, len(outcomes))
	for _, key := range outcomes {
		lines = append(lines, workflow.OutcomeLine(key, info.Outputs[key]))
	}
	return strings.Join(lines, "\n"), true
}
