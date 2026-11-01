package relevo

import (
	"context"
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/pathscope"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/text"
)

// judgeRoundScope resolves the closing binding's actor and judges the round's
// changed paths against its scope.
//
// It returns the zero verdict for an actor with no scope, and for a role the
// registry no longer knows -- treated as unscoped, with a warning, so a
// config change does not strand an in-flight round. An actor that declared a
// scope but whose round cannot be judged (no baseline, no git, a git error)
// is refused with the reason: refuse, never guess.
func judgeRoundScope(ctx context.Context, rt Runtime, b store.Binding) pathscope.Verdict {
	role := b.WriterRole()
	r, ok := rt.RoleRegistry().Role(role)
	if !ok {
		slog.Warn("round close: actor not in the registry; treating as unscoped",
			"binding", b.Name, "actor", role, "round", b.Round)
		return pathscope.Verdict{}
	}
	if r.Scope == nil {
		return pathscope.Verdict{}
	}

	v := pathscope.Verdict{Scoped: true}
	if rt.Git == nil {
		v.Refused, v.Reason = true, "git unavailable"
		return v
	}
	if b.RoundBaselineTree == "" {
		v.Refused, v.Reason = true, "no baseline tree"
		return v
	}
	end, err := rt.Git.SnapshotTree(ctx, b.CWD)
	if err != nil {
		v.Refused, v.Reason = true, text.Brief(err)
		return v
	}
	changes, err := rt.Git.ChangedFiles(ctx, b.CWD, b.RoundBaselineTree, end)
	if err != nil {
		v.Refused, v.Reason = true, text.Brief(err)
		return v
	}
	readBlob := func(oid string) ([]byte, error) {
		return rt.Git.ReadBlob(ctx, b.CWD, oid)
	}
	violations := pathscope.Judge(*r.Scope, changes, readBlob)
	if len(violations) == 0 {
		return v
	}
	v.Refused = true
	v.Path = violations[0].Path
	v.Reason = violations[0].Reason
	v.More = len(violations) - 1
	return v
}
