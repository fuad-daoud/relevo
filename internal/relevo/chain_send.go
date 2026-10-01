package relevo

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/capture"
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// RoundOpenError is returned when a chain member's round is still open: its
// prompt entry exists with no report entry, so a new round would overwrite the
// one in flight. It carries the member the caller must stop, so the CLI can
// name `relevo stop <member>` as the next command instead of an internal
// failure.
type RoundOpenError struct {
	Member string
	Round  int
}

// Error renders today's message, unchanged: the round number and the stop
// command the member's own name completes.
func (e *RoundOpenError) Error() string {
	return fmt.Sprintf("%s: round %d is still open; relevo stop %s ends it", e.Member, e.Round, e.Member)
}

// sendChainRound starts one round for a chain member. It is Send's in-lock core
// as a reusable helper: the caller holds the state lock and passes its tx, and
// the member's new binding and its prompt entry are written in the same
// critical section as the chain row that named it.
//
// It requests no verify: a chain member round is always verify-free, whatever
// policy.verify.default says, because the chain's own reviewer replaces the
// verify consult.
//
// A startRound failure leaves the member NEEDS YOU, exactly as Send's spawn
// failure does, and the error is returned wrapped with the member's name; the
// caller (the chain wiring) then halts the chain in the same critical section.
func sendChainRound(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, text string) (store.Binding, error) {
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

	planPath := rt.Store.PromptPath(name, cur.Round)
	reportPath := rt.Store.ReportPath(name, cur.Round)
	donePath := rt.Store.DonePath(name, cur.Round)
	if err := stagePlan(planPath, []byte(text)); err != nil {
		return b, fmt.Errorf("%s: stage plan at %s: %w", name, planPath, err)
	}

	baseline, baselineHead := capture.Baseline(ctx, captureDeps(rt), cur)
	prompt := roundPrompt(rt, tx, cur, planPath, reportPath, donePath)

	var pending []store.LogEntry
	cur, res, err := repickStale(rt, cur, false)
	if err != nil {
		return b, fmt.Errorf("%s: %w", name, err)
	}
	if res != nil {
		pending = append(pending, pickEntry(rt.Now().UTC(), cur.Round, bindingRole(cur), *res))
	}

	// A served member (a chain member on a server) never starts its round
	// here: like Send(Defer) it records the round as queued and lets the
	// server's admit start it, so serve.max_builders and the tier cap decide.
	// That also defers the reader's scratch worktree, which Admit builds.
	served := cur.Owner != ""
	if !served {
		// A reader round runs in a throwaway scratch worktree, never in b.CWD:
		// create it from the round's captured baseline before anything is
		// spawned, exactly as Send does.
		if cur.Shape == store.ShapeReader {
			if _, err := CreateScratchFrom(ctx, rt, cur, cur.Round, baselineHead, baseline); err != nil {
				return b, fmt.Errorf("%s: %w", name, err)
			}
		}

		started, err := startRound(ctx, rt, tx, cur, prompt, false)
		if err != nil {
			// The plan is staged and the round is open; nothing was started.
			// NEEDS YOU says so in status, exactly as Send's spawn failure does.
			cur.State = store.StateNeedsYou
			cur.Halt = "builder spawn failed: " + err.Error()
			cur.HaltAt = rt.Now().UTC()
			if saveErr := tx.SaveWithLog(cur, pending...); saveErr != nil {
				return b, fmt.Errorf("%s: %v; saving NEEDS YOU failed: %w", name, err, saveErr)
			}
			return b, fmt.Errorf("%s: %w", name, err)
		}
		cur = started
	}

	pending = append(pending, store.LogEntry{
		TS: rt.Now().UTC(), Round: cur.Round,
		Direction: store.DirToBuilder, Kind: store.KindPrompt,
		Path: planPath, Confirmed: true,
		Tier: string(effectiveTier(cur)),
		Note: chainStepNote(tx, name),
	})

	cur.RoundBaselineTree = baseline
	cur.RoundBaselineHead = baselineHead
	cur.RoundClosedTree = ""
	if served {
		cur.QueuedAt = rt.Now().UTC()
	} else {
		cur.RoundStartedAt = rt.Now().UTC()
	}
	cur.FinishPending = true
	cur.State = store.StateActive
	// Verify off, whatever policy.verify.default says: the chain's reviewer
	// replaces the verify consult on every member round.
	cur.RoundVerify = false
	if err := tx.SaveWithLog(cur, pending...); err != nil {
		return b, fmt.Errorf("%s: %w", name, err)
	}
	return cur, nil
}

// chainStepNote names the chain step a member round runs, for its prompt
// entry's note: the part the member fills. A binding that is not a chain
// member reads as the plain word.
func chainStepNote(tx *store.Tx, name string) string {
	c, err := tx.ChainByMember(name)
	if err == nil {
		if part := chainPartOf(c, name); part != "" {
			return "chain " + part
		}
	}
	return "chain"
}

// chainMemberBlock is the block a chain member's final message must end with
// and the chain parses: the reviewer's verdict or the security member's
// finding count. The planner has none -- its final message is the plan
// artifact the chain hands the builder next -- so it keeps the ordinary reader
// prompt and its reporttail block.
func chainMemberBlock(tx *store.Tx, name string) (string, bool) {
	c, err := tx.ChainByMember(name)
	if err != nil {
		return "", false
	}
	switch chainPartOf(c, name) {
	case chain.MemberReviewer:
		return "verdict: pass        # or: changes", true
	case chain.MemberSecurity:
		return "findings: 0", true
	}
	return "", false
}
