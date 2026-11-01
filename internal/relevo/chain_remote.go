package relevo

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/installation"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainAppendPickNotes writes the pick log entry for every member whose
// actor's placement list was walked: the same pick entry a local bind
// writes, with the placement and every skip. A member whose actor named no
// placement writes none, so its log stays as it was before placement
// existed.
func chainAppendPickNotes(tx *store.Tx, rt Runtime, members []chainMember, resolutions map[string]resolve.Resolution) error {
	for _, m := range members {
		res := resolutions[m.part]
		if res.How == "" || res.Placement.How == "" {
			continue
		}
		role := store.Binding{Role: resolve.NormRole(m.actor)}.WriterRole()
		if err := tx.AppendLog(m.name, resolve.PickEntry(rt.Now(), 1, role, res)); err != nil {
			return err
		}
	}
	return nil
}

// chainRemotePlan is what a remote builder's read-only preflight resolved,
// so the later create cannot fail on anything answerable before the server
// was asked.
type chainRemotePlan struct {
	server      string
	base        string
	repoID      string
	authorName  string
	authorEmail string
	tier        string
	candidate   string
	role        string
	// subdir is the prefix the create carries: the server cuts its own
	// worktree at its root, so the offset travels on the wire.
	subdir string
	who    remote.WhoAmI
}

// chainResolvePlacements resolves every member's placement, read-only and
// before anything exists. The builder's list is walked exactly as `bind
// --actor` does: the chain verb has no --server, so the list decides, and an
// uncarrying server is a skip with its reason. A reader's list is walked with
// every server entry skipped unprobed; a list with no local entry refuses the
// start. The builder resolves first, so a refusal names it before any reader.
func chainResolvePlacements(ctx context.Context, rt Runtime, opts ChainOptions, plan chainStartPlan) (map[string]resolve.PlacementResolution, error) {
	out := make(map[string]resolve.PlacementResolution, len(plan.members))
	for _, m := range plan.members {
		flags := resolve.PlacementFlags{ChainReader: true}
		if m.writer {
			flags = resolve.PlacementFlags{ChainMember: true, Feature: opts.Feature, Ticket: plan.ticket}
		}
		p, err := resolve.CreatePlacement(ctx, ResolveDeps(rt), m.actor, "", "", false, flags)
		if err != nil {
			return nil, err
		}
		out[m.part] = p
	}
	return out, nil
}

// chainRemotePreflight resolves everything a remote builder's create needs,
// all read-only: base commit, repo id, git identity, tier, and the server's
// candidate token. It runs before anything is created, so every refusal fires
// with nothing on the server and nothing local.
func chainRemotePreflight(ctx context.Context, rt Runtime, opts ChainOptions, plan chainStartPlan, p resolve.PlacementResolution) (*chainRemotePlan, error) {
	if rt.Remote == nil {
		return nil, ErrRemoteUnavailable
	}
	if rt.Git == nil {
		return nil, ErrGitRequired
	}
	server := p.Name
	repo := plan.repo

	base, repoID, authorName, authorEmail, err := chainRemoteFacts(ctx, rt, repo, opts.Base)
	if err != nil {
		return nil, err
	}

	res := plan.resolutions[chain.MemberBuilder]
	role := resolve.NormRole(chainActorOrBuilder(opts.BuilderActor))
	tier := resolve.ResolveTier("", res.Candidate, rt.RoleRegistry(), store.Binding{Role: role}.WriterRole())
	if err := resolve.CheckTierCap(tier, rt.Policy, false); err != nil {
		return nil, err
	}

	token := res.Candidate.Ref().String()
	srv, miss, err := ServerTokenFor(ctx, rt, server, token, res.Candidate.Name)
	if err != nil {
		return nil, err
	}
	if srv == "" {
		return nil, errors.New(miss)
	}

	who, err := rt.Remote.WhoAmI(ctx, server)
	if err != nil {
		return nil, err
	}
	// The prefix needs a server that stores it, exactly as a lone bind does:
	// a server silently dropping it would run the rounds at the bare repo's
	// root, not the subdirectory the mastermind's shell was in.
	subdir := housekeep.ResolveSubdir(ctx, rt.Git, repo)
	if subdir != "" && !remote.HasFeature(who, remote.FeatureSubdir) {
		return nil, refusal.New("server %s does not carry binding subdirectories (pre-subdir server); upgrade it or start the chain at the repository root", server)
	}

	return &chainRemotePlan{
		server: server, base: base, repoID: repoID,
		authorName: authorName, authorEmail: authorEmail,
		tier: string(tier), candidate: srv, role: role,
		subdir: subdir,
		who:    who,
	}, nil
}

// chainRemoteFacts resolves the base commit, repository id and git identity a
// remote create needs, read-only, so every refusal it can answer fires before
// the server is asked. base is the caller's --base value, "" meaning HEAD.
func chainRemoteFacts(ctx context.Context, rt Runtime, repo, base string) (resolved, repoID, authorName, authorEmail string, err error) {
	if rt.Git == nil {
		return "", "", "", "", ErrGitRequired
	}
	resolved = base
	if resolved == "" {
		if resolved, err = rt.Git.HeadCommit(ctx, repo); err != nil {
			return "", "", "", "", fmt.Errorf("head commit: %w", err)
		}
	}
	if !remote.Is40Hex(resolved) {
		sha, ok, rerr := rt.Git.RefSHA(ctx, repo, resolved)
		if rerr != nil {
			return "", "", "", "", fmt.Errorf("resolve base %s: %w", resolved, rerr)
		}
		if !ok {
			return "", "", "", "", refusal.New("base %q not found", resolved)
		}
		resolved = sha
	}

	// The repository's identity, through the one helper the repo settings read:
	// a tree git cannot identify has no settings and no id, and a remote
	// create needs the id, so that case is refused here rather than sent as
	// "".
	var ok bool
	repoID, ok, err = RepoKey(ctx, rt.Git, repo)
	if err != nil {
		return "", "", "", "", fmt.Errorf("repo id: %w", err)
	}
	if !ok {
		return "", "", "", "", fmt.Errorf("root commit: %w", git.ErrRefMissing)
	}

	if authorName, authorEmail, err = rt.Git.Identity(ctx, repo); err != nil {
		return "", "", "", "", fmt.Errorf("git identity for %s: %w", repo, err)
	}
	if authorName == "" || authorEmail == "" {
		return "", "", "", "", fmt.Errorf("%w for %s: a remote builder commits as you; set git config user.name and git config user.email (in the repo or --global)", remote.ErrNoGitIdentity, repo)
	}
	return resolved, repoID, authorName, authorEmail, nil
}

// chainBuildRemoteBuilder is `addRemote`'s create half for a chain's builder
// member: the server create carrying the chain's resolved gate, then the local
// branch `relevo/<name>` cut at base, then the client binding. It returns an
// unwind removing what it made -- server Unbind first, then the branch -- for
// a later start failure.
//
// A failure of its own is all-or-none: a create that succeeded but a branch
// that did not unbinds the server binding before returning.
func chainBuildRemoteBuilder(ctx context.Context, rt Runtime, opts ChainOptions, plan chainStartPlan) (b store.Binding, unwind func(), err error) {
	name := opts.Name
	rp := plan.remote
	server := rp.server
	base := rp.base
	branch := "relevo/" + name

	if rt.Remote == nil {
		return store.Binding{}, nil, ErrRemoteUnavailable
	}
	if rt.Git == nil {
		return store.Binding{}, nil, ErrGitRequired
	}

	view, clientBindingID, err := chainRemoteCreateBinding(ctx, rt, opts, plan)
	if err != nil {
		return store.Binding{}, nil, err
	}

	// From here the server has a binding this client does not. Every later
	// failure unbinds it on the server first, then removes the local branch.
	unbind, markBranch := chainRemoteUnwindGuard(ctx, rt, server, name, branch, plan.repo, &err)
	defer func() {
		if err != nil {
			unbind()
		}
	}()

	b, err = chainRemoteLocalBinding(ctx, rt, opts, plan, name, branch, base, view, clientBindingID, markBranch)
	if err != nil {
		return store.Binding{}, nil, err
	}
	return b, unbind, nil
}

// chainRemoteCreateBinding posts the server create carrying the chain's
// resolved gate, mapping the server's answer the way addRemote does. It
// returns the server's view with the client binding id the create carried.
func chainRemoteCreateBinding(ctx context.Context, rt Runtime, opts ChainOptions, plan chainStartPlan) (remote.BindingView, string, error) {
	name := opts.Name
	rp := plan.remote
	server := rp.server
	base := rp.base

	// FeatureOrigin decides whether the two link ids travel, as in addRemote's.
	who := rp.who
	clientInstallation := ""
	clientBindingID := ""
	if slices.Contains(who.Features, remote.FeatureOrigin) {
		inst, ierr := installation.Load(filepath.Dir(rt.Store.DBPath()))
		if ierr != nil {
			return remote.BindingView{}, "", ierr
		}
		clientInstallation = inst.ID
		clientBindingID = db.NewID()
	}

	createReq := remote.CreateBindingRequest{
		Name:       name,
		RepoID:     rp.repoID,
		BaseCommit: base,
		Candidate:  rp.candidate,
		RoundCap:   chainMemberRoundCap(chain.MemberBuilder, len(plan.bodies), plan.settings),
		Tier:       rp.tier,
		Role:       chainWireRole(rp.role),
		Feature:    opts.Feature,
		Ticket:     plan.ticket,
		Subdir:     rp.subdir,
		Gate:       plan.settings.Gate,
		Author:     &remote.GitIdentity{Name: rp.authorName, Email: rp.authorEmail},

		ClientInstallation: clientInstallation,
		ClientBindingID:    clientBindingID,
	}
	view, err := rt.Remote.CreateBinding(ctx, server, createReq)
	if err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == 409 {
			return remote.BindingView{}, "", fmt.Errorf("binding %s already exists on %s for this client but not here; relevo serve unbind --owner <your label> %s on the server, or choose another name", name, server, name)
		}
		if errors.As(err, &httpErr) && httpErr.Status == 422 && httpErr.Body.Code == remote.CodeTierAboveMax {
			return remote.BindingView{}, "", fmt.Errorf("%w: %s", resolve.ErrTierAboveMax, httpErr.Body.Message)
		}
		if errors.As(err, &httpErr) && httpErr.Status == 422 && httpErr.Body.Code == remote.CodeInvalid && resolve.TierUnsupportedMessage(httpErr.Body.Message) {
			return remote.BindingView{}, "", resolve.TierUnsupportedFromWire(httpErr.Body.Message)
		}
		return remote.BindingView{}, "", pickRefusalFromWire(err)
	}
	return view, clientBindingID, nil
}

// chainRemoteUnwindGuard arms the all-or-none unwind for a created server
// binding: server Unbind first, then the local branch. armed guards a double
// unwind: the deferred call for an own failure and the caller's call for a
// later start failure run the same closure. markBranch records the branch
// once cut, so the unwind removes only what exists.
func chainRemoteUnwindGuard(ctx context.Context, rt Runtime, server, name, branch, repo string, err *error) (unbind func(), markBranch func()) {
	armed := true
	created := false
	unbind = func() {
		if !armed {
			return
		}
		armed = false
		if uerr := rt.Remote.Unbind(ctx, server, name, true); uerr != nil {
			*err = fmt.Errorf("%w; server binding %s on %s could not be removed: %w", *err, name, server, uerr)
		}
		if created {
			if derr := rt.Git.DeleteBranch(ctx, repo, branch); derr != nil {
				*err = fmt.Errorf("%w; local branch %s could not be removed: %w", *err, branch, derr)
			}
		}
	}
	markBranch = func() { created = true }
	return unbind, markBranch
}

// chainRemoteLocalBinding cuts the local branch at base and builds the client
// binding from the server's view, the mirror addRemote's create leaves
// behind.
func chainRemoteLocalBinding(ctx context.Context, rt Runtime, opts ChainOptions, plan chainStartPlan, name, branch, base string, view remote.BindingView, clientBindingID string, markBranch func()) (store.Binding, error) {
	rp := plan.remote
	server := rp.server
	if err := rt.Git.CreateBranch(ctx, plan.repo, branch, base); err != nil {
		if errors.Is(err, git.ErrBranchExists) {
			return store.Binding{}, fmt.Errorf("branch relevo/%s exists; delete it or pick another name", name)
		}
		return store.Binding{}, err
	}
	markBranch()

	builderKind := ""
	if view.Candidate != "" {
		if ref, rerr := candidate.ParseRef(view.Candidate); rerr == nil {
			builderKind = ref.Harness
		}
	}
	return store.Binding{
		Name:             name,
		CWD:              plan.repo,
		Repo:             plan.repo,
		Branch:           branch,
		Base:             base,
		MasterMind:       plan.mastermind,
		MasterMindID:     plan.mastermindID,
		Builder:          store.Endpoint{Mode: store.ModeRemote, Server: server, Kind: builderKind, AgentName: name},
		BuilderCandidate: view.Candidate,
		Round:            1,
		State:            store.StateActive,
		Tier:             view.Tier,
		Role:             rp.role,
		Shape:            view.Shape,
		RepoRef:          captureRepo(ctx, rt, plan.repo),
		Feature:          opts.Feature,
		Ticket:           plan.ticket,
		Link:             remote.Link(view),
		RecordID:         clientBindingID,
		Gate:             plan.settings.Gate,
		Regate:           plan.settings.Regate,
	}, nil
}

// chainWireRole is the actor name the create request carries: the same
// "builder" default addRemote sends when the client's role is "".
func chainWireRole(role string) string {
	if role == "" {
		return "builder"
	}
	return role
}
