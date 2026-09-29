package capture

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/store"
)

// DriftResult is what one between-rounds capture attempt produced. A zero value
// means "nothing to say", which is the normal outcome.
type DriftResult struct {
	Available bool     // a comparison ran, or was short-circuited by equal trees
	Path      string   // patch file; "" when the trees matched or the body was truncated
	Stat      git.Stat // exact whenever Available
	Truncated bool     // patch omitted because it exceeded the cap
	Reason    string   // why Available is false; "" when it is true
}

// Drift compares the tree a round ended at against the tree the next round
// is opening at, writing the patch to d.Store.DriftPath(b.Name, b.Round) when
// there is a body worth keeping.
//
// When baseline == b.RoundClosedTree, Drift short-circuits before any git
// call. It NEVER returns an error: every failure lands in DriftResult.Reason.
//
// Preconditions: none.
// Postconditions:
//   - Available is false with empty Reason when d.Git is nil, b.RoundClosedTree
//     is empty, baseline is empty, or DiffTrees returned git.ErrNotRepo.
//   - Available is true with zero Stat when baseline == b.RoundClosedTree.
//   - Available is false with Reason set when DiffTrees or writing the patch failed.
//   - When Available is true and Stat is non-empty, Path resolves through
//     Store.ReadFile (a round_file row, no file on disk) unless Truncated is
//     true.
func Drift(ctx context.Context, d Deps, tx *store.Tx, b store.Binding, baseline string) DriftResult {
	if d.Git == nil || b.RoundClosedTree == "" || baseline == "" {
		return DriftResult{Available: false}
	}
	if baseline == b.RoundClosedTree {
		return DriftResult{Available: true}
	}

	diff, err := d.Git.DiffTrees(ctx, b.CWD, b.RoundClosedTree, baseline)
	if errors.Is(err, git.ErrNotRepo) {
		return DriftResult{Available: false}
	}
	if err != nil {
		return DriftResult{Available: false, Reason: brief(err)}
	}

	if diff.Stat.Empty() {
		return DriftResult{Available: true, Stat: diff.Stat}
	}

	if diff.Truncated {
		return DriftResult{Available: true, Stat: diff.Stat, Truncated: true}
	}

	patchPath := d.Store.DriftPath(b.Name, b.Round)
	if err := tx.PutRoundFile(b.Name, b.Round, patchPath, diff.Patch); err != nil {
		return DriftResult{Available: false, Reason: brief(err)}
	}

	return DriftResult{Available: true, Path: patchPath, Stat: diff.Stat}
}

// DriftSummary returns the human summary for the drift log entry.
func DriftSummary(res DriftResult) string {
	if !res.Available {
		if res.Reason != "" {
			return fmt.Sprintf("unavailable: %s", res.Reason)
		}
		return "unavailable"
	}
	if res.Stat.Empty() {
		return "no drift"
	}
	if res.Truncated {
		return "truncated"
	}
	return fmt.Sprintf("%s, +%d -%d", formatFiles(res.Stat.FilesChanged), res.Stat.Insertions, res.Stat.Deletions)
}

// DriftLine renders the stdout line for drift detected between rounds, or ""
// when there is nothing worth telling the mastermind (no git, not a repository,
// or an empty Stat).
//
// round is the opening round; the prose names round-1, the round that closed,
// and points the mastermind at `relevo show <name> --round <round> --drift`
// instead of the patch's path.
func DriftLine(res DriftResult, name string, round int) string {
	if !res.Available {
		if res.Reason == "" {
			return ""
		}
		return fmt.Sprintf("drift: unavailable (%s)", res.Reason)
	}
	if res.Stat.Empty() {
		return ""
	}
	msg := fmt.Sprintf("drift: %s, +%d -%d between round %d's report and this send",
		formatFiles(res.Stat.FilesChanged), res.Stat.Insertions, res.Stat.Deletions, round-1)
	if res.Truncated {
		return msg + "\n       (patch omitted, over the 4 MiB cap)"
	}
	if res.Path != "" {
		return msg + "\n       " + showCommand(name, round, "drift")
	}
	return msg
}

// ReadDrift returns the stored drift patch for one round, and whether one exists.
// It is the read path behind `relevo show --drift`.
//
// Errors: store.ErrNotFound for an unknown binding; a wrapped read error.
func ReadDrift(s *store.Store, name string, round int) ([]byte, bool, error) {
	if _, err := s.Load(name); err != nil {
		return nil, false, err
	}

	path := s.DriftPath(name, round)
	data, err := s.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read drift %s: %w", path, err)
	}
	return data, true, nil
}
