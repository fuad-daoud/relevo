package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/text"
)

// worktreeOutcome is what a teardown attempt decided about one binding's
// relevo-created worktree. Exactly one of Removed, Kept, Gone is set, or none
// when the binding never had a worktree.
type worktreeOutcome struct {
	Removed string // worktree relevo removed, or ""
	Kept    string // worktree relevo left in place, or ""
	Reason  string // why it was kept; "" when nothing was kept
	Gone    string // recorded worktree whose directory no longer exists, or ""
}

// worktreeTeardown decides what to do with a binding's relevo-created worktree
// and reports what it did. It never returns an error: failing to remove a
// directory must not fail the unbind or the sweep that asked for it.
//
// Rules, in order:
//  1. b.Worktree == ""        -> nothing to do (zero outcome)
//  2. rt.Git == nil           -> keep, reason "git unavailable"
//  3. stat(worktree) is ErrNotExist -> gone (the directory was already removed)
//  4. the dirty check errors    -> keep, reason naming the FAILED CHECK: "dirty check failed: " + brief(err)
//  5. the tree is dirty         -> keep, reason "uncommitted changes"
//  6. otherwise                 -> remove; on failure keep with the git error brief(err)
//
// worktreeTeardown never removes a branch; GC removes a relevo-created branch once it is on a remote-tracking ref (refclean.go).
func worktreeTeardown(ctx context.Context, rt Runtime, b store.Binding, dryRun bool) worktreeOutcome {
	if b.Worktree == "" {
		return worktreeOutcome{}
	}
	if rt.Git == nil {
		return worktreeOutcome{Kept: b.Worktree, Reason: "git unavailable"}
	}

	// git classifies a chdir into a missing directory as a missing binary,
	// which is the wrong diagnosis; the caller knows the path and checks it first.
	if _, err := os.Stat(b.Worktree); errors.Is(err, os.ErrNotExist) {
		rt.Store.PruneWorktreeDirs()
		return worktreeOutcome{Gone: b.Worktree}
	}

	dirty, err := rt.Git.Dirty(ctx, b.Worktree)
	if err != nil {
		return worktreeOutcome{Kept: b.Worktree, Reason: "dirty check failed: " + text.Brief(err)}
	}
	if dirty {
		return worktreeOutcome{Kept: b.Worktree, Reason: "uncommitted changes"}
	}

	if dryRun {
		return worktreeOutcome{Removed: b.Worktree}
	}

	if err := rt.Git.RemoveWorktree(ctx, b.CWD, b.Worktree, false); err != nil {
		return worktreeOutcome{Kept: b.Worktree, Reason: text.Brief(err)}
	}
	rt.Store.PruneWorktreeDirs()
	return worktreeOutcome{Removed: b.Worktree}
}

// UnbindResult is what unbinding actually did. Exactly one of WorktreeRemoved,
// WorktreeKept, WorktreeGone is set, or none when the binding never had a
// worktree. A kept worktree is the important case: it means the fork's tree
// still holds uncommitted work, so relevo left it alone and the human decides.
type UnbindResult struct {
	Archived        bool   // true when the binding was archived rather than deleted
	WorktreeRemoved string // worktree relevo removed, or ""
	WorktreeKept    string // worktree relevo refused to remove, or ""
	KeptReason      string // why it was kept; "" when nothing was kept
	WorktreeGone    string // recorded worktree whose directory no longer exists, or ""
	// ProcessStopped is the headless builder process relevo stopped, or 0.
	// ProcessErr is why a stop failed; "" when it did not. Both
	// zero for a pane binding and for a headless one between rounds.
	ProcessStopped int
	ProcessErr     string
}

// ErrWorktreeKept reports that unbind left the binding in place because its
// worktree was kept; force archives it anyway.
var ErrWorktreeKept = refusal.Sentinel("worktree kept")

func keptRefusal(name string, res UnbindResult) error {
	msg := fmt.Sprintf("binding %s kept: worktree %s kept (%s)", name, res.WorktreeKept, res.KeptReason)
	if res.ProcessStopped != 0 {
		msg += fmt.Sprintf("; its builder process (pid %d) was stopped", res.ProcessStopped)
	}
	return NewWorktreeKept(msg)
}

// askServerUnbind tells a remote binding's server to release it before
// anything local changes. A 404 means the server already considers it gone,
// which is not a reason to refuse the local unbind.
func askServerUnbind(ctx context.Context, rt Runtime, b store.Binding, force bool) error {
	if rt.Remote == nil {
		return ErrRemoteUnavailable
	}
	uerr := rt.Remote.Unbind(ctx, b.Builder.Server, b.Name, force)
	if uerr == nil {
		return nil
	}
	var httpErr *client.HTTPError
	if errors.As(uerr, &httpErr) {
		switch {
		case httpErr.Status == 404:
			return nil
		case httpErr.Status == 409 && httpErr.Body.Code == remote.CodeWorktreeKept:
			reason := KeptReason(httpErr.Body.Message)

			return newWorktreeKeptWithReason("worktree on "+b.Builder.Server+" kept: "+reason, reason)
		}
	}
	if errors.Is(uerr, client.ErrUnreachable) {
		return fmt.Errorf("%s unreachable: %w", b.Builder.Server, uerr)
	}
	return fmt.Errorf("%s: %w", b.Builder.Server, uerr)
}

// reapSessions deletes the builder's session and everything already abandoned,
// directly: the record is about to go, so nothing is saved and a failed delete
// only warns. Deletion is safe only once the process is really stopped -- a
// live one still holds its session.
func reapSessions(ctx context.Context, rt Runtime, b store.Binding, res UnbindResult) {
	if res.ProcessStopped != 0 && res.ProcessErr == "" {
		b = abandonSession(b)
	}
	for _, s := range b.AbandonedSessions {
		if rt.SessionReaper == nil {
			break
		}
		if err := rt.SessionReaper.DeleteSession(ctx, s); err != nil {
			slog.Warn("abandoned harness session not deleted", "binding", b.Name, "kind", s.Kind, "id", s.ID, "err", err)
		}
	}
}

// Unbind is UnbindWith without force: a kept worktree keeps the binding.
func Unbind(ctx context.Context, rt Runtime, name string, archive bool) (UnbindResult, error) {
	return UnbindWith(ctx, rt, name, archive, false)
}

// UnbindWith clears away one binding's state, leaving its builder process and
// worktree release to the paths below.
//
// When archive is set the binding's record is archived rather than deleted,
// which frees the name for a fresh bind while its log and every round file
// survive as rows -- the record of what the mastermind actually told the builder.
// If relevo created a git worktree for this binding, UnbindWith removes it provided
// it is clean, never removing the branch. When the worktree is kept the binding
// stays, resumable, and the result comes back with ErrWorktreeKept; force
// archives or deletes it anyway.
func UnbindWith(ctx context.Context, rt Runtime, name string, archive, force bool) (UnbindResult, error) {
	b, err := rt.Store.Load(name)
	if err != nil {
		return UnbindResult{}, err
	}

	// A running chain owns its member's rounds: unbinding one would steal the
	// record the chain's next send needs, and the chain has no way to resume
	// it. The refusal is read-only and sits before every side effect below --
	// a late one would stop a process and then keep the record.
	if err := chain.RefuseRunningMemberStore(rt.Store, name); err != nil {
		return UnbindResult{}, err
	}

	if b.Builder.Remote() {
		if err := askServerUnbind(ctx, rt, b, force); err != nil {
			return UnbindResult{}, err
		}
	}

	var res UnbindResult
	// Stop a live headless round before touching its tree: a builder still
	// writing would dirty the worktree relevo is about to judge clean or not.
	// A failed stop is reported, never fatal -- the unbind is the human's
	// decision and it proceeds.
	if pid, err := stopProcess(ctx, rt, b, "unbind"); err != nil {
		res.ProcessErr = fmt.Sprintf("pid %d: %v", pid, err)
	} else if pid != 0 {
		res.ProcessStopped = pid
	}

	outcome := worktreeTeardown(ctx, rt, b, false)
	res.WorktreeRemoved = outcome.Removed
	res.WorktreeKept = outcome.Kept
	res.KeptReason = outcome.Reason
	res.WorktreeGone = outcome.Gone

	// The guard sits before the session reaping so a kept binding stays
	// resumable with its sessions intact.
	if outcome.Kept != "" && !force {
		return res, keptRefusal(name, res)
	}

	reapSessions(ctx, rt, b, res)

	// A reader's throwaway worktree goes with the binding, before the record
	// is archived or deleted: the sweep can no longer resolve it afterwards.
	removeReaderScratch(ctx, rt, b, b.Round)
	housekeep.RemoveRoundTmp(HousekeepDeps(rt), b, b.Round)

	if archive {
		if _, err := rt.Store.Archive(name); err != nil {
			return res, err
		}
		res.Archived = true
	} else {
		if err := rt.Store.Delete(name); err != nil {
			return res, err
		}
	}

	return res, nil
}

// SanitizeName coerces a directory name into relevo's binding-name rule.
func SanitizeName(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteRune('-')
		}
	}

	out := strings.Trim(sb.String(), "-")
	if out == "" {
		return "relevo"
	}
	if out[0] < 'a' || out[0] > 'z' {
		out = "b" + out
	}
	if len(out) > store.MaxAgentNameLen {
		out = out[:store.MaxAgentNameLen]
	}

	return out
}

func baseName(path string) string {
	trimmed := strings.TrimRight(path, "/")
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}
