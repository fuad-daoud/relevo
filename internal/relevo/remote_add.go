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
	"github.com/fuad-daoud/relevo/internal/installation"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

func addRemote(ctx context.Context, rt Runtime, opts AddOptions, rec mastermind.Record, haveRec bool) (result AddResult, err error) {
	// The caller's mastermind is a hard precondition here exactly as it is on the
	// local path: a remote binding records the client mastermind's id and
	// session, so without a resolved record there is nothing to record and
	// nothing may be created on the server.
	if !haveRec {
		return AddResult{}, ErrNoMasterMindSession
	}
	opts.MasterMindID = rec.ID
	mastermindEP := recordEndpoint(rec)
	if mastermindEP.TranscriptLocator == "" {
		mastermindEP.TranscriptLocator = mastermindLocator(rt, mastermindEP.Kind, mastermindEP.SessionID)
	}

	if opts.CWD != "" {
		return AddResult{}, errors.New("remote builders are add-only: --cwd and --server cannot be combined")
	}
	if rt.Remote == nil {
		return AddResult{}, ErrRemoteUnavailable
	}
	if rt.Git == nil {
		return AddResult{}, ErrGitRequired
	}

	// 1. store.ValidName; binding must not exist locally.
	if err := store.ValidName(opts.Name); err != nil {
		return AddResult{}, err
	}
	if opts.Feature != "" {
		if err := store.ValidFeature(opts.Feature); err != nil {
			return AddResult{}, err
		}
	}
	if _, err := rt.Store.Load(opts.Name); err == nil {
		return AddResult{}, fmt.Errorf("binding %q already exists locally", opts.Name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return AddResult{}, err
	}

	// 1.5. --branch adopts an existing branch: the same driven-by-live-binding
	// guard and local/origin resolution as Add, and its tip overrides --base.
	// No worktree is made on the client for a remote binding, so there is no
	// CheckoutWorktree here.
	branch := "relevo/" + opts.Name
	existingBranch := false
	base := opts.Base
	if opts.Branch != "" {
		if opts.Base != "" {
			return AddResult{}, errors.New("--base and --branch are exclusive")
		}
		if err := branchDrivenByLiveBinding(rt, opts.Branch); err != nil {
			return AddResult{}, err
		}
		exists, err := rt.Git.BranchExists(ctx, opts.Repo, opts.Branch)
		if err != nil {
			return AddResult{}, err
		}
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
		}
		tip, ok, err := rt.Git.RefSHA(ctx, opts.Repo, "refs/heads/"+opts.Branch)
		if err != nil {
			return AddResult{}, err
		}
		if !ok {
			return AddResult{}, fmt.Errorf("branch %q vanished", opts.Branch)
		}
		branch = opts.Branch
		existingBranch = true
		base = tip
	} else {
		// 2. base := opts.Base if given else rt.Git.HeadCommit(opts.Repo)
		// resolve to a full sha with RefSHA(opts.Repo, base) when it is not 40-hex; missing -> error
		if base == "" {
			var err error
			base, err = rt.Git.HeadCommit(ctx, opts.Repo)
			if err != nil {
				return AddResult{}, fmt.Errorf("head commit: %w", err)
			}
		}
		if !is40Hex(base) {
			sha, ok, err := rt.Git.RefSHA(ctx, opts.Repo, base)
			if err != nil {
				return AddResult{}, fmt.Errorf("resolve base %s: %w", base, err)
			}
			if !ok {
				return AddResult{}, fmt.Errorf("base %q not found", base)
			}
			base = sha
		}
	}

	// 3. root := rt.Git.RootCommit(opts.Repo); repoID := remote.RepoID(root)
	root, err := rt.Git.RootCommit(ctx, opts.Repo)
	if err != nil {
		return AddResult{}, fmt.Errorf("root commit: %w", err)
	}
	repoID, err := remote.RepoID(root)
	if err != nil {
		return AddResult{}, fmt.Errorf("repo id: %w", err)
	}

	// 3.5. name, email := rt.Git.Identity(opts.Repo): the identity the remote
	// builder commits as (#335). Resolved here, before the candidate check
	// and before anything that touches the server, so a missing identity
	// refuses with no binding on the server, no local binding and no branch.
	authorName, authorEmail, err := rt.Git.Identity(ctx, opts.Repo)
	if err != nil {
		return AddResult{}, fmt.Errorf("git identity for %s: %w", opts.Repo, err)
	}
	if authorName == "" || authorEmail == "" {
		return AddResult{}, fmt.Errorf("%w for %s: a remote builder commits as you; set git config user.name and git config user.email (in the repo or --global)", ErrNoGitIdentity, opts.Repo)
	}

	// 4. candidate := opts.Candidate; if non-empty, rt.Remote.Candidates(server) must list it, by canonical
	// token or by name (else error naming the server's candidates); if empty, leave "" and let the server pick.
	// The canonical token is what travels.
	candidateStr := opts.Candidate
	if candidateStr != "" {
		candResp, err := rt.Remote.Candidates(ctx, opts.Server)
		if err != nil {
			return AddResult{}, fmt.Errorf("list remote candidates: %w", err)
		}
		found := false
		if view, ok := matchCandidateView(candResp.Candidates, candidateStr); ok {
			candidateStr, found = view.Token, true
		} else if !strings.Contains(candidateStr, "/") {
			// A name this client knows resolves to its canonical token,
			// which the server may list under that token.
			if c, rerr := rt.Candidates.Resolve(candidateStr); rerr == nil {
				if view, ok := matchCandidateView(candResp.Candidates, c.Ref().String()); ok {
					candidateStr, found = view.Token, true
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
			return AddResult{}, fmt.Errorf("candidate %q not available on %s (available: %s)", opts.Candidate, opts.Server, strings.Join(available, ", "))
		}
	}

	// 4.5. feature probe: a requested --tier, label or custom --actor requires
	// the server to advertise the matching feature before any binding is
	// created there, and the link requires FeatureOrigin. One WhoAmI answers
	// all of them, and the add makes exactly one probe.
	wireTier := ""
	if opts.Tier != "" {
		t, err := harness.ParseTier(opts.Tier)
		if err != nil {
			return AddResult{}, err
		}
		if err := checkTierCap(t, rt.Policy, opts.AllowYolo); err != nil {
			return AddResult{}, err
		}
		wireTier = string(t)
	}
	wireRole := normRole(opts.Role)
	// The wire always names the actor; a default binding sends "builder".
	actor := wireRole
	if actor == "" {
		actor = "builder"
	}
	who, err := rt.Remote.WhoAmI(ctx, opts.Server)
	if err != nil {
		return AddResult{}, err
	}
	if opts.Tier != "" && !slices.Contains(who.Features, remote.FeatureTier) {
		return AddResult{}, fmt.Errorf("%w: server %s does not carry a permission tier (pre-tier server); upgrade it or drop --tier", ErrServerPreTier, opts.Server)
	}
	// #637: a server that does not advertise labels would save the binding
	// without them, so a client that sets one refuses -- before any branch,
	// worktree or create call.
	if (opts.Feature != "" || opts.Ticket != "") && !slices.Contains(who.Features, remote.FeatureLabels) {
		return AddResult{}, fmt.Errorf("%w: server %s does not carry binding labels (pre-labels server); upgrade it or drop --feature/--ticket", ErrServerPreTier, opts.Server)
	}
	// The client's actors never travel (§5.3): the server resolves the
	// actor against its own. A server too old to do that would ignore the
	// field and run the default actor, so it is refused here -- before any
	// branch, worktree or create call.
	if wireRole != "" && !slices.Contains(who.Features, remote.FeatureRoles) {
		return AddResult{}, fmt.Errorf("server %s does not run custom actors (actor %q); upgrade it", opts.Server, opts.Role)
	}

	// 5. view := rt.Remote.CreateBinding(ctx, server, {Name, RepoID, BaseCommit: base, Candidate, RoundCap, RoundTimeoutMS, Tier})
	// HTTPError 409 -> the server already has this binding for this client, with
	// no local counterpart here; only `relevo serve unbind` on the server can
	// clear that, since a local `relevo bind --resume` has nothing to resume.
	// The two ids the link needs: this client's installation, and a record id
	// minted here so both sides know the client's row from the start. They
	// travel only to a server that advertises FeatureOrigin, which stores them
	// as its row's link; the fields are empty otherwise and the wire omits
	// them, so an older server sees exactly today's request.
	clientInstallation := ""
	clientBindingID := ""
	if slices.Contains(who.Features, remote.FeatureOrigin) {
		inst, err := installation.Load(filepath.Dir(rt.Store.DBPath()))
		if err != nil {
			return AddResult{}, err
		}
		clientInstallation = inst.ID
		clientBindingID = db.NewID()
	}

	// The gate resolves exactly as the local path does: with the client's
	// policy and the actor's role. The resolved value rides on the wire -- the
	// field chains already use -- and is stored on the client mirror, so both
	// sides read the same acceptance check.
	gate := resolveGateFor(opts.Gate, opts.NoGate, rt.Policy, roleChecks(rt.RoleRegistry(), bindingRole(store.Binding{Role: normRole(opts.Role)})))

	createReq := remote.CreateBindingRequest{
		Name:               opts.Name,
		RepoID:             repoID,
		BaseCommit:         base,
		Candidate:          candidateStr,
		Tier:               wireTier,
		Role:               actor,
		Feature:            opts.Feature,
		Ticket:             opts.Ticket,
		Gate:               gate,
		Author:             &remote.GitIdentity{Name: authorName, Email: authorEmail},
		ClientInstallation: clientInstallation,
		ClientBindingID:    clientBindingID,
	}
	view, err := rt.Remote.CreateBinding(ctx, opts.Server, createReq)
	if err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == 409 {
			return AddResult{}, fmt.Errorf("binding %s already exists on %s for this client but not here; relevo serve unbind --owner <your label> %s on the server, or choose another name", opts.Name, opts.Server, opts.Name)
		}
		if errors.As(err, &httpErr) && httpErr.Status == 422 && httpErr.Body.Code == remote.CodeTierAboveMax {
			return AddResult{}, fmt.Errorf("%w: %s", ErrTierAboveMax, httpErr.Body.Message)
		}
		return AddResult{}, err
	}

	// From here on the server has a binding this client does not yet. Every
	// later failure (branch exists, CreateBranch error, Save error including
	// store.ErrCWDTaken, log append error) unbinds it on the server
	// best-effort before returning the original error, so a client-side
	// refusal never leaves an orphaned server binding behind (#100). Once
	// CreateBranch itself has succeeded, the same failure also removes the
	// local branch relevo just cut -- server first, since that binding is the
	// one another client could see (#100 round 4).
	created := true
	branchCreated := false
	defer func() {
		if !created || err == nil {
			return
		}
		if unbindErr := rt.Remote.Unbind(ctx, opts.Server, opts.Name); unbindErr != nil {
			slog.Warn("unbind after failed add", "server", opts.Server, "name", opts.Name, "err", unbindErr)
			err = fmt.Errorf("%w; server binding %s on %s could not be removed: %v", err, opts.Name, opts.Server, unbindErr)
		}
		if branchCreated {
			if delErr := rt.Git.DeleteBranch(ctx, opts.Repo, branch); delErr != nil {
				slog.Warn("delete branch after failed add", "repo", opts.Repo, "branch", branch, "err", delErr)
				err = fmt.Errorf("%w; local branch %s could not be removed: %v", err, branch, delErr)
			}
		}
	}()

	// 6. rt.Git.CreateBranch(opts.Repo, "relevo/"+name, base) -- after the server agreed, so a refused create leaves no branch
	// ErrBranchExists -> error "branch relevo/api exists; delete it or pick another name"
	// In --branch mode the branch already exists and relevo did not create it:
	// there is nothing to create, and branchCreated stays false so the
	// deferred cleanup above never deletes a branch relevo did not make.
	if !existingBranch {
		if err := rt.Git.CreateBranch(ctx, opts.Repo, branch, base); err != nil {
			if errors.Is(err, git.ErrBranchExists) {
				return AddResult{}, fmt.Errorf("branch relevo/%s exists; delete it or pick another name", opts.Name)
			}
			return AddResult{}, err
		}
		branchCreated = true
	}

	// 7. b := Binding{Name, CWD: opts.Repo, Repo: opts.Repo, Branch, Base: base, MasterMind: <as Add fills it>,
	//                 Builder: Endpoint{Mode: ModeRemote, Server: server, Kind: <kind from view.Candidate's harness, "" if unknown>,
	//                                   AgentName: name},
	//                 BuilderCandidate: view.Candidate, Round: 1, State: active, RoundCap/Timeout as Add}
	// Save under lock; append the same pick log entry Add writes, with the candidate the server reported.
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
		Branch: branch,
		Base:   base,
		// ExistingBranch records that relevo adopted a branch it did not
		// create, so nothing here will ever delete it.
		ExistingBranch: existingBranch,
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
		Gate: gate,
		// The actor travels as the client asked it: the server resolved it
		// against its own actors, and the mirror records it so
		// `relevo status` shows it (#382 §5.3).
		Role: wireRole,
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
		Link: remoteLink(view),
		// RecordID is the id minted above: the server was told the same id, so
		// the client copy the server recorded is this row.
		RecordID: clientBindingID,
	}
	res := Resolution{
		Candidate: cand,
		How:       HowExplicit,
		Placement: opts.Placement,
	}

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(b); err != nil {
			return err
		}
		if view.Candidate != "" {
			return tx.AppendLog(b.Name, remotePickEntry(rt.Now(), opts.Server, view.Candidate, opts.Candidate != "", 1, opts.Placement))
		}
		return nil
	}); err != nil {
		return AddResult{}, fmt.Errorf("save remote binding: %w", err)
	}

	if stored, err := rt.Store.Load(b.Name); err == nil {
		b = stored
	}

	created = false
	return AddResult{
		Binding:    b,
		Worktree:   "",
		Branch:     branch,
		Base:       base,
		Resolution: res,
	}, nil
}

// remoteLink is the client row's link to the server's copy, from a create
// view: nil when the view carries no installation and no record id, which is
// what a server that does not advertise FeatureOrigin sends.
func remoteLink(view remote.BindingView) *store.RemoteLink {
	if view.Installation == "" && view.ID == "" {
		return nil
	}
	return &store.RemoteLink{Installation: view.Installation, ID: view.ID}
}

// remotePickEntry is addRemote's and sendRemote's pick log entry: the same
// shape pickEntry writes (DirToMasterMind, KindPick, Confirmed) but naming the
// server and whether the token was named by the mastermind or picked by the
// server's own policy -- ExplainResolution's "explicit, policy bypassed"
// wording assumes a local resolveCandidate call that never ran here, so it
// would misdescribe a token the server picked on its own. round is the round
// the pick is filed under: 1 for a fresh binding, the sent round for a
// `relevo send --candidate` (#318). A placement appends its own clause; a zero
// one leaves the sentence byte-identical.
func remotePickEntry(now time.Time, server, token string, explicit bool, round int, placement PlacementResolution) store.LogEntry {
	how := "server's pick"
	if explicit {
		how = "explicit"
	}
	return store.LogEntry{
		TS: now.UTC(), Round: round, Direction: store.DirToMasterMind,
		Kind: store.KindPick, Confirmed: true,
		Note: fmt.Sprintf("picked %s on %s: %s", token, server, how) + placementClause(placement),
	}
}
