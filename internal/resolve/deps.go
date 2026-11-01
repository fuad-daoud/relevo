package resolve

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
)

// Remote is the server surface a placement probe reads: identity and one
// actor's answer. It mirrors the two methods the engine's client carries, so
// the engine's client satisfies it structurally and resolution never names
// the engine's client type.
type Remote interface {
	WhoAmI(ctx context.Context, server string) (remote.WhoAmI, error)
	Actor(ctx context.Context, server, actor, candidate string) (remote.ActorView, error)
}

// Deps is what the resolution code reads from its caller, beyond what
// availability.Deps already carries. A nil field means that facility is not
// configured and reads as empty.
type Deps struct {
	availability.Deps
	// Policy is the mastermind's candidate order and tier caps per role.
	Policy policy.Policy
	// Remote is the server surface placement probes. Nil means no server can
	// be probed, so every server entry is skipped.
	Remote Remote
	// SkillExists reports whether a skill file is on disk; nil means os.Stat.
	SkillExists func(string) bool
	// GitDirAt reports whether ref names a directory at path; nil means judge
	// the disk alone, matching the current nil-Git branch.
	GitDirAt func(ctx context.Context, dir, ref, path string) (bool, error)
	// Config is the database-backed config store; nil means no saved workflows.
	Config *config.Store
}
