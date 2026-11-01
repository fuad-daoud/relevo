package relevo

import (
	"context"
	"errors"

	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrBuilderAlive reports a rebind attempt against a binding whose builder is
// still running. Relevo never abandons a live builder: the human ends it, or
// `relevo done` the binding first.
var ErrBuilderAlive = errors.New("builder is still alive; rebinding would abandon it")

// ErrNoMasterMindSession is mastermind.ErrNoMasterMind as bind, add, fork and
// ask surface it: a hard error whose text is the fix.
var ErrNoMasterMindSession = errors.New(`no relevo MasterMind for this session. Run "relevo mastermind init" once here, or enable the relevo plugin (relevo doctor)`)

// ErrGitRequired reports a bind that needs a worktree with no git available.
var ErrGitRequired = errors.New("relevo bind needs git; pass --cwd to bind a tree yourself")

// BindResolved ties the calling mastermind to a builder over one working tree.
// The second return is how the builder was chosen, zero when a pane was
// adopted.
func BindResolved(ctx context.Context, rt Runtime, opts BindOptions) (store.Binding, resolve.Resolution, error) {
	rec, haveRec, err := resolveVerbMasterMind(rt, opts.MasterMindID)
	if err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}
	if opts.CWD == "" {
		return store.Binding{}, resolve.Resolution{}, refusal.BadInputf("no working directory")
	}

	if !haveRec {
		return store.Binding{}, resolve.Resolution{}, ErrNoMasterMindSession
	}
	opts.MasterMindID = rec.ID
	mastermindEP := recordEndpoint(rec)

	if opts.Resume {
		return resume(ctx, rt, opts, mastermindEP)
	}

	// A fresh bind places itself once, before anything is created. A remote
	// result goes through add's own path, so the wire and the client's record
	// keep one implementation.
	placement, perr := resolve.CreatePlacement(ctx, ResolveDeps(rt),
		store.Binding{Role: resolve.NormRole(opts.Role)}.WriterRole(), opts.Candidate, "", opts.Local,
		resolve.PlacementFlags{Tier: opts.Tier, Feature: opts.Feature, Ticket: opts.Ticket})
	if perr != nil {
		return store.Binding{}, resolve.Resolution{}, perr
	}
	if !placement.Local() {
		name := opts.Name
		if name == "" {
			name = SanitizeName(baseName(opts.CWD))
		}
		added, aerr := Add(ctx, rt, AddOptions{
			Name:         name,
			Candidate:    opts.Candidate,
			MasterMindID: opts.MasterMindID,
			Repo:         opts.CWD,
			Server:       placement.Name,
			Tier:         opts.Tier,
			Feature:      opts.Feature,
			Ticket:       opts.Ticket,
			Role:         opts.Role,
			Placement:    placement,
		})
		if aerr != nil {
			return store.Binding{}, resolve.Resolution{}, aerr
		}
		return added.Binding, added.Resolution, nil
	}

	return create(ctx, rt, opts, mastermindEP, placement)
}

// Bind is BindResolved without the resolution, for the callers that only
// need the binding.
func Bind(ctx context.Context, rt Runtime, opts BindOptions) (store.Binding, error) {
	b, _, err := BindResolved(ctx, rt, opts)
	return b, err
}
