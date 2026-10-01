package relevo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/capture"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainPlanDiff captures the plan's cumulative diff -- the plan-start commit to
// the closing round's closed tree -- as a round_file beside the round diff, and
// returns its key, or "" when there is nothing to capture. It NEVER fails the
// close: an empty plan-start commit, a builder whose closed tree is unknown, no
// git and a git failure all read as "not captured", the same shape capture's
// own diff results use.
func chainPlanDiff(rt Runtime, tx *store.Tx, c db.ChainRow, builder string, builderRound int) string {
	if c.PlanStartCommit == "" {
		return ""
	}
	b, err := tx.Load(builder)
	if err != nil || b.RoundClosedTree == "" {
		return ""
	}
	path := rt.Store.PlanDiffPath(builder, builderRound)
	res := capture.PlanDiff(context.Background(), captureDeps(rt), tx, capture.PlanDiffSpec{
		Name: builder, Round: builderRound,
		Dir: b.CWD, From: c.PlanStartCommit, End: b.RoundClosedTree, Path: path,
	})
	if res.Path == "" {
		return ""
	}
	return path
}

// chainRoundPromptPath is the round's own staged prompt when it is not the plan
// copy itself, and "" when the round ran the plan copy: a builder round handed
// the plan's own bytes needs no prompt named beside it, while a correction or
// fix round, whose prompt is the planner's text, does.
func chainRoundPromptPath(rt Runtime, planPath, builder string, builderRound int) string {
	promptPath := rt.Store.PromptPath(builder, builderRound)
	staged, perr := rt.Store.ReadFile(promptPath)
	plan, lerr := rt.Store.ReadFile(planPath)
	if perr != nil || lerr != nil || !bytes.Equal(staged, plan) {
		return promptPath
	}
	return ""
}

// memberReportPath is the path the planning member's report entry for round
// carries: the correction or fix plan artifact the planner wrote, which is what
// a builder round on that plan must be handed. A planner with no such entry, or
// one with no path, is a failure of the send rather than a silent plan copy.
func memberReportPath(tx *store.Tx, planner string, round int) (string, error) {
	entries, err := tx.ReadLog(planner)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && e.Kind == store.KindReport && e.Round == round && e.Path != "" {
			return e.Path, nil
		}
	}
	return "", fmt.Errorf("chain planner %s has no plan for round %d", planner, round)
}

// chainSeedInput is the one path a seed may name for one input. An empty path
// stays empty. A path the store reads as itself -- a regular file on disk that
// is not a reserved round-file name -- is named as it is. Anything else is a
// fact about its source member round, so its bytes are read through the store
// and copied under the chain's own directory, and the copy is named: a reserved
// key, a sealed row and a file a later seal removes all copy the same way, and
// the copy stays valid for the round.
//
// A read, a key or a write that fails yields chainSeedMissing's one path-free
// clause, so a seed never names a path a runner cannot open.
func chainSeedInput(rt Runtime, c db.ChainRow, path string) string {
	if path == "" {
		return ""
	}
	if rt.Store.DiskRegularFile(path) {
		return path
	}
	body, err := rt.Store.ReadFile(path)
	if err != nil {
		return chainSeedMissing(err)
	}
	dest, ok := rt.Store.ChainInputPath(c.Name, path)
	if !ok {
		return chainSeedMissing(errors.New("not a binding round file"))
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return chainSeedMissing(err)
	}
	if err := os.WriteFile(dest, body, 0o644); err != nil {
		return chainSeedMissing(err)
	}
	return dest
}

// chainSeedMissing renders an input the seed cannot name as one path-free
// clause: "not available: " plus the reason. A *fs.PathError keeps only its
// underlying reason; any other error keeps its first line. The input's own path
// is never repeated, so nothing in the seed reads as an openable path.
func chainSeedMissing(err error) string {
	reason := err.Error()
	var perr *fs.PathError
	if errors.As(err, &perr) {
		reason = perr.Err.Error()
	} else if i := strings.IndexByte(reason, '\n'); i >= 0 {
		reason = reason[:i]
	}
	return "not available: " + reason
}
