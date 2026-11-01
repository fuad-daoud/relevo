package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// resume re-points an existing binding at the calling mastermind, and -- when
// the caller supplied a builder, or asked for one with Rebind -- at a new
// builder as well. When a builder is supplied, the current one must not be
// alive: rebinding over a working builder would abandon a round mid-flight.
//
// A rebind of a DONE binding is refused; a mastermind-only resume of one is
// allowed and reactivates it. RoundClosedTree is cleared with a new builder:
// a tree that changed hands says nothing about the builder that left it.
// Round, CWD, Name and the round log are untouched, and so is the binding's
// mode: a remote binding stays remote, a local one headless.
//
// Errors: store.ErrNotFound; ErrBuilderAlive; ErrRunnerUnavailable; a wrapped
// git or remote error.
func resume(ctx context.Context, rt Runtime, opts BindOptions, mastermindEP store.Endpoint) (store.Binding, resolve.Resolution, error) {
	b, ticket, res, restore, err := resumeRestoreWorktree(ctx, rt, opts, mastermindEP)
	if err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}
	b, builder, res, rebinding, err := resumeRebindBuilder(ctx, rt, &opts, b, res, restore)
	if err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}
	return resumeApplyCommit(rt, opts, mastermindEP, b, builder, res, rebinding, restore, ticket)
}

func resumeRestoreWorktree(ctx context.Context, rt Runtime, opts BindOptions, mastermindEP store.Endpoint) (store.Binding, string, resolve.Resolution, bool, error) {
	var res resolve.Resolution
	b, err := resumeLoad(rt, opts.Name)
	if err != nil {
		return store.Binding{}, "", resolve.Resolution{}, false, err
	}
	if mine, err := handedToMe(rt, opts.Name); err != nil {
		return store.Binding{}, "", resolve.Resolution{}, false, err
	} else if mine && carriesOldPaths(rt, b) {
		if b, err = resumeHandedOver(ctx, rt, opts, b); err != nil {
			return store.Binding{}, "", resolve.Resolution{}, false, err
		}
	}

	// A remote binding's mode is fixed at creation, so none of the
	// pane/worktree logic below applies to it.
	if b.Builder.Remote() {
		out, res, err := resumeRemote(ctx, rt, opts, mastermindEP, b)
		return out, "", res, false, err
	}

	// The label flags are applied after the load, because --ticket's
	// repository hint is the binding's own recorded origin.
	if opts.Feature != "" {
		if err := store.ValidFeature(opts.Feature); err != nil {
			return store.Binding{}, "", resolve.Resolution{}, false, err
		}
	}
	ticket := ""
	if opts.Ticket != "" {
		ticket, err = parseTicket(opts.Ticket, b.RepoRef)
		if err != nil {
			return store.Binding{}, "", resolve.Resolution{}, false, err
		}
	}

	restore := false
	if b.Worktree != "" {
		if _, err := os.Stat(b.Worktree); errors.Is(err, os.ErrNotExist) {
			restore = true
		}
	}

	if restore {
		if b.Branch == "" {
			return store.Binding{}, "", resolve.Resolution{}, false, fmt.Errorf("binding %q: worktree %s is gone and no branch is recorded; relevo bind --worktree to start fresh", opts.Name, b.Worktree)
		}
		if rt.Git == nil {
			return store.Binding{}, "", resolve.Resolution{}, false, errors.New("git unavailable; cannot restore worktree")
		}
		// An add binding's CWD is the worktree itself, which is gone, and the
		// repository it was cut from is not recorded: restore from the
		// caller's cwd once the branch is confirmed to live in it.
		exists, err := rt.Git.BranchExists(ctx, opts.CWD, b.Branch)
		if err != nil {
			return store.Binding{}, "", resolve.Resolution{}, false, fmt.Errorf("restore worktree: %w", err)
		}
		if !exists {
			return store.Binding{}, "", resolve.Resolution{}, false, fmt.Errorf("binding %q: branch %s is not in %s; run resume from the repository the worktree was cut from", opts.Name, b.Branch, opts.CWD)
		}
		if err := rt.Git.CheckoutWorktree(ctx, opts.CWD, b.Worktree, b.Branch); err != nil {
			if errors.Is(err, git.ErrBranchCheckedOut) {
				return store.Binding{}, "", resolve.Resolution{}, false, fmt.Errorf("binding %q: branch %s is checked out in another worktree (git worktree list); free it, then resume", opts.Name, b.Branch)
			}
			return store.Binding{}, "", resolve.Resolution{}, false, fmt.Errorf("restore worktree: %w", err)
		}
		res.RestoredWorktree = b.Worktree
		res.RestoredBranch = b.Branch
	}
	return b, ticket, res, restore, nil
}

func resumeRebindBuilder(ctx context.Context, rt Runtime, opts *BindOptions, b store.Binding, res resolve.Resolution, restore bool) (store.Binding, store.Endpoint, resolve.Resolution, bool, error) {
	rebinding := opts.Rebind || opts.Candidate != ""
	// A paused binding has no builder identity left -- pause cleared it -- so
	// a resume from PAUSED always rebinds; --rebind is implied.
	if b.State == store.StatePaused {
		res.WasPaused = true
		rebinding = true
	}

	var builder store.Endpoint
	if !rebinding {
		return b, builder, res, false, nil
	}
	if b.State == store.StateDone && !restore {
		return store.Binding{}, store.Endpoint{}, resolve.Resolution{}, false, fmt.Errorf("binding %q is done: `relevo bind` to start fresh", opts.Name)
	}
	// A local builder is a process, so aliveness is asked of the Runner:
	// a PID has no "moved pane" ambiguity.
	if rt.Runner == nil {
		return store.Binding{}, store.Endpoint{}, resolve.Resolution{}, false, spawn.ErrRunnerUnavailable
	}
	if b.Builder.PID != 0 {
		alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
		if err != nil {
			return store.Binding{}, store.Endpoint{}, resolve.Resolution{}, false, fmt.Errorf("check builder process: %w", err)
		}
		if alive {
			return store.Binding{}, store.Endpoint{}, resolve.Resolution{}, false, ErrBuilderAlive
		}
	}
	opts.Headless = true
	// A resume re-points the binding at a builder; it keeps its stored
	// writer role, so BindOptions.Role is ignored here.
	opts.Role = b.Role
	resCandidate, err := resolve.ResolveRole(rt.RoleRegistry(), rt.Candidates, resolve.WithSkillGates(ResolveDeps(rt), availability.Gates(AvailabilityDeps(rt)), b.CWD, nil), opts.Candidate, b.WriterRole())
	if err != nil {
		return store.Binding{}, store.Endpoint{}, resolve.Resolution{}, false, err
	}
	if opts.Tier != "" {
		tier := resolve.ResolveTier(opts.Tier, resCandidate.Candidate, rt.RoleRegistry(), b.WriterRole())
		if err := resolve.CheckTierCap(tier, rt.Policy, opts.AllowYolo); err != nil {
			return store.Binding{}, store.Endpoint{}, resolve.Resolution{}, false, err
		}
		opts.Tier = string(tier)
		b.Tier = string(tier)
	} else if b.Tier != "" {
		opts.Tier = b.Tier
	}
	var res2 resolve.Resolution
	pick := *opts
	pick.CWD = b.CWD
	builder, res2, err = resolveBuilder(ctx, rt, nil, pick, opts.Name)
	if err != nil {
		return store.Binding{}, store.Endpoint{}, resolve.Resolution{}, false, err
	}
	res2.RestoredWorktree = res.RestoredWorktree
	res2.RestoredBranch = res.RestoredBranch
	res2.WasPaused = res.WasPaused
	return b, builder, res2, true, nil
}

func resumeApplyCommit(rt Runtime, opts BindOptions, mastermindEP store.Endpoint, b store.Binding, builder store.Endpoint, res resolve.Resolution, rebinding, restore bool, ticket string) (store.Binding, resolve.Resolution, error) {
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(opts.Name)
		if err != nil {
			return err
		}
		if rebinding && b.State == store.StateDone && !restore {
			return fmt.Errorf("binding %q is done: `relevo bind` to start fresh", opts.Name)
		}
		// RepoRef and Feature stay untouched beyond the overrides below: a
		// resume re-points endpoints, it does not rediscover facts.
		b.MasterMind = mastermindEP
		if opts.MasterMindID != "" {
			b.MasterMindID = opts.MasterMindID
		}
		if opts.Feature != "" {
			b.Feature = opts.Feature
		} else if opts.NoFeature {
			b.Feature = ""
		}
		if opts.Ticket != "" {
			b.Ticket = ticket
		}
		wasPaused := b.State == store.StatePaused
		// A resume is a fresh attempt, so the previous halt goes with it: the
		// per-round key would dedup the next halt against a reason nobody is
		// looking at. OwedHalt is left alone.
		b = ClearHaltFields(b)
		b.State = store.StateActive
		// A resume is a fresh attempt, so the previous round's progress,
		// stall, exploring and stale stamps go with it.
		b.StalledSince = time.Time{}
		b.Progress = nil
		b.ExploringSince = time.Time{}
		b.StaleSince = time.Time{}
		// A rebind can land mid-round with StreamRound == Round: the
		// stream cursor and segment list move onto the replacement, so
		// the drain keeps rendering the same file from where it left
		// off. Between rounds there is nothing to carry.
		next := builder
		if b.Builder.StreamRound == b.Round {
			next = carryStream(b.Builder, builder)
		}
		if rebinding {
			b.Builder = next
			b.BuilderCandidate = res.Token() // "" when adopting a pane
			b.BuilderAccount = res.Account
			b.BuilderPinned = res.How == resolve.HowExplicit
			b = ClearHaltFields(b)
			// The owed notification goes with the halt: the replacement is a
			// fresh attempt, and the old entry would notify a halt of a round
			// nobody is running.
			b.OwedHalt = nil
			b.BuilderScreen = ""
			b.BuilderScreenAt = time.Time{}
			b.RoundClosedTree = ""
			if opts.Tier != "" {
				b.Tier = opts.Tier
			}
		}
		if err := tx.Save(b); err != nil {
			return err
		}
		if rebinding && res.How != "" {
			if err := tx.AppendLog(opts.Name, resolve.PickEntry(rt.Now(), b.Round, "builder", res)); err != nil {
				return err
			}
		}
		out = b
		// The resume entry is appended after the pick, so it is the log's
		// last word: the resume is the event, the pick a detail.
		if wasPaused {
			entry := store.LogEntry{Round: b.Round, Direction: store.DirToMasterMind, Kind: store.KindResume, Confirmed: true, Note: "resumed"}
			return tx.AppendLog(opts.Name, entry)
		}
		return nil
	})
	if err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}
	return out, res, nil
}

// resumeRemote is resume's remote-binding path. A remote binding's mode is
// fixed at creation: it never adopts a pane or spawns one, only forwards to
// the server and repoints the mastermind once the server agrees.
//
// Errors: "cannot change a remote builder; unbind and add" on --rebind,
// --candidate or --headless; the label refusal on --feature, --no-feature or
// --ticket, which has no server endpoint and would leave the mirror
// disagreeing; ErrRemoteUnavailable; a wrapped server or git error; a message
// naming the binding when its branch is gone.
func resumeRemote(ctx context.Context, rt Runtime, opts BindOptions, mastermindEP store.Endpoint, b store.Binding) (store.Binding, resolve.Resolution, error) {
	if opts.Rebind || opts.Candidate != "" || opts.Headless {
		return store.Binding{}, resolve.Resolution{}, refusal.BadInputf("cannot change a remote builder; unbind and add")
	}
	// --feature/--no-feature/--ticket have no server endpoint, so a remote
	// resume refuses them rather than changing only the mirror.
	if opts.Feature != "" || opts.NoFeature || opts.Ticket != "" {
		return store.Binding{}, resolve.Resolution{}, refusal.BadInputf("cannot change a remote binding's feature or ticket; unbind and add")
	}
	if rt.Remote == nil {
		return store.Binding{}, resolve.Resolution{}, ErrRemoteUnavailable
	}

	if _, rerr := rt.Remote.Resume(ctx, b.Builder.Server, b.Name); rerr != nil {
		var httpErr *client.HTTPError
		if errors.As(rerr, &httpErr) {
			return store.Binding{}, resolve.Resolution{}, fmt.Errorf("%s: %s", b.Builder.Server, httpErr.Body.Message)
		}
		if errors.Is(rerr, client.ErrUnreachable) {
			return store.Binding{}, resolve.Resolution{}, fmt.Errorf("%s unreachable: %w", b.Builder.Server, rerr)
		}
		return store.Binding{}, resolve.Resolution{}, fmt.Errorf("%s: %w", b.Builder.Server, rerr)
	}

	if rt.Git == nil {
		return store.Binding{}, resolve.Resolution{}, errors.New("git unavailable; cannot verify the branch")
	}
	exists, berr := rt.Git.BranchExists(ctx, opts.CWD, b.Branch)
	if berr != nil {
		return store.Binding{}, resolve.Resolution{}, fmt.Errorf("check branch %s: %w", b.Branch, berr)
	}
	if !exists {
		return store.Binding{}, resolve.Resolution{}, fmt.Errorf("binding %q: branch %s is gone; relevo unbind, then relevo bind --server to start fresh", opts.Name, b.Branch)
	}

	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(opts.Name)
		if err != nil {
			return err
		}
		cur.MasterMind = mastermindEP
		if opts.MasterMindID != "" {
			cur.MasterMindID = opts.MasterMindID
		}
		cur = ClearHaltFields(cur)
		cur.State = store.StateActive
		if err := tx.Save(cur); err != nil {
			return err
		}
		out = cur
		return nil
	})
	if err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}

	return out, resolve.Resolution{}, nil
}
