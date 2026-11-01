package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
)

// AddOptions describes one peer-builder request.
type AddOptions struct {
	Name      string // name for the new binding; required, must be free
	Candidate string // candidate harness/provider/model token; empty means resolve by role
	// MasterMindID is the caller's --mastermind value when it has one, else the
	// resolved record's id. Empty means "resolve this session's mastermind".
	MasterMindID string
	Repo         string // Repo is the repository the worktree is cut from; the caller's cwd

	// CWD binds the peer to a prepared directory instead of creating a
	// worktree: the escape hatch for a non-git tree. Relevo records no
	// Worktree for it and never removes it.
	CWD string

	// Branch is an existing branch to check out into relevo's own worktree
	// (local name, e.g. "feature/api-auth"); "" cuts relevo/<name>.
	// Mutually exclusive with CWD. Name may be "" only when Branch is set
	// (see DefaultBindingName).
	Branch string

	// Headless makes the peer's builder a process relevo runs per round
	// instead of a pane.
	Headless bool

	// Server hosts the builder on a remote relevo server.
	Server string

	// Local runs the builder here, whatever the actor's placement says;
	// refused with Server.
	Local bool

	// Placement is a choice the caller already made, so Add does not probe
	// again. Zero means "resolve it here".
	Placement resolve.PlacementResolution

	// Base is the commit or ref to branch from; defaults to HEAD.
	Base string

	// Tier overrides the candidate/policy permission tier.
	Tier string

	// AllowYolo permits Tier == "yolo" above policy max_tier for this command.
	AllowYolo bool

	// Gate is the acceptance command relevo runs on the binding's completion
	// marker. Empty means policy.json's gate.default, unless NoGate opts out.
	Gate string

	// NoGate opts this binding out of policy.json's gate.default even when
	// Gate is empty. Ignored when Gate is set.
	NoGate bool

	// Regate is the binding's automatic repair-round budget: nil falls back
	// to policy.json's gate.regate, an explicit 0 disables repair even when
	// the policy sets one.
	Regate *int

	// Feature is the human-given label grouping this binding with others;
	// "" means ungrouped. Validated by store.ValidFeature when set.
	Feature string

	// Ticket is the issue this binding serves, as typed on --ticket: a number,
	// #N, project/path#N, an issue key like ABC-123, or an http(s) URL, stored
	// in canonical form; "" means none.
	Ticket string

	// Role is the writer role the new binding runs; "" means builder.
	// It must name a writer role in roles.json.
	Role string
}

// AddResult is what an add produced: the binding and where its tree and
// branch are.
type AddResult struct {
	Binding  store.Binding
	Worktree string // "" when --cwd was used
	Branch   string // "" when --cwd was used
	Base     string // commit the worktree was cut from; "" when --cwd was used

	// Resolution is how the builder was chosen, for the pick line.
	Resolution resolve.Resolution
}

// requireRemoteReaders refuses a reader bind against a server without
// remote.FeatureReaders, which would run the reader as a writer.
func requireRemoteReaders(ctx context.Context, rt Runtime, server string) error {
	if rt.Remote == nil {
		return ErrRemoteUnavailable
	}
	who, err := rt.Remote.WhoAmI(ctx, server)
	if err != nil {
		return err
	}
	if !slices.Contains(who.Features, remote.FeatureReaders) {
		return fmt.Errorf("reader actors run locally only; bind without --server (server %s predates remote readers; upgrade it)", server)
	}
	return nil
}

// Add attaches an additional builder to the calling mastermind, on its own tree.
//
// On ANY error, no binding exists and any worktree Add created has been
// removed.
//
// Errors: ErrGitRequired, store.ErrCWDTaken, git.ErrBranchExists,
// or a wrapped git failure.
func Add(ctx context.Context, rt Runtime, opts AddOptions) (AddResult, error) {
	// Resolve the caller's mastermind first: a remote binding is the calling
	// mastermind's, exactly like a local one, and nothing about it crosses
	// the wire.
	rec, haveRec, err := resolveVerbMasterMind(rt, opts.MasterMindID)
	if err != nil {
		return AddResult{}, err
	}
	// Parse the ticket once, before the local/remote split, so both paths
	// carry the stored form.
	if opts.Ticket != "" {
		ticket, terr := parseTicket(opts.Ticket, captureRepo(ctx, rt, opts.Repo))
		if terr != nil {
			return AddResult{}, terr
		}
		opts.Ticket = ticket
	}
	if opts.Server != "" {
		// A reader runs on the server too, so the bind needs one advertising
		// remote.FeatureReaders: an older server would run it as a writer and
		// touch the served tree. An unknown role is left to the server.
		if shape, rerr := resolve.ActorShape(rt.RoleRegistry(), opts.Role); rerr == nil && shape == store.ShapeReader {
			if err := requireRemoteReaders(ctx, rt, opts.Server); err != nil {
				return AddResult{}, err
			}
			// A reader round has no check, so the writer-only knobs are
			// refused here too.
			if opts.Gate != "" {
				return AddResult{}, refusal.BadInputf("--gate: a reader round has no check")
			}
			if opts.Regate != nil {
				return AddResult{}, refusal.BadInputf("--regate: a reader round has no check")
			}
		}
		// A caller that resolved the actor's list passes its choice in; only
		// a bare --server is chosen here.
		if opts.Placement.How == "" {
			opts.Placement = resolve.PlacementResolution{Name: opts.Server, How: resolve.PlacementExplicit}
		}
		return addRemote(ctx, rt, opts, rec, haveRec)
	}
	// The placement is decided once, before anything is created: an explicit
	// --local is taken as given, otherwise the actor's own list is probed.
	if opts.Placement.How == "" {
		placement, perr := resolve.CreatePlacement(ctx, ResolveDeps(rt),
			store.Binding{Role: resolve.NormRole(opts.Role)}.WriterRole(), opts.Candidate, "", opts.Local,
			resolve.PlacementFlags{Tier: opts.Tier, Feature: opts.Feature, Ticket: opts.Ticket})
		if perr != nil {
			return AddResult{}, perr
		}
		opts.Placement = placement
	}
	if !opts.Placement.Local() {
		opts.Server = opts.Placement.Name
		if shape, rerr := resolve.ActorShape(rt.RoleRegistry(), opts.Role); rerr == nil && shape == store.ShapeReader {
			if err := requireRemoteReaders(ctx, rt, opts.Server); err != nil {
				return AddResult{}, err
			}
			if opts.Gate != "" {
				return AddResult{}, refusal.BadInputf("--gate: a reader round has no check")
			}
			if opts.Regate != nil {
				return AddResult{}, refusal.BadInputf("--regate: a reader round has no check")
			}
		}
		return addRemote(ctx, rt, opts, rec, haveRec)
	}
	// An unknown actor is refused here, on the local path only: a remote
	// binding's actor resolves against the server's own roles.json.
	shape, err := resolve.ActorShape(rt.RoleRegistry(), opts.Role)
	if err != nil {
		return AddResult{}, err
	}
	// A reader round has no check, so the writer-only knobs are refused at
	// add, naming the flag.
	if shape == store.ShapeReader {
		if opts.Gate != "" {
			return AddResult{}, refusal.BadInputf("--gate: a reader round has no check")
		}
		if opts.Regate != nil {
			return AddResult{}, refusal.BadInputf("--regate: a reader round has no check")
		}
	}
	if err := store.ValidName(opts.Name); err != nil {
		return AddResult{}, err
	}
	if opts.Feature != "" {
		if err := store.ValidFeature(opts.Feature); err != nil {
			return AddResult{}, err
		}
	}
	// Refuse here, not just inside resolveBuilder: Add cuts its worktree
	// before that runs, and a refused name must not leave one behind.
	if _, err := builderAgentName(opts.Name); err != nil {
		return AddResult{}, err
	}
	// Resolve before AddWorktree for the same reason builderAgentName runs
	// here -- a refused add must leave no worktree.
	roleName := store.Binding{Role: resolve.NormRole(opts.Role)}.WriterRole()
	// The repository's own settings, read before anything is created: the
	// gate tier reads them, and opts.Repo -- not cwd -- is the tree they key.
	repoSet, err := RepoSettingsFor(ctx, rt, opts.Repo)
	if err != nil {
		return AddResult{}, err
	}
	res, err := resolve.ResolveRole(rt.RoleRegistry(), rt.Candidates, resolve.AddSkillGates(ctx, ResolveDeps(rt), opts.CWD, opts.Repo, opts.Branch, opts.Base), opts.Candidate, roleName, resolve.PickFor(ResolveDeps(rt)))
	if err != nil {
		return AddResult{}, err
	}
	// The placement rides on the resolution, so the pick entry says where the
	// binding landed.
	res.Placement = opts.Placement
	// The gate resolution rides on the resolution too, so the CLI can print
	// which tier supplied the command the binding stored.
	res.Gate = resolveGateFor(opts.Gate, opts.NoGate, rt.Policy, resolve.RoleChecks(rt.RoleRegistry(), roleName), repoSet)
	c := res.Candidate

	tier := resolve.ResolveTier(opts.Tier, c, rt.RoleRegistry(), roleName)
	if shape == store.ShapeReader {
		var tierErr error
		tier, tierErr = resolve.ReaderTier(tier, c.Harness, rt.Policy)
		if tierErr != nil {
			return AddResult{}, tierErr
		}
	}
	if err := resolve.CheckTierCap(tier, rt.Policy, opts.AllowYolo); err != nil {
		return AddResult{}, err
	}

	if !haveRec {
		return AddResult{}, ErrNoMasterMindSession
	}
	opts.MasterMindID = rec.ID
	mastermindEP := recordEndpoint(rec)

	if _, err := rt.Store.Load(opts.Name); err == nil {
		return AddResult{}, refusal.BadInputf(
			"binding %q already exists: `relevo unbind %s` to start fresh, or `relevo bind --resume --name %s` to adopt it",
			opts.Name, opts.Name, opts.Name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return AddResult{}, err
	}

	var (
		cwd            string
		worktree       string
		branch         string
		base           string
		baseRef        string
		existingBranch bool
		createdBranch  bool
	)

	// The subdirectory the round runs in, resolved once: a bind started below
	// the root offsets its round by the prefix. --cwd never consults it.
	subdir := ""
	if opts.CWD == "" && rt.Git != nil {
		subdir = housekeep.ResolveSubdir(ctx, rt.Git, opts.Repo)
	}

	if opts.Branch != "" && opts.CWD != "" {
		return AddResult{}, refusal.BadInputf("--branch and --cwd are exclusive")
	}

	if opts.CWD != "" {
		info, err := os.Stat(opts.CWD)
		if err != nil {
			return AddResult{}, fmt.Errorf("stat %s: %w", opts.CWD, err)
		}
		if !info.IsDir() {
			return AddResult{}, fmt.Errorf("%s is not a directory", opts.CWD)
		}
		cwd = opts.CWD
	} else if opts.Branch != "" {
		if rt.Git == nil {
			return AddResult{}, ErrGitRequired
		}
		tip, createdTracking, err := resolveAdoptedBranch(ctx, rt, opts.Repo, opts.Branch)
		if err != nil {
			return AddResult{}, err
		}
		cwd = rt.Store.WorktreePath(opts.Name)
		if err := rt.Git.CheckoutWorktree(ctx, opts.Repo, cwd, opts.Branch); err != nil {
			if errors.Is(err, git.ErrBranchCheckedOut) {
				return AddResult{}, fmt.Errorf("branch %s is checked out in another worktree (git worktree list); free it first", opts.Branch)
			}
			if createdTracking {
				return AddResult{}, fmt.Errorf("%w; local branch %s now tracks origin/%s", err, opts.Branch, opts.Branch)
			}
			return AddResult{}, err
		}
		worktree, branch, base, existingBranch = cwd, opts.Branch, tip, true
	} else {
		if rt.Git == nil {
			return AddResult{}, ErrGitRequired
		}
		// One cut shared with a chain's start, so the two can never drift.
		cwd, branch, base, baseRef, err = cutWorktree(ctx, rt, opts.Name, opts.Repo, opts.Base)
		if err != nil {
			return AddResult{}, err
		}
		worktree = cwd
		createdBranch = true
	}

	rollback := func() {
		if worktree != "" && rt.Git != nil {
			_ = rt.Git.RemoveWorktree(ctx, opts.Repo, worktree, true)
		}
		// A branch Add cut itself is removed too, so a retry does not fail
		// with "branch already exists".
		if createdBranch && rt.Git != nil {
			_ = rt.Git.DeleteBranch(ctx, opts.Repo, branch)
		}
	}

	// The cut commit must hold the subdirectory, or every round would start
	// in a path that does not exist.
	if subdir != "" {
		exists, derr := rt.Git.DirAt(ctx, opts.Repo, base, subdir)
		if derr != nil {
			rollback()
			return AddResult{}, derr
		}
		if !exists {
			rollback()
			return AddResult{}, refusal.New("subdirectory %s does not exist at %s: nothing to run the round in", subdir, base)
		}
	}

	other, found, err := rt.Store.FindByCWD(cwd)
	if err != nil && !errors.Is(err, store.ErrAmbiguousCWD) {
		rollback()
		return AddResult{}, err
	}
	// Only a writer is refused here: a reader may share a writer's tree,
	// so the refusal applies between two writers only.
	if shape == store.ShapeWriter && found && other.Name != opts.Name && other.State != store.StateDone && other.Shape == store.ShapeWriter && !rt.Store.SameChain(opts.Name, other.Name) {
		rollback()
		return AddResult{}, fmt.Errorf("%s is driven by binding %q (builder %s, round %d): %w",
			cwd, other.Name, other.BuilderCandidate, other.Round, store.ErrCWDTaken)
	}

	bindOpts := BindOptions{
		Name:         opts.Name,
		Candidate:    c.Ref().String(),
		MasterMindID: opts.MasterMindID,
		CWD:          cwd,
		Headless:     opts.Headless,
		Tier:         string(tier),
		AllowYolo:    opts.AllowYolo,
		Role:         resolve.NormRole(opts.Role),
	}
	// Discard resolveBuilder's own resolution: it would report HowExplicit and
	// lose the How/Position/Skipped resolved above.
	builder, _, err := resolveBuilder(ctx, rt, nil, bindOpts, opts.Name)
	if err != nil {
		rollback()
		return AddResult{}, err
	}

	// RoundCap, RoundTimeoutMS, CreatedAt and UpdatedAt stay zero: store.Save
	// fills them, so a peer builder gets a plain bind's defaults.
	b := store.Binding{
		Name:             opts.Name,
		CWD:              cwd,
		Subdir:           subdir,
		MasterMind:       mastermindEP,
		MasterMindID:     opts.MasterMindID,
		Builder:          builder,
		BuilderCandidate: c.Ref().String(),
		BuilderAccount:   res.Account,
		BuilderPinned:    res.How == resolve.HowExplicit,
		Round:            1,
		State:            store.StateActive,
		Worktree:         worktree,
		Branch:           branch,
		Base:             base,
		BaseRef:          baseRef,
		ExistingBranch:   existingBranch,
		Repo:             opts.Repo,
		Tier:             string(tier),
		Role:             resolve.NormRole(opts.Role),
		Shape:            shape,
		Gate:             res.Gate.Command,
		Regate:           ResolveRegate(opts.Regate, rt.Policy),
		// captureRepo runs against opts.Repo, not cwd: only it is always a
		// real checkout, including on the --cwd escape hatch.
		RepoRef: captureRepo(ctx, rt, opts.Repo),
		Feature: opts.Feature,
		Ticket:  opts.Ticket,
	}

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(b); err != nil {
			return err
		}
		if res.How != "" {
			return tx.AppendLog(b.Name, resolve.PickEntry(rt.Now(), 1, "builder", res))
		}
		return nil
	}); err != nil {
		rollback()
		return AddResult{}, fmt.Errorf("add failed after choosing builder %s: %w",
			builder.Kind, err)
	}

	if stored, err := rt.Store.Load(b.Name); err == nil {
		b = stored
	}

	return AddResult{Binding: b, Worktree: worktree, Branch: branch, Base: base, Resolution: res}, nil
}

// cutWorktree cuts relevo's own worktree for a new binding: the branch
// relevo/<name> from base (else HEAD). It is the one cut both `add` and a
// chain's start run, so the two can never drift.
//
// A named base that does not resolve and a branch that already exists are
// refused before anything is created. baseRef is "" when CurrentBranch
// could not answer.
func cutWorktree(ctx context.Context, rt Runtime, name, repo, base string) (worktree, branch, commit, baseRef string, err error) {
	if rt.Git == nil {
		return "", "", "", "", ErrGitRequired
	}
	// --base when given, HEAD otherwise, resolved to a commit before any
	// worktree exists.
	if base != "" {
		sha, ok, rerr := rt.Git.RefSHA(ctx, repo, base)
		if rerr != nil {
			return "", "", "", "", rerr
		}
		if !ok {
			return "", "", "", "", refusal.New("base %q not found", base)
		}
		commit = sha
	} else if commit, err = rt.Git.HeadCommit(ctx, repo); err != nil {
		return "", "", "", "", err
	}
	branch = "relevo/" + name
	exists, err := rt.Git.BranchExists(ctx, repo, branch)
	if err != nil {
		return "", "", "", "", err
	}
	if exists {
		return "", "", "", "", git.ErrBranchExists
	}
	worktree = rt.Store.WorktreePath(name)
	if err := rt.Git.AddWorktree(ctx, repo, worktree, branch, commit); err != nil {
		return "", "", "", "", err
	}
	// The branch the cut came from, for `relevo land`. A --cwd or --branch
	// binding records none: nothing was cut.
	if ref, cerr := rt.Git.CurrentBranch(ctx, repo); cerr == nil {
		baseRef = ref
	}
	return worktree, branch, commit, baseRef, nil
}

// DefaultBindingName derives a binding name from an existing branch: the
// last path segment, lowercased and coerced to store.ValidName's rule.
// store.ValidName's own error is returned unchanged.
func DefaultBindingName(branch string) (string, error) {
	seg := branch
	if i := strings.LastIndex(branch, "/"); i >= 0 {
		seg = branch[i+1:]
	}
	seg = strings.ToLower(seg)

	var sb strings.Builder
	replacing := false
	for _, r := range seg {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			sb.WriteRune(r)
			replacing = false
			continue
		}
		if !replacing {
			sb.WriteByte('-')
			replacing = true
		}
	}

	name := strings.Trim(sb.String(), "-")
	if err := store.ValidName(name); err != nil {
		return "", err
	}
	return name, nil
}

// branchDrivenByLiveBinding names the live binding already driving branch.
// A DONE binding does not block: relevo is finished with it.
func branchDrivenByLiveBinding(rt Runtime, branch string) error {
	bindings, err := rt.Store.List()
	if err != nil {
		return err
	}
	for _, b := range bindings {
		if b.Branch == branch && b.State != store.StateDone {
			return fmt.Errorf("branch %s is driven by binding %q (round %d); relevo done or unbind it first", branch, b.Name, b.Round)
		}
	}
	return nil
}
