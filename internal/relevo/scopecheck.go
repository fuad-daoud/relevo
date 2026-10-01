package relevo

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/pathscope"
	"github.com/fuad-daoud/relevo/internal/store"
)

// scopeVerdict is one close's scope judgement. Its zero value is "the actor
// has no scope", which changes nothing. Scoped is set for an actor that
// declared a scope; Refused is set when a changed path (or an unjudgeable
// round) refuses.
type scopeVerdict struct {
	Scoped  bool
	Refused bool
	Path    string
	Reason  string
	More    int
}

// judgeRoundScope resolves the closing binding's actor and judges the round's
// changed paths against its scope.
//
// It returns the zero verdict for an actor with no scope, and for a role the
// registry no longer knows -- treated as unscoped, with a warning, so a
// config change does not strand an in-flight round. An actor that declared a
// scope but whose round cannot be judged (no baseline, no git, a git error)
// is refused with the reason: refuse, never guess.
func judgeRoundScope(ctx context.Context, rt Runtime, b store.Binding) scopeVerdict {
	role := bindingRole(b)
	r, ok := rt.RoleRegistry().Role(role)
	if !ok {
		slog.Warn("round close: actor not in the registry; treating as unscoped",
			"binding", b.Name, "actor", role, "round", b.Round)
		return scopeVerdict{}
	}
	if r.Scope == nil {
		return scopeVerdict{}
	}

	v := scopeVerdict{Scoped: true}
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
		v.Refused, v.Reason = true, brief(err)
		return v
	}
	changes, err := rt.Git.ChangedFiles(ctx, b.CWD, b.RoundBaselineTree, end)
	if err != nil {
		v.Refused, v.Reason = true, brief(err)
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

// scopeNote is the note a scoped close adds: "scope=ok" on a pass and
// "scope=refused" on a refusal. An unscoped actor adds nothing.
func scopeNote(v scopeVerdict) string {
	switch {
	case !v.Scoped:
		return ""
	case v.Refused:
		return "scope=refused"
	default:
		return "scope=ok"
	}
}

// scopeHaltedAt is the report entry's HaltedAt for a refusal:
// "scope: <first path>".
func scopeHaltedAt(v scopeVerdict) string {
	if v.Path == "" {
		return "scope"
	}
	return "scope: " + v.Path
}

// scopePayloadLine is the payload's refusal line, followed by the
// `relevo show … --diff` hint.
func scopePayloadLine(b store.Binding, v scopeVerdict) string {
	var line string
	if v.Path == "" {
		line = "Scope: refused -- " + v.Reason
	} else {
		line = fmt.Sprintf("Scope: refused -- %s: %s", v.Path, v.Reason)
		if v.More > 0 {
			line += fmt.Sprintf(" (+%d more)", v.More)
		}
	}
	return line + ". Diff: " + showCommand(b.Name, b.Round, "diff")
}

// scopeHaltText is the NEEDS YOU message a refusal sets after the round has
// advanced: it names the offending file, or the reason the round could not be
// judged.
func scopeHaltText(b store.Binding, round int, v scopeVerdict) string {
	role := bindingRole(b)
	if v.Path == "" {
		return fmt.Sprintf("%s: round %d cannot be judged against actor %s's scope (%s)",
			b.Name, round, role, v.Reason)
	}
	msg := fmt.Sprintf("%s: round %d changed %s outside actor %s's scope (%s)",
		b.Name, round, v.Path, role, v.Reason)
	if v.More > 0 {
		msg += fmt.Sprintf("; %d more", v.More)
	}
	return msg
}
