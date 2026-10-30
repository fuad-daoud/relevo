package relevo

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/resolve"
)

// ResolveDeps builds internal/resolve's Deps from rt: the availability inputs
// carry through wholesale, and the engine's client, skill check, git and
// config store carry through nil-safe, since a test runtime sets none of
// them. It cannot live in the new package: only the engine names Runtime.
func ResolveDeps(rt Runtime) resolve.Deps {
	var gitDirAt func(ctx context.Context, dir, ref, path string) (bool, error)
	if rt.Git != nil {
		gitDirAt = rt.Git.DirAt
	}
	return resolve.Deps{
		Deps:        AvailabilityDeps(rt),
		Policy:      rt.Policy,
		Remote:      rt.Remote,
		SkillExists: rt.SkillExists,
		GitDirAt:    gitDirAt,
		Config:      rt.Config,
	}
}
