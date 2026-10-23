package relevo

import (
	"context"
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/store"
)

// GCOptions controls how finished bindings are cleared away.
type GCOptions struct {
	// Delete removes each finished binding's directory instead of archiving it.
	// Archiving is the default because every other destruction decision in relevo
	// keeps by default, and the round logs are the only record of how a feature
	// was built.
	Delete bool
	// DryRun reports what would happen and changes nothing.
	DryRun bool
	// Force archives or deletes a binding whose worktree was kept; without it
	// that binding stays DONE so a later run can retry once the tree is clean.
	Force bool
	// MasterMindID clears only DONE bindings whose Binding.MasterMindID equals this.
	// It must be non-empty unless AllMasterMinds is set (#482).
	MasterMindID string
	// AllMasterMinds clears every DONE binding regardless of mastermind, including
	// bindings with an empty MasterMindID. It is mutually exclusive with a
	// non-empty MasterMindID (#482).
	AllMasterMinds bool
}

// ErrGCNoScope reports that GC was called with neither a MasterMindID nor
// AllMasterMinds set, so no caller can get "everything" by leaving the scope
// empty (#482).
var ErrGCNoScope = errors.New("gc needs a mastermind id or AllMasterMinds")

// GCResult is one binding gc considered.
type GCResult struct {
	Name            string       `json:"name"`
	CWD             string       `json:"cwd"`
	Rounds          int          `json:"rounds"`
	Archived        bool         `json:"archived"`
	Deleted         bool         `json:"deleted"`
	BindingKept     bool         `json:"binding_kept,omitempty"` // worktree kept, so the binding was left in place
	WorktreeRemoved string       `json:"worktree_removed,omitempty"`
	WorktreeKept    string       `json:"worktree_kept,omitempty"`
	KeptReason      string       `json:"kept_reason,omitempty"`
	WorktreeGone    string       `json:"worktree_gone,omitempty"` // recorded worktree whose directory no longer exists
	Refs            []RefOutcome `json:"refs,omitempty"`
	// ServerErr is why a remote binding's server could not be asked to release
	// it; the binding was archived or deleted locally anyway.
	ServerErr string `json:"server_err,omitempty"`
	// MasterMindID is the binding's MasterMindID, verbatim, which may be "" (#482).
	MasterMindID string `json:"mastermind_id,omitempty"`
}

// GC clears away every binding the calling mastermind has marked done, or, with
// opts.AllMasterMinds, every mastermind's. Only StateDone is touched: a broken or
// orphaned binding still needs a human, and removing it would throw away the
// state that explains why.
//
// Exactly one of opts.MasterMindID and opts.AllMasterMinds must be set (#482): GC
// refuses to run with neither, so no caller can get "everything" by leaving
// the scope empty, and refuses with both, since they are exclusive. A
// binding with an empty MasterMindID (written before #303) belongs to no
// mastermind, and is cleared only by AllMasterMinds.
//
// By default, finished bindings are archived rather than deleted; passing
// opts.Delete removes them entirely.
//
// GC also deletes the binding's branch and client refs once they are on a
// remote-tracking ref, never an adopted branch, and never while the worktree
// is kept.
//
// Local bindings are scanned and removed in one critical section, so one cannot
// be marked done, or resumed, between the scan and the removal. A remote
// binding's server is asked between two critical sections, never inside one,
// and the binding is re-loaded before it is removed.
func GC(ctx context.Context, rt Runtime, opts GCOptions) ([]GCResult, error) {
	switch {
	case opts.MasterMindID == "" && !opts.AllMasterMinds:
		return nil, ErrGCNoScope
	case opts.MasterMindID != "" && opts.AllMasterMinds:
		return nil, fmt.Errorf("%w: MasterMindID and AllMasterMinds are exclusive", ErrGCNoScope)
	}

	var out []GCResult
	var remotes []store.Binding

	err := rt.Store.WithLock(func(tx *store.Tx) error {
		bindings, err := tx.List()
		if err != nil {
			return err
		}

		for _, b := range bindings {
			if !gcInScope(b, opts) {
				continue
			}
			// A remote binding's tree lives on its server, so the server is asked
			// after the lock is released; a dry run never asks.
			if b.Builder.Remote() && !opts.DryRun {
				remotes = append(remotes, b)
				continue
			}

			res := newGCResult(b)
			outcome := worktreeTeardown(ctx, rt, b, opts.DryRun)
			res.WorktreeRemoved = outcome.Removed
			res.WorktreeKept = outcome.Kept
			res.KeptReason = outcome.Reason
			res.WorktreeGone = outcome.Gone
			if outcome.Kept == "" {
				res.Refs = gcRefs(ctx, rt, b, opts.DryRun)
			}

			res.BindingKept = outcome.Kept != "" && !opts.Force
			if !opts.DryRun && !res.BindingKept {
				if err := gcRemove(tx, b.Name, opts, &res); err != nil {
					return err
				}
			}

			out = append(out, res)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	released, kept := askRemoteServers(ctx, rt, remotes, opts)
	out = append(out, kept...)

	cleared, err := clearReleased(rt, released, opts)
	if err != nil {
		return nil, err
	}

	return append(out, cleared...), nil
}

func gcInScope(b store.Binding, opts GCOptions) bool {
	if b.State != store.StateDone {
		return false
	}

	return opts.AllMasterMinds || b.MasterMindID == opts.MasterMindID
}

func newGCResult(b store.Binding) GCResult {
	return GCResult{Name: b.Name, CWD: b.CWD, Rounds: b.Round, MasterMindID: b.MasterMindID}
}

// gcRefs reports (and, unless dryRun, deletes) the binding's branch and client
// refs that are already on a remote-tracking ref.
func gcRefs(ctx context.Context, rt Runtime, b store.Binding, dryRun bool) []RefOutcome {
	if rt.Git == nil {
		return nil
	}
	dir := repoDirOf(b)
	if dir == "" {
		return nil
	}
	refs, err := bindingRefCandidates(ctx, rt, dir, b)
	if err != nil {
		return []RefOutcome{{Ref: "refs/relevo/" + b.Name + "/", Reason: "check failed: " + brief(err)}}
	}

	return cleanRefs(ctx, rt, dir, refs, dryRun)
}

func gcRemove(tx *store.Tx, name string, opts GCOptions, res *GCResult) error {
	if opts.Delete {
		if err := tx.Delete(name); err != nil {
			return fmt.Errorf("delete %q: %w", name, err)
		}
		res.Deleted = true

		return nil
	}
	if _, err := tx.Archive(name); err != nil {
		return fmt.Errorf("archive %q: %w", name, err)
	}
	res.Archived = true

	return nil
}

// releasedBinding is a remote DONE binding whose server let go of it, with the
// result so far.
type releasedBinding struct {
	b   store.Binding
	res GCResult
}

// askRemoteServers asks each remote binding's server to unbind it, outside any
// lock. A server that keeps its worktree keeps the binding; one that is
// unreachable or fails does not block the sweep, and its error is reported.
func askRemoteServers(ctx context.Context, rt Runtime, remotes []store.Binding, opts GCOptions) ([]releasedBinding, []GCResult) {
	var released []releasedBinding
	var kept []GCResult

	for _, b := range remotes {
		res := newGCResult(b)
		err := askServerUnbind(ctx, rt, b, opts.Force)
		switch {
		case errors.Is(err, ErrWorktreeKept):
			res.BindingKept = true
			res.WorktreeKept = "on " + b.Builder.Server
			res.KeptReason = KeptReasonOf(err)
			if res.KeptReason == "" {
				res.KeptReason = err.Error()
			}
			kept = append(kept, res)

			continue
		case err != nil:
			res.ServerErr = err.Error()
		}
		res.Refs = gcRefs(ctx, rt, b, false)
		released = append(released, releasedBinding{b: b, res: res})
	}

	return released, kept
}

// clearReleased archives or deletes the released bindings under the lock. Each
// is re-loaded first: one resumed or removed since the scan is left alone.
func clearReleased(rt Runtime, released []releasedBinding, opts GCOptions) ([]GCResult, error) {
	var out []GCResult
	if len(released) == 0 {
		return out, nil
	}

	err := rt.Store.WithLock(func(tx *store.Tx) error {
		for _, r := range released {
			cur, err := tx.Load(r.b.Name)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if cur.State != store.StateDone {
				continue
			}
			if err := gcRemove(tx, r.b.Name, opts, &r.res); err != nil {
				return err
			}
			out = append(out, r.res)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}
