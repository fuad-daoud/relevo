package relevo

import (
	"context"
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/store"
)

// captureRepo best-effort captures cwd's git identity for the coming history
// database (docs/specs/2026-09-20-persistence-design.md §5.4). It never
// fails the caller: a nil Git, or any error RepoFacts returns, is logged at
// Debug and reported as a nil *store.RepoRef.
func captureRepo(ctx context.Context, rt Runtime, cwd string) *store.RepoRef {
	if rt.Git == nil {
		return nil
	}
	originURL, commonDir, err := rt.Git.RepoFacts(ctx, cwd)
	if err != nil {
		slog.Debug("repo facts", "cwd", cwd, "err", err)
		return nil
	}
	return &store.RepoRef{
		OriginURL: git.NormalizeOriginURL(originURL),
		CommonDir: commonDir,
	}
}
