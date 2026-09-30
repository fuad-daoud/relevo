package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// AddOptions describes one peer-builder request.
type AddOptions struct {
	Name      string // name for the new binding; required, must be free
	Candidate string // candidate harness/provider/model token; empty means resolve by role through resolveCandidate
	// MasterMindID is the caller's --mastermind value when it has one, and the
	// resolved record's id afterwards. Empty means "resolve this session's
	// mastermind" (§4.3).
	MasterMindID string
	Repo         string // the repository the worktree is cut from; the caller's cwd

	// CWD binds the peer to a directory the human already prepared instead of
	// creating a worktree. It is the escape hatch for a non-git tree; relevo
	// records no Worktree for it and will never remove it.
	CWD string

	// Branch is an existing branch to check out into relevo's own worktree
	// (local name, e.g. "feature/api-auth"); "" = cut relevo/<name> as today.
	// Mutually exclusive with CWD. Name may be "" only when Branch is set
	// (see DefaultBindingName).
	Branch string

	// Headless makes the peer's builder a process relevo runs per round
	// instead of a pane (#99). Passed through to resolveBuilder.
	Headless bool

	// Server hosts the builder on a remote relevo server (§4.4).
	Server string

	// Local runs the builder here, whatever the actor's placement says. It is
	// the --local flag, and it is refused with Server on the command line.
	Local bool

	// Placement is a choice the caller already made, so Add does not probe
	// again. The zero value means "resolve it here": an explicit Server or
	// Local decides without a probe, and otherwise the actor's own list does.
	Placement PlacementResolution

	// Base commit or ref to branch from; defaults to HEAD.
	Base string

	// Tier overrides the candidate/policy permission tier (#141).
	Tier string

	// AllowYolo permits Tier == "yolo" above policy max_tier for this command (#141).
	AllowYolo bool

	// Gate is the acceptance command relevo runs on the binding's completion
	// marker (#132). Empty means fall back to policy.json's gate.default,
	// unless NoGate opts out of that default.
	Gate string

	// NoGate opts this binding out of policy.json's gate.default even when
	// Gate is empty (#132). Ignored when Gate is set.
	NoGate bool

	// Regate is the binding's automatic repair-round budget (#132 part 2):
	// nil falls back to policy.json's gate.regate, an explicit 0 disables
	// repair even when the policy sets one.
	Regate *int

	// Feature is the human-given label grouping this binding with others
	// (#172); "" means ungrouped. Validated by store.ValidFeature when set.
	Feature string

	// Ticket is the issue this binding serves, as typed on --ticket (#637).
	// Add parses it once against opts.Repo and stores the canonical form; ""
	// means none. There is no --no-feature: nothing to clear, the mark is
	// CLI-only and only a fresh bind needs it.
	Ticket string

	// Role is the writer role the new binding runs (#382); "" means builder.
	// It must name a writer role in roles.json.
	Role string
}

// AddResult is what an add produced, so the CLI can tell the human where the
// new tree and branch are without re-deriving them.
type AddResult struct {
	Binding  store.Binding
	Worktree string // "" when --cwd was used
	Branch   string // "" when --cwd was used
	Base     string // commit the worktree was cut from; "" when --cwd was used

	// Resolution is how the builder was chosen, for the pick line.
	Resolution Resolution
}

// requireRemoteReaders refuses a reader bind against a server that does not
// advertise remote.FeatureReaders. The server is the side that runs the
// reader, so a server without the feature would run it as a writer: the
// client refuses before creating anything, and the message keeps the old
// local-only wording and names the server.
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
// Preconditions:  a relevo mastermind resolves for the caller (--mastermind,
//
//	$RELEVO_MASTERMIND, the host process, or the session); opts.Name is valid
//	and unused; opts.Candidate is resolvable to builder; opts.Repo is a git
//	repository unless opts.CWD is given.
//
// Postconditions: on success a new binding exists at round 1, in StateActive,
//
//	with a running builder and its own working tree. On ANY
//	error, no binding exists and any worktree Add created has
//	been removed.
//
// Errors: ErrGitRequired, store.ErrCWDTaken, git.ErrBranchExists,
//
//	or a wrapped git failure.
func Add(ctx context.Context, rt Runtime, opts AddOptions) (AddResult, error) {
	// Resolve the caller's mastermind before the --server branch. A remote
	// binding is the calling mastermind's, exactly like a local one: its id and
	// session go into the client-side record addRemote writes. Only the
	// server-side binding stays mastermind-less -- nothing about the mastermind
	// crosses the wire (§4.4).
	rec, haveRec, err := resolveVerbMasterMind(rt, opts.MasterMindID)
	if err != nil {
		return AddResult{}, err
	}
	// #637: the ticket is parsed once, before the local/remote split, so the
	// local literal and addRemote both carry the stored form.
	if opts.Ticket != "" {
		ticket, terr := parseTicket(opts.Ticket, captureRepo(ctx, rt, opts.Repo))
		if terr != nil {
			return AddResult{}, terr
		}
		opts.Ticket = ticket
	}
	if opts.Server != "" {
		// A reader runs on the server too (#607): the server cuts the round's
		// scratch worktree and serves the closed round's artifacts, so a
		// reader bind needs a server that advertises remote.FeatureReaders.
		// A server that predates it would run the reader as a writer and
		// touch the served tree, so the refusal here is feature-gated. A role
		// this client's registry does not know is left to the server's own
		// check.
		if shape, rerr := actorShape(rt.RoleRegistry(), opts.Role); rerr == nil && shape == store.ShapeReader {
			if err := requireRemoteReaders(ctx, rt, opts.Server); err != nil {
				return AddResult{}, err
			}
			// A reader round has no check, so the writer-only knobs are
			// refused here too, naming the flag, exactly as the local path
			// below does.
			if opts.Gate != "" {
				return AddResult{}, errors.New("--gate: a reader round has no check")
			}
			if opts.Regate != nil {
				return AddResult{}, errors.New("--regate: a reader round has no check")
			}
		}
		// A caller that resolved the actor's list (a plain bind that landed
		// here) passes its choice in; only a bare --server is explicitly
		// chosen here.
		if opts.Placement.How == "" {
			opts.Placement = PlacementResolution{Name: opts.Server, How: placementHowExplicit}
		}
		return addRemote(ctx, rt, opts, rec, haveRec)
	}
	// The placement decides the rest of the path, once, before anything is
	// created: an explicit --local is taken as given, and otherwise the
	// actor's own list is probed in order. A caller that already resolved
	// passes its choice in, so nothing is probed twice.
	if opts.Placement.How == "" {
		placement, perr := createPlacement(ctx, rt,
			bindingRole(store.Binding{Role: normRole(opts.Role)}), opts.Candidate, "", opts.Local,
			placementFlags{Tier: opts.Tier, Feature: opts.Feature, Ticket: opts.Ticket})
		if perr != nil {
			return AddResult{}, perr
		}
		opts.Placement = placement
	}
	if !opts.Placement.local() {
		opts.Server = opts.Placement.Name
		if shape, rerr := actorShape(rt.RoleRegistry(), opts.Role); rerr == nil && shape == store.ShapeReader {
			if err := requireRemoteReaders(ctx, rt, opts.Server); err != nil {
				return AddResult{}, err
			}
			if opts.Gate != "" {
				return AddResult{}, errors.New("--gate: a reader round has no check")
			}
			if opts.Regate != nil {
				return AddResult{}, errors.New("--regate: a reader round has no check")
			}
		}
		return addRemote(ctx, rt, opts, rec, haveRec)
	}
	// A binding runs one actor, writer or reader (A5 §2). Refuse an unknown
	// one here, on the local path only: a remote binding's actor is resolved
	// against the server's own roles.json, never this client's (§5.3).
	shape, err := actorShape(rt.RoleRegistry(), opts.Role)
	if err != nil {
		return AddResult{}, err
	}
	// A reader round has no check, so the writer-only knobs are refused at
	// add, naming the flag.
	if shape == store.ShapeReader {
		if opts.Gate != "" {
			return AddResult{}, errors.New("--gate: a reader round has no check")
		}
		if opts.Regate != nil {
			return AddResult{}, errors.New("--regate: a reader round has no check")
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
	// Refuse here, not just inside resolveBuilder: Add cuts its worktree before
	// that runs, and a name relevo would refuse must not leave a worktree
	// behind. The composed name is discarded -- resolveBuilder recomputes it.
	if _, err := builderAgentName(opts.Name); err != nil {
		return AddResult{}, err
	}
	// Resolve before AddWorktree for the same reason builderAgentName runs
	// here -- a refused add must leave no worktree.
	roleName := bindingRole(store.Binding{Role: normRole(opts.Role)})
	res, err := resolveRole(rt.RoleRegistry(), rt.Candidates, availability.Gates(AvailabilityDeps(rt)), opts.Candidate, roleName, pickFor(rt))
	if err != nil {
		return AddResult{}, err
	}
	// The placement rides on the resolution so the pick note and the added
	// line say where the binding landed and what it passed over.
	res.Placement = opts.Placement
	c := res.Candidate

	tier := resolveRoleTier(opts.Tier, c, rt.RoleRegistry(), roleName)
	if shape == store.ShapeReader {
		var tierErr error
		tier, tierErr = readerTier(tier, c.Harness, rt.Policy)
		if tierErr != nil {
			return AddResult{}, tierErr
		}
	}
	if err := checkTierCap(tier, rt.Policy, opts.AllowYolo); err != nil {
		return AddResult{}, err
	}

	if !haveRec {
		return AddResult{}, ErrNoMasterMindSession
	}
	opts.MasterMindID = rec.ID
	mastermindEP := recordEndpoint(rec)
	if mastermindEP.TranscriptLocator == "" {
		mastermindEP.TranscriptLocator = mastermindLocator(rt, mastermindEP.Kind, mastermindEP.SessionID)
	}

	if _, err := rt.Store.Load(opts.Name); err == nil {
		return AddResult{}, fmt.Errorf(
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

	if opts.Branch != "" && opts.CWD != "" {
		return AddResult{}, errors.New("--branch and --cwd are exclusive")
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
		if err := branchDrivenByLiveBinding(rt, opts.Branch); err != nil {
			return AddResult{}, err
		}
		exists, err := rt.Git.BranchExists(ctx, opts.Repo, opts.Branch)
		if err != nil {
			return AddResult{}, err
		}
		createdTracking := false
		if !exists {
			originRef := "origin/" + opts.Branch
			_, ok, err := rt.Git.RefSHA(ctx, opts.Repo, "refs/remotes/"+originRef)
			if err != nil {
				return AddResult{}, err
			}
			if !ok {
				return AddResult{}, fmt.Errorf("branch %q not found locally or on origin", opts.Branch)
			}
			if err := rt.Git.CreateTrackingBranch(ctx, opts.Repo, opts.Branch, originRef); err != nil {
				return AddResult{}, err
			}
			createdTracking = true
		}
		tip, ok, err := rt.Git.RefSHA(ctx, opts.Repo, "refs/heads/"+opts.Branch)
		if err != nil {
			return AddResult{}, err
		}
		if !ok {
			return AddResult{}, fmt.Errorf("branch %q vanished", opts.Branch)
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
		// The cut is one helper, shared with a chain's start: --base when
		// given, HEAD otherwise, a named base that does not resolve refused
		// before any worktree exists, and the branch relevo/<name> checked
		// before it is created.
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
		// A branch Add cut itself (the cut path only: --cwd and --branch
		// adopt a branch that already existed) is removed too, so a retry
		// does not fail with "branch already exists" (#437). DeleteBranch is
		// idempotent on a missing branch, and a failure here is ignored
		// exactly like the worktree removal's.
		if createdBranch && rt.Git != nil {
			_ = rt.Git.DeleteBranch(ctx, opts.Repo, branch)
		}
	}

	other, found, err := rt.Store.FindByCWD(cwd)
	if err != nil && !errors.Is(err, store.ErrAmbiguousCWD) {
		rollback()
		return AddResult{}, err
	}
	// Only a writer is refused here: a reader may share a writer's tree
	// (A5 §3), so the refusal applies between two writers only.
	if shape == store.ShapeWriter && found && other.Name != opts.Name && other.State != store.StateDone && other.Shape == store.ShapeWriter {
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
		Role:         normRole(opts.Role),
	}
	// Discard resolveBuilder's own resolution: bindOpts.Candidate is already
	// pinned to c (explicit), so resolveBuilder's internal resolveCandidate
	// call would report HowExplicit and lose the real How/Position/Skipped
	// this function resolved above -- res, from the pre-worktree resolution,
	// is what the pick entry and AddResult.Resolution must carry.
	builder, _, err := resolveBuilder(ctx, rt, nil, bindOpts, opts.Name)
	if err != nil {
		rollback()
		return AddResult{}, err
	}

	// RoundCap, RoundTimeoutMS, CreatedAt and UpdatedAt are all left zero on
	// purpose: store.Save fills them in (internal/store/store.go:269-279), so a
	// peer builder gets exactly the same defaults a plain `relevo bind` does.
	b := store.Binding{
		Name:             opts.Name,
		CWD:              cwd,
		MasterMind:       mastermindEP,
		MasterMindID:     opts.MasterMindID,
		Builder:          builder,
		BuilderCandidate: c.Ref().String(),
		BuilderAccount:   res.Account,
		Round:            1,
		State:            store.StateActive,
		Worktree:         worktree,
		Branch:           branch,
		Base:             base,
		BaseRef:          baseRef,
		ExistingBranch:   existingBranch,
		Repo:             opts.Repo,
		Tier:             string(tier),
		Role:             normRole(opts.Role),
		Shape:            shape,
		Gate:             resolveGateFor(opts.Gate, opts.NoGate, rt.Policy, roleChecks(rt.RoleRegistry(), roleName)),
		Regate:           resolveRegate(opts.Regate, rt.Policy),
		// captureRepo runs against opts.Repo, not cwd: opts.Repo is the
		// parent checkout the worktree is cut from (its git identity is
		// what the coming history database wants), while cwd is the fresh
		// worktree/peer directory -- a linked worktree reports the same
		// facts once AddWorktree has run, but opts.Repo is always a real
		// checkout, including on the --cwd escape hatch.
		RepoRef: captureRepo(ctx, rt, opts.Repo),
		Feature: opts.Feature,
		Ticket:  opts.Ticket,
	}

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(b); err != nil {
			return err
		}
		if res.How != "" {
			return tx.AppendLog(b.Name, pickEntry(rt.Now(), 1, "builder", res))
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
// relevo/<name> from base (else HEAD), at the store's worktree path. It is the
// one cut both `add` and a chain's start run, so the two can never drift.
//
// Preconditions: rt.Git is set. A named base that does not resolve and a
// branch that already exists are refused before anything is created.
//
// Postconditions: the worktree and its branch exist at the returned commit;
// baseRef names the branch the cut came from, or "" when CurrentBranch could
// not answer.
func cutWorktree(ctx context.Context, rt Runtime, name, repo, base string) (worktree, branch, commit, baseRef string, err error) {
	if rt.Git == nil {
		return "", "", "", "", ErrGitRequired
	}
	// The cut's starting commit: --base when given, HEAD otherwise. A named
	// base that does not resolve is refused here, before any worktree exists;
	// the ref is resolved to a commit, so the branch is cut from exactly what
	// the caller named.
	if base != "" {
		sha, ok, rerr := rt.Git.RefSHA(ctx, repo, base)
		if rerr != nil {
			return "", "", "", "", rerr
		}
		if !ok {
			return "", "", "", "", fmt.Errorf("base %q not found", base)
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
	// The branch the cut came from, for `relevo land` (#136). A --cwd or
	// --branch binding records none: nothing was cut, so there is no branch
	// to rebase onto and land asks for --onto instead.
	if ref, cerr := rt.Git.CurrentBranch(ctx, repo); cerr == nil {
		baseRef = ref
	}
	return worktree, branch, commit, baseRef, nil
}

// DefaultBindingName derives a binding name from an existing branch: the last
// path segment, lowercased, with every run of characters store.ValidName does
// not accept replaced by one "-", then trimmed of "-". store.ValidName's own
// error is returned unchanged, so the CLI can say "pass --name".
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

// branchDrivenByLiveBinding reports the error naming the live binding that
// already drives branch, or nil when no binding in a non-DONE state holds it.
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
