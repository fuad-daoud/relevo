package relevo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// The two outcomes a fork's merge reports, matching the workflow's joined
// and conflict edges.
const (
	chainMergeJoined   = "joined"
	chainMergeConflict = "conflict"
)

// chainFlowMerge merges every child's branch into the parent writer's tree in
// child key order -- declaration order, so a fixed fork merges as written and
// an each fork in item order -- and reports back as one merge_closed event.
//
// The merge stops at the first conflicting child with that conflict still in
// the tree, and lists the branches after it in the conflict report, because
// the author's merge round has to merge those itself. Any other git failure
// is not a conflict and has nothing the author can resolve, so the parent
// halts.
func chainFlowMerge(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, before workflow.State, next *workflow.State, ev workflow.Event, act workflow.Action) error {
	writer, err := chainFlowWriterMember(tx, c)
	if err != nil {
		return chainMergeHalt(ctx, rt, tx, c, def, before, next, ev, err.Error())
	}
	dir, err := chainMergeDir(tx, writer)
	if err != nil {
		return chainMergeHalt(ctx, rt, tx, c, def, before, next, ev,
			fmt.Sprintf("chain %s: the writer %s has no tree to merge into: %v", c.Name, writer, err))
	}

	out := chainMergeChildren(ctx, rt, c, dir, next.Awaiting.Children)
	if out.fatal != "" {
		return chainMergeHalt(ctx, rt, tx, c, def, before, next, ev, out.fatal)
	}

	result := chainMergeJoined
	var artifacts map[string][]string
	var flagged string
	if out.stopKey != "" {
		result = chainMergeConflict
		key, werr := chainWriteMergeConflict(ctx, rt, tx, c, out, &flagged)
		if werr != nil {
			return chainMergeHalt(ctx, rt, tx, c, def, before, next, ev,
				fmt.Sprintf("chain %s: the conflict report could not be written: %v", c.Name, werr))
		}
		artifacts = map[string][]string{"conflict": {key}}
	}

	act.Reason = out.trace(result) + flagged
	if err := chainSaveFlow(rt, tx, c, def, before, *next, ev, act, writer); err != nil {
		return err
	}
	// The merge closes against the row just written, so the parent is re-read
	// rather than carried in from the caller's stale copy.
	saved, err := tx.Chain(c.Name)
	if err != nil {
		return err
	}
	return chainAdvance(ctx, rt, tx, saved, workflow.Event{
		Kind:      workflow.EventMergeClosed,
		Step:      act.Step,
		Result:    result,
		Artifacts: artifacts,
	})
}

// mergeOutcome is one merge pass: the keys merged, the conflict stop, the
// paths it conflicted on, the branches never reached, and a non-conflict git
// failure's message.
type mergeOutcome struct {
	merged    []string
	stopKey   string
	paths     []string
	remaining []string
	fatal     string
}

func (o mergeOutcome) trace(result string) string {
	return "merged " + strings.Join(o.merged, ", ") + " → " + result
}

// chainMergeChildren merges each child's branch into dir in key order,
// stopping at the first conflict. The merge stays in progress and the keys
// after it are recorded, so the conflict report can name them.
func chainMergeChildren(ctx context.Context, rt Runtime, c db.ChainRow, dir string, keys []string) mergeOutcome {
	var out mergeOutcome
	for i, key := range keys {
		ref := chainForkChildRef(c.Name, key)
		paths, err := rt.Git.MergeKeep(ctx, dir, ref)
		switch {
		case err == nil:
			out.merged = append(out.merged, key)
		case errors.Is(err, git.ErrMergeConflict):
			out.stopKey = key
			out.paths = paths
			for _, later := range keys[i+1:] {
				out.remaining = append(out.remaining, chainForkChildRef(c.Name, later))
			}
			return out
		default:
			out.fatal = fmt.Sprintf("child %s could not merge into the parent: %v",
				chainForkChildName(c.Name, key), err)
			return out
		}
	}
	return out
}

func chainMergeDir(tx *store.Tx, writer string) (string, error) {
	b, err := tx.Load(writer)
	if err != nil {
		return "", err
	}
	if b.CWD == "" {
		return "", errors.New("it has no working directory")
	}
	return b.CWD, nil
}

// chainWriteMergeConflict seals the fork's conflict report as a round file
// under the parent: the unmerged paths, then the branches after the stopped
// child. It is keyed to the parent's writer and newest closed round, the same
// pair a check's log uses.
//
// The body is scanned for injection first, because a workflow seeded with
// {{<fork>.conflict}} alone inlines this file verbatim into the resolving
// builder's prompt, so an instruction-shaped path would otherwise reach a
// model unflagged. flagged carries the caveat when the scan found something,
// and stays "" when it did not.
func chainWriteMergeConflict(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, out mergeOutcome, flagged *string) (string, error) {
	member, round, err := chainCheckLogTarget(tx, c)
	if err != nil {
		return "", err
	}
	body := out.report()
	binding, lerr := tx.Load(member)
	if lerr != nil {
		return "", lerr
	}
	sc := scanForInjection(ctx, rt, "report", binding, []byte(body))
	if sc.Flagged > 0 {
		caveat := flaggedParenthetical(sc.Flagged, sc.Record)
		*flagged = caveat
		body += strings.TrimRight(caveat, " ") + "\n"
	}
	key := rt.Store.ForkConflictPath(member, round)
	return key, tx.PutRoundFile(member, round, key, []byte(body))
}

// report renders the conflict file: the unmerged paths, then the branches
// the merge stopped short of.
func (o mergeOutcome) report() string {
	var b strings.Builder
	for _, p := range o.paths {
		b.WriteString(p + "\n")
	}
	if len(o.remaining) > 0 {
		b.WriteString("not yet merged:\n")
		for _, ref := range o.remaining {
			b.WriteString(ref + "\n")
		}
	}
	return b.String()
}

// chainMergeHalt ends the parent after an uncarryable merge, naming the child
// and git's own message so the author sees which merge failed and why.
func chainMergeHalt(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, before workflow.State, next *workflow.State, ev workflow.Event, reason string) error {
	next.Status = workflow.StatusHalted
	next.Reason = reason
	return chainTerminalWF(ctx, rt, tx, c, def, before, *next, ev,
		workflow.Action{Kind: workflow.ActionHalt, Step: next.Awaiting.Step, Reason: reason})
}
