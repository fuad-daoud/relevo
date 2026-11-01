package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/installation"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
)

func addRemote(ctx context.Context, rt Runtime, opts AddOptions, rec mastermind.Record, haveRec bool) (result AddResult, err error) {
	input, err := validateRemoteAddInput(ctx, rt, &opts, rec, haveRec)
	if err != nil {
		return AddResult{}, err
	}
	base, err := resolveRemoteBaseIdentity(ctx, rt, opts)
	if err != nil {
		return AddResult{}, err
	}
	feat, err := resolveRemoteCandidateFeatures(ctx, rt, opts, input.subdir)
	if err != nil {
		return AddResult{}, err
	}
	mastermindEP := recordEndpoint(rec)

	// The gate resolves exactly as the local path does: with the client's
	// policy, the actor's role and the repository's own settings, read on the
	// client for the tree the branch is cut from. The resolved value rides on
	// the wire -- the field chains already use -- and is stored on the client
	// mirror, so both sides read the same acceptance check.
	createReq := remote.CreateBindingRequest{
		Name:               opts.Name,
		RepoID:             base.repoID,
		BaseCommit:         input.base,
		Candidate:          base.candidate,
		Tier:               feat.wireTier,
		Role:               feat.actor,
		Feature:            opts.Feature,
		Ticket:             opts.Ticket,
		Gate:               feat.gate,
		Regate:             &feat.regate,
		Author:             &remote.GitIdentity{Name: base.authorName, Email: base.authorEmail},
		ClientInstallation: feat.clientInstallation,
		ClientBindingID:    feat.clientBindingID,
		Subdir:             input.subdir,
		Trimpath:           feat.trimpath,
	}
	view, err := rt.Remote.CreateBinding(ctx, opts.Server, createReq)
	if err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == 409 {
			return AddResult{}, fmt.Errorf("binding %s already exists on %s for this client but not here; relevo serve unbind --owner <your label> %s on the server, or choose another name", opts.Name, opts.Server, opts.Name)
		}
		if errors.As(err, &httpErr) && httpErr.Status == 422 && httpErr.Body.Code == remote.CodeTierAboveMax {
			return AddResult{}, fmt.Errorf("%w: %s", resolve.ErrTierAboveMax, httpErr.Body.Message)
		}
		if errors.As(err, &httpErr) && httpErr.Status == 422 && httpErr.Body.Code == remote.CodeInvalid && resolve.TierUnsupportedMessage(httpErr.Body.Message) {
			return AddResult{}, resolve.TierUnsupportedFromWire(httpErr.Body.Message)
		}
		return AddResult{}, pickRefusalFromWire(err)
	}
	// From here on the server will hold a binding this client does not yet.
	// Every later failure unbinds it on the server best-effort before
	// returning the original error, and removes the local branch relevo cut
	// once CreateBranch itself has succeeded.
	branchCreated := false
	defer func() {
		if err == nil {
			return
		}
		if unbindErr := rt.Remote.Unbind(ctx, opts.Server, opts.Name, true); unbindErr != nil {
			slog.Warn("unbind after failed add", "server", opts.Server, "name", opts.Name, "err", unbindErr)
			err = fmt.Errorf("%w; server binding %s on %s could not be removed: %w", err, opts.Name, opts.Server, unbindErr)
		}
		if branchCreated {
			if delErr := rt.Git.DeleteBranch(ctx, opts.Repo, input.branch); delErr != nil {
				slog.Warn("delete branch after failed add", "repo", opts.Repo, "branch", input.branch, "err", delErr)
				err = fmt.Errorf("%w; local branch %s could not be removed: %w", err, input.branch, delErr)
			}
		}
	}()
	return createAndMirrorBinding(ctx, rt, opts, input, base, feat, mastermindEP, view, &branchCreated)
}

// remoteAddInput is the validated local half of a remote add: where the round
// runs and which commit the server cuts its worktree from.
type remoteAddInput struct {
	subdir         string
	branch         string
	base           string
	existingBranch bool
}

// remoteAddBase is the base identity a remote add was resolved to: the repo,
// the author the remote builder commits as, and the candidate token.
type remoteAddBase struct {
	repoID      string
	authorName  string
	authorEmail string
	candidate   string
}

// remoteAddFeatures is what the server probe admitted: the tier and actor to
// send, the resolved gate and repair budget, and the link ids.
type remoteAddFeatures struct {
	wireTier           string
	wireRole           string
	actor              string
	gate               string
	gateRes            resolve.GateResolution
	regate             int
	trimpath           string
	clientInstallation string
	clientBindingID    string
}

func validateRemoteAddInput(ctx context.Context, rt Runtime, opts *AddOptions, rec mastermind.Record, haveRec bool) (remoteAddInput, error) {
	// The caller's mastermind is a hard precondition here exactly as it is on the
	// local path: a remote binding records the client mastermind's id and
	// session, so without a resolved record there is nothing to record and
	// nothing may be created on the server.
	if !haveRec {
		return remoteAddInput{}, ErrNoMasterMindSession
	}
	opts.MasterMindID = rec.ID

	if opts.CWD != "" {
		// The input is simply wrong, so this is the input boundary's refusal
		// class: the CLI renders it as `refused` (exit 2), never `internal`.
		return remoteAddInput{}, refusal.New("remote builders are add-only: --cwd and --server cannot be combined")
	}
	if rt.Remote == nil {
		return remoteAddInput{}, ErrRemoteUnavailable
	}
	if rt.Git == nil {
		return remoteAddInput{}, ErrGitRequired
	}
	var input remoteAddInput
	// The subdirectory the round runs in, resolved from the caller's own
	// checkout: the worktree the server cuts is at the bare repo's root, so
	// the offset the bind was started under travels on the create and the
	// server's own paths join it in.
	input.subdir = housekeep.ResolveSubdir(ctx, rt.Git, opts.Repo)

	if err := store.ValidName(opts.Name); err != nil {
		return remoteAddInput{}, err
	}
	if opts.Feature != "" {
		if err := store.ValidFeature(opts.Feature); err != nil {
			return remoteAddInput{}, err
		}
	}
	if _, err := rt.Store.Load(opts.Name); err == nil {
		return remoteAddInput{}, fmt.Errorf("binding %q already exists locally", opts.Name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return remoteAddInput{}, err
	}

	// 1.5. --branch adopts an existing branch: the same driven-by-live-binding
	// guard and local/origin resolution as Add, and its tip overrides --base.
	// No worktree is made on the client for a remote binding, so there is no
	// CheckoutWorktree here.
	input.branch = "relevo/" + opts.Name
	input.base = opts.Base
	if opts.Branch != "" {
		if opts.Base != "" {
			return remoteAddInput{}, errors.New("--base and --branch are exclusive")
		}
		// Resolved locally before anything below, so the fetch this may run
		// happens before every server call: a refusal or a fetch failure must
		// reach the server never.
		tip, _, err := resolveAdoptedBranch(ctx, rt, opts.Repo, opts.Branch)
		if err != nil {
			return remoteAddInput{}, err
		}
		input.branch = opts.Branch
		input.existingBranch = true
		input.base = tip
	} else {
		if input.base == "" {
			var err error
			input.base, err = rt.Git.HeadCommit(ctx, opts.Repo)
			if err != nil {
				return remoteAddInput{}, fmt.Errorf("head commit: %w", err)
			}
		}
		if !remote.Is40Hex(input.base) {
			sha, ok, err := rt.Git.RefSHA(ctx, opts.Repo, input.base)
			if err != nil {
				return remoteAddInput{}, fmt.Errorf("resolve base %s: %w", input.base, err)
			}
			if !ok {
				return remoteAddInput{}, fmt.Errorf("base %q not found", input.base)
			}
			input.base = sha
		}
	}
	return input, nil
}

func resolveRemoteBaseIdentity(ctx context.Context, rt Runtime, opts AddOptions) (remoteAddBase, error) {
	var base remoteAddBase
	root, err := rt.Git.RootCommit(ctx, opts.Repo)
	if err != nil {
		return remoteAddBase{}, fmt.Errorf("root commit: %w", err)
	}
	base.repoID, err = remote.RepoID(root)
	if err != nil {
		return remoteAddBase{}, fmt.Errorf("repo id: %w", err)
	}

	// 3.5. name, email := rt.Git.Identity(opts.Repo): the identity the remote
	// builder commits as. Resolved here, before the candidate check
	// and before anything that touches the server, so a missing identity
	// refuses with no binding on the server, no local binding and no branch.
	base.authorName, base.authorEmail, err = rt.Git.Identity(ctx, opts.Repo)
	if err != nil {
		return remoteAddBase{}, fmt.Errorf("git identity for %s: %w", opts.Repo, err)
	}
	if base.authorName == "" || base.authorEmail == "" {
		return remoteAddBase{}, fmt.Errorf("%w for %s: a remote builder commits as you; set git config user.name and git config user.email (in the repo or --global)", remote.ErrNoGitIdentity, opts.Repo)
	}

	// A non-empty candidate must be listed by the server, by canonical token
	// or by name; if empty the server picks. The canonical token is what travels.
	base.candidate = opts.Candidate
	if base.candidate != "" {
		candResp, err := rt.Remote.Candidates(ctx, opts.Server)
		if err != nil {
			return remoteAddBase{}, fmt.Errorf("list remote candidates: %w", err)
		}
		found := false
		if view, ok := matchCandidateView(candResp.Candidates, base.candidate); ok {
			base.candidate, found = view.Token, true
		} else if !strings.Contains(base.candidate, "/") {
			// A name this client knows resolves to its canonical token,
			// which the server may list under that token.
			if c, rerr := rt.Candidates.Resolve(base.candidate); rerr == nil {
				if view, ok := matchCandidateView(candResp.Candidates, c.Ref().String()); ok {
					base.candidate, found = view.Token, true
				}
			}
		}
		if !found {
			available := make([]string, len(candResp.Candidates))
			for i, c := range candResp.Candidates {
				available[i] = c.Token
				if c.Name != "" {
					available[i] = fmt.Sprintf("%s (%s)", c.Name, c.Token)
				}
			}
			return remoteAddBase{}, fmt.Errorf("candidate %q not available on %s (available: %s)", opts.Candidate, opts.Server, strings.Join(available, ", "))
		}
	}
	return base, nil
}

func resolveRemoteCandidateFeatures(ctx context.Context, rt Runtime, opts AddOptions, subdir string) (remoteAddFeatures, error) {
	var feat remoteAddFeatures
	// 4.5. feature probe: a requested --tier, label or custom --actor requires
	// the server to advertise the matching feature before any binding is
	// created there, and the link requires FeatureOrigin. One WhoAmI answers
	// all of them, and the add makes exactly one probe.
	if opts.Tier != "" {
		t, err := harness.ParseTier(opts.Tier)
		if err != nil {
			return remoteAddFeatures{}, err
		}
		if err := resolve.CheckTierCap(t, rt.Policy, opts.AllowYolo); err != nil {
			return remoteAddFeatures{}, err
		}
		feat.wireTier = string(t)
	}
	feat.wireRole = resolve.NormRole(opts.Role)
	// The wire always names the actor; a default binding sends "builder".
	feat.actor = feat.wireRole
	if feat.actor == "" {
		feat.actor = "builder"
	}
	who, err := rt.Remote.WhoAmI(ctx, opts.Server)
	if err != nil {
		return remoteAddFeatures{}, err
	}
	if opts.Tier != "" && !slices.Contains(who.Features, remote.FeatureTier) {
		return remoteAddFeatures{}, fmt.Errorf("%w: server %s does not carry a permission tier (pre-tier server); upgrade it or drop --tier", ErrServerPreTier, opts.Server)
	}
	// A server that does not advertise labels would save the binding
	// without them, so a client that sets one refuses -- before any branch,
	// worktree or create call.
	if (opts.Feature != "" || opts.Ticket != "") && !slices.Contains(who.Features, remote.FeatureLabels) {
		return remoteAddFeatures{}, fmt.Errorf("%w: server %s does not carry binding labels (pre-labels server); upgrade it or drop --feature/--ticket", ErrServerPreTier, opts.Server)
	}
	// The same negative applies to a subdirectory the server would silently
	// drop: bind's round would run at the bare repo's root, not where the
	// mastermind's shell was. An empty prefix needs no feature, so a
	// root-started bind meets a pre-subdir server unchanged.
	if subdir != "" && !slices.Contains(who.Features, remote.FeatureSubdir) {
		return remoteAddFeatures{}, fmt.Errorf("%w: server %s does not carry binding subdirectories (pre-subdir server); upgrade it or bind at the repository root", ErrServerPreTier, opts.Server)
	}
	// The client's actors never travel: the server resolves the
	// actor against its own. A server too old to do that would ignore the
	// field and run the default actor, so it is refused here -- before any
	// branch, worktree or create call.
	if feat.wireRole != "" && !slices.Contains(who.Features, remote.FeatureRoles) {
		return remoteAddFeatures{}, fmt.Errorf("server %s does not run custom actors (actor %q); upgrade it", opts.Server, opts.Role)
	}

	// A 409 means the server already has this binding for this client with no
	// local counterpart here; only `relevo serve unbind` on the server can
	// clear that, since a local `relevo bind --resume` has nothing to resume.
	// The two ids the link needs: this client's installation, and a record id
	// minted here so both sides know the client's row from the start. They
	// travel only to a server that advertises FeatureOrigin, which stores them
	// as its row's link; the fields are empty otherwise and the wire omits
	// them, so a server without the feature sees a request it understands.
	if slices.Contains(who.Features, remote.FeatureOrigin) {
		inst, err := installation.Load(filepath.Dir(rt.Store.DBPath()))
		if err != nil {
			return remoteAddFeatures{}, err
		}
		feat.clientInstallation = inst.ID
		feat.clientBindingID = db.NewID()
	}

	// The gate resolves exactly as the local path does: with the client's
	// policy, the actor's role and the repository's own settings, read on the
	// client for the tree the branch is cut from. The resolved value rides on
	// the wire -- the field chains already use -- and is stored on the client
	// mirror, so both sides read the same acceptance check.
	repoSet, err := RepoSettingsFor(ctx, rt, opts.Repo)
	if err != nil {
		return remoteAddFeatures{}, err
	}
	feat.gateRes = resolveGateFor(opts.Gate, opts.NoGate, rt.Policy, resolve.RoleChecks(rt.RoleRegistry(), store.Binding{Role: resolve.NormRole(opts.Role)}.WriterRole()), repoSet)
	feat.gate = feat.gateRes.Command
	// The regate resolves the same way, and for the same reason: the mirror
	// and the served binding must agree on the repair budget.
	feat.regate = ResolveRegate(opts.Regate, rt.Policy)
	feat.trimpath = repoSet.Trimpath
	return feat, nil
}

func createAndMirrorBinding(ctx context.Context, rt Runtime, opts AddOptions, input remoteAddInput, base remoteAddBase, feat remoteAddFeatures, mastermindEP store.Endpoint, view remote.BindingView, branchCreated *bool) (AddResult, error) {
	// The branch is cut after the server agreed, so a refused create leaves no branch.
	// In --branch mode the branch already exists and relevo did not create it:
	// there is nothing to create, and branchCreated stays false so the
	// deferred cleanup above never deletes a branch relevo did not make.
	if !input.existingBranch {
		if err := rt.Git.CreateBranch(ctx, opts.Repo, input.branch, input.base); err != nil {
			if errors.Is(err, git.ErrBranchExists) {
				return AddResult{}, fmt.Errorf("branch relevo/%s exists; delete it or pick another name", opts.Name)
			}
			return AddResult{}, err
		}
		*branchCreated = true
	}

	// Saved under lock with the same pick log entry Add writes, carrying the
	// candidate the server reported.
	builderKind := ""
	var cand candidate.Candidate
	if view.Candidate != "" {
		if ref, err := candidate.ParseRef(view.Candidate); err == nil {
			builderKind = ref.Harness
			cand = candidate.Candidate{Harness: ref.Harness, Provider: ref.Provider, Model: ref.Model}
		}
	}

	b := store.Binding{
		Name:   opts.Name,
		CWD:    opts.Repo,
		Repo:   opts.Repo,
		Branch: input.branch,
		Base:   input.base,
		// ExistingBranch records that relevo adopted a branch it did not
		// create, so nothing here will ever delete it.
		ExistingBranch: input.existingBranch,
		MasterMind:     mastermindEP,
		MasterMindID:   opts.MasterMindID,
		Builder: store.Endpoint{
			Mode:      store.ModeRemote,
			Server:    opts.Server,
			Kind:      builderKind,
			AgentName: opts.Name,
		},
		BuilderCandidate: view.Candidate,
		Round:            1,
		State:            store.StateActive,
		Tier:             view.Tier,
		// The gate the create request carried: the client's resolved check,
		// recorded on the mirror so `relevo status` shows what the server runs.
		Gate: feat.gate,
		// The regate the create request carried, resolved the same way, so
		// the mirror's repair budget matches the served binding's.
		Regate: feat.regate,
		// The actor travels as the client asked it: the server resolved it
		// against its own actors, and the mirror records it so
		// `relevo status` shows it.
		Role: feat.wireRole,
		// The server's Shape decides how the round runs: "reader" for a
		// reader binding, and "" from a server that predates the field, i.e.
		// a writer.
		Shape: view.Shape,
		// rt.Git is guaranteed non-nil here (checked at the top of
		// addRemote), and opts.Repo is the client's local checkout the
		// branch and bundle are cut from -- the same "parent repo" concept
		// captureRepo uses for the local Add path.
		RepoRef: captureRepo(ctx, rt, opts.Repo),
		Feature: opts.Feature,
		Ticket:  opts.Ticket,
		// The server's copy of this binding: its installation and its record
		// id, as the create view reported them. A server that does not
		// advertise FeatureOrigin sends neither, and the link is then nil.
		Link: remote.Link(view),
		// RecordID is the id minted above: the server was told the same id, so
		// the client copy the server recorded is this row.
		RecordID: feat.clientBindingID,
	}
	res := resolve.Resolution{Candidate: cand, How: resolve.HowExplicit, Placement: opts.Placement, Gate: feat.gateRes}

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(b); err != nil {
			return err
		}
		if view.Candidate != "" {
			return tx.AppendLog(b.Name, RemotePickEntry(rt.Now(), opts.Server, view.Candidate, opts.Candidate != "", 1, opts.Placement))
		}
		return nil
	}); err != nil {
		return AddResult{}, fmt.Errorf("save remote binding: %w", err)
	}

	if stored, err := rt.Store.Load(b.Name); err == nil {
		b = stored
	}

	return AddResult{Binding: b, Branch: input.branch, Base: input.base, Resolution: res}, nil
}

// RemotePickEntry is addRemote's and sendRemote's pick log entry: the same
// shape pickEntry writes (DirToMasterMind, KindPick, Confirmed) but naming the
// server and whether the token was named by the mastermind or picked by the
// server's own policy -- ExplainResolution's "explicit, policy bypassed"
// wording assumes a local resolveCandidate call that never ran here, so it
// would misdescribe a token the server picked on its own. round is the round
// the pick is filed under: 1 for a fresh binding, the sent round for a
// `relevo send --candidate`. A placement appends its own clause; a zero
// one leaves the sentence byte-identical.
func RemotePickEntry(now time.Time, server, token string, explicit bool, round int, placement resolve.PlacementResolution) store.LogEntry {
	how := "server's pick"
	if explicit {
		how = "explicit"
	}
	return store.LogEntry{
		TS: now.UTC(), Round: round, Direction: store.DirToMasterMind,
		Kind: store.KindPick, Confirmed: true,
		Note: fmt.Sprintf("picked %s on %s: %s", token, server, how) + resolve.PlacementClause(placement),
	}
}
