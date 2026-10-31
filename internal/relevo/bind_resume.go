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
// the caller supplied a builder, or asked for one with Rebind -- at a new builder as well.
// The binding must exist, and when a builder is supplied the current one must
// not be alive: rebinding over a working builder would abandon a round
// mid-flight.
//
// A rebind of a DONE binding is refused; a mastermind-only resume of one is
// allowed and reactivates it. RoundClosedTree is cleared with a new builder:
// a tree that changed hands says nothing about the builder that left it.
// Round, CWD, Name, RoundBaselineTree and the round log are untouched, and so
// is the binding's mode: a remote binding stays remote, a local one headless.
//
// Errors: store.ErrNotFound; ErrBuilderAlive; ErrRunnerUnavailable; a wrapped
// git or remote error.
func resume(ctx context.Context, rt Runtime, opts BindOptions, mastermindEP store.Endpoint) (store.Binding, resolve.Resolution, error) {
	b, err := resumeLoad(rt, opts.Name)
	if err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}
	if mine, err := handedToMe(rt, opts.Name); err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	} else if mine && carriesOldPaths(rt, b) {
		if b, err = resumeHandedOver(ctx, rt, opts, b); err != nil {
			return store.Binding{}, resolve.Resolution{}, err
		}
	}

	// A remote binding has no pane and no worktree: its mode is fixed at
	// creation (like a headless binding's), so none of the pane/worktree
	// logic below applies to it.
	if b.Builder.Remote() {
		return resumeRemote(ctx, rt, opts, mastermindEP, b)
	}

	// The label flags are applied after the load, because --ticket's
	// repository hint is the binding's own recorded origin.
	if opts.Feature != "" {
		if err := store.ValidFeature(opts.Feature); err != nil {
			return store.Binding{}, resolve.Resolution{}, err
		}
	}
	ticket := ""
	if opts.Ticket != "" {
		ticket, err = parseTicket(opts.Ticket, b.RepoRef)
		if err != nil {
			return store.Binding{}, resolve.Resolution{}, err
		}
	}

	var res resolve.Resolution
	restore := false
	if b.Worktree != "" {
		if _, err := os.Stat(b.Worktree); errors.Is(err, os.ErrNotExist) {
			restore = true
		}
	}

	if restore {
		if b.Branch == "" {
			return store.Binding{}, resolve.Resolution{}, fmt.Errorf("binding %q: worktree %s is gone and no branch is recorded; relevo bind --worktree to start fresh", opts.Name, b.Worktree)
		}
		if rt.Git == nil {
			return store.Binding{}, resolve.Resolution{}, errors.New("git unavailable; cannot restore worktree")
		}
		// An add binding's CWD is the worktree itself (add.go), which is the
		// directory that is gone; the repository it was cut from is not
		// recorded. The caller's cwd is where add ran, so restore from there
		// once the branch is confirmed to live in it.
		exists, err := rt.Git.BranchExists(ctx, opts.CWD, b.Branch)
		if err != nil {
			return store.Binding{}, resolve.Resolution{}, fmt.Errorf("restore worktree: %w", err)
		}
		if !exists {
			return store.Binding{}, resolve.Resolution{}, fmt.Errorf("binding %q: branch %s is not in %s; run resume from the repository the worktree was cut from", opts.Name, b.Branch, opts.CWD)
		}
		if err := rt.Git.CheckoutWorktree(ctx, opts.CWD, b.Worktree, b.Branch); err != nil {
			if errors.Is(err, git.ErrBranchCheckedOut) {
				return store.Binding{}, resolve.Resolution{}, fmt.Errorf("binding %q: branch %s is checked out in another worktree (git worktree list); free it, then resume", opts.Name, b.Branch)
			}
			return store.Binding{}, resolve.Resolution{}, fmt.Errorf("restore worktree: %w", err)
		}
		res.RestoredWorktree = b.Worktree
		res.RestoredBranch = b.Branch
	}

	rebinding := opts.Rebind || opts.Candidate != ""
	// A paused binding has no builder identity left -- pause cleared it -- so
	// a resume from PAUSED always rebinds; --rebind is implied.
	if b.State == store.StatePaused {
		res.WasPaused = true
		rebinding = true
	}

	var (
		builder store.Endpoint
	)
	if rebinding {
		if b.State == store.StateDone && !restore {
			return store.Binding{}, resolve.Resolution{}, fmt.Errorf("binding %q is done: `relevo bind` to start fresh", opts.Name)
		}
		// A binding's mode is fixed at creation. A local builder is a
		// process, so ask the Runner whether its PID is still alive: a PID
		// has no "moved pane" ambiguity.
		if rt.Runner == nil {
			return store.Binding{}, resolve.Resolution{}, spawn.ErrRunnerUnavailable
		}
		if b.Builder.PID != 0 {
			alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
			if err != nil {
				return store.Binding{}, resolve.Resolution{}, fmt.Errorf("check builder process: %w", err)
			}
			if alive {
				return store.Binding{}, resolve.Resolution{}, ErrBuilderAlive
			}
		}
		opts.Headless = true
		// A resume re-points the binding at a builder; it keeps its stored
		// writer role, so BindOptions.Role is ignored here.
		opts.Role = b.Role
		resCandidate, err := resolve.ResolveRole(rt.RoleRegistry(), rt.Candidates, resolve.WithSkillGates(ResolveDeps(rt), availability.Gates(AvailabilityDeps(rt)), b.CWD, nil), opts.Candidate, resolve.BindingRole(b))
		if err != nil {
			return store.Binding{}, resolve.Resolution{}, err
		}
		if opts.Tier != "" {
			tier := resolve.ResolveTier(opts.Tier, resCandidate.Candidate, rt.RoleRegistry(), resolve.BindingRole(b))
			if err := resolve.CheckTierCap(tier, rt.Policy, opts.AllowYolo); err != nil {
				return store.Binding{}, resolve.Resolution{}, err
			}
			opts.Tier = string(tier)
			b.Tier = string(tier)
		} else if b.Tier != "" {
			opts.Tier = b.Tier
		}
		var res2 resolve.Resolution
		pick := opts
		pick.CWD = b.CWD
		builder, res2, err = resolveBuilder(ctx, rt, nil, pick, opts.Name)
		if err != nil {
			return store.Binding{}, resolve.Resolution{}, err
		}
		res2.RestoredWorktree = res.RestoredWorktree
		res2.RestoredBranch = res.RestoredBranch
		res2.WasPaused = res.WasPaused
		res = res2
	}

	var out store.Binding
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(opts.Name)
		if err != nil {
			return err
		}
		if rebinding && b.State == store.StateDone && !restore {
			return fmt.Errorf("binding %q is done: `relevo bind` to start fresh", opts.Name)
		}

		// RepoRef and Feature are deliberately left untouched here (beyond
		// the explicit Feature/Ticket overrides below): a resume re-points
		// endpoints, it does not rediscover facts a fresh bind already
		// captured.
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
		// A resume is a fresh attempt, so the halt the previous one left goes
		// with it: every field of it, or the per-round notification key dedups
		// the next halt of this round against a reason nobody is looking at any
		// more. OwedHalt is left alone, as the other human paths leave it.
		b = ClearHaltFields(b)
		b.State = store.StateActive
		// A resume or a rebind is a fresh attempt, so the previous round's
		// progress clock, stall, exploring and stale stamps say nothing about
		// it.
		b.StalledSince = time.Time{}
		b.Progress = nil
		b.ExploringSince = time.Time{}
		b.StaleSince = time.Time{}
		if rebinding {
			// A rebind can land mid-round: resume refuses only while the
			// current process is alive (ErrBuilderAlive above), so an open
			// round whose process exited rebinds here with
			// StreamRound == Round. The round's stream cursor and its
			// segment list then move onto the replacement, so the drain
			// keeps rendering the same file from where it left off. Between
			// rounds (StreamRound != Round) there is nothing to carry and
			// the fresh endpoint stands.
			if b.Builder.StreamRound == b.Round {
				b.Builder = carryStream(b.Builder, builder)
			} else {
				b.Builder = builder
			}
			b.BuilderCandidate = res.Token() // "" when adopting a pane
			b.BuilderAccount = res.Account
			b.BuilderPinned = res.How == resolve.HowExplicit
			b = ClearHaltFields(b)
			// The owed notification goes with the halt: the replacement builder
			// is a fresh attempt, and an entry the replaced round still owed
			// would be notified as a halt of a round nobody is running.
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

		// The resume entry is appended after the pick so it is the log's
		// last word on the binding: it is the event, the pick is a detail.
		if wasPaused {
			if err := tx.AppendLog(opts.Name, store.LogEntry{
				Round:     b.Round,
				Direction: store.DirToMasterMind,
				Kind:      store.KindResume,
				Confirmed: true,
				Note:      "resumed",
			}); err != nil {
				return err
			}
		}

		out = b
		return nil
	})
	if err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}

	return out, res, nil
}

// resumeRemote is resume's remote-binding path. A remote binding's mode is
// fixed at creation, so this never adopts a pane or spawns one: it only
// forwards to the server and, once the server agrees, repoints the mastermind
// and reactivates the binding locally.
//
// Errors: "cannot change a remote builder; unbind and add" when the caller
// asked to change the builder (--rebind, --candidate, or --headless); the
// label refusal when --feature, --no-feature or --ticket is set -- no server
// endpoint could change a stored label, and a mirror-only change would
// recreate the laptop/server disagreement; ErrRemoteUnavailable; a wrapped
// server error; a wrapped git error; or a message naming the binding when
// its branch is gone.
func resumeRemote(ctx context.Context, rt Runtime, opts BindOptions, mastermindEP store.Endpoint, b store.Binding) (store.Binding, resolve.Resolution, error) {
	if opts.Rebind || opts.Candidate != "" || opts.Headless {
		return store.Binding{}, resolve.Resolution{}, refusal.BadInputf("cannot change a remote builder; unbind and add")
	}
	// --feature/--no-feature/--ticket have no server endpoint to reach,
	// so a remote resume refuses them rather than changing only the mirror.
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
