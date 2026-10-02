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
	"github.com/fuad-daoud/relevo/internal/installation"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainAppendPickNotes writes the pick log entry for every member whose actor's
// own placement list was walked: the same pickEntry a local bind writes, with
// the placement and every skip. A member whose actor named no placement writes
// none, so its log stays exactly as it was before placement existed.
func chainAppendPickNotes(tx *store.Tx, rt Runtime, members []chainMember, resolutions map[string]Resolution) error {
	for _, m := range members {
		res := resolutions[m.part]
		if res.How == "" || res.Placement.How == "" {
			continue
		}
		role := bindingRole(store.Binding{Role: normRole(m.actor)})
		if err := tx.AppendLog(m.name, pickEntry(rt.Now(), 1, role, res)); err != nil {
			return err
		}
	}
	return nil
}

// chainRemotePlan is what a remote builder's read-only preflight resolved, so
// the create the start later performs cannot fail on anything that could have
// been answered before the server was asked to create.
type chainRemotePlan struct {
	server      string
	base        string
	repoID      string
	authorName  string
	authorEmail string
	tier        string
	candidate   string
	role        string
}

// chainResolvePlacements resolves every member's placement, read-only and
// before anything exists. The builder's list is walked exactly as `bind
// --actor` does: the chain verb has no --server, so the actor's list decides,
// and a server that cannot carry a chain member is a skip with its reason. A
// reader member's list is walked with every server entry skipped unprobed; a
// list with no local entry refuses the start with placementError's one line.
// The builder is resolved first, so a refusal names it before any reader.
func chainResolvePlacements(ctx context.Context, rt Runtime, opts ChainOptions, plan chainStartPlan) (map[string]PlacementResolution, error) {
	out := make(map[string]PlacementResolution, len(plan.members))
	for _, m := range plan.members {
		flags := placementFlags{ChainReader: true}
		if m.writer {
			flags = placementFlags{ChainMember: true, Feature: opts.Feature, Ticket: plan.ticket}
		}
		p, err := createPlacement(ctx, rt, m.actor, "", "", false, flags)
		if err != nil {
			return nil, err
		}
		out[m.part] = p
	}
	return out, nil
}

// chainRemotePreflight resolves everything a remote builder's create needs, all
// read-only: the base commit, the repo id, the git identity, the tier and the
// server's candidate token. It runs before anything is created, so every
// refusal -- a missing identity, a candidate the server does not list, a tier
// above the client's cap -- fires with nothing on the server and nothing local.
func chainRemotePreflight(ctx context.Context, rt Runtime, opts ChainOptions, plan chainStartPlan, p PlacementResolution) (*chainRemotePlan, error) {
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
	role := normRole(chainActorOrBuilder(opts.BuilderActor))
	tier := resolveRoleTier("", res.Candidate, rt.RoleRegistry(), bindingRole(store.Binding{Role: role}))
	if err := checkTierCap(tier, rt.Policy, false); err != nil {
		return nil, err
	}

	token := res.Candidate.Ref().String()
	srv, miss, err := serverTokenFor(ctx, rt, server, token, res.Candidate.Name)
	if err != nil {
		return nil, err
	}
	if srv == "" {
		return nil, errors.New(miss)
	}

	return &chainRemotePlan{
		server: server, base: base, repoID: repoID,
		authorName: authorName, authorEmail: authorEmail,
		tier: string(tier), candidate: srv, role: role,
	}, nil
}

// chainRemoteFacts resolves the base commit, the repository id and the git
// identity a remote create needs. It is read-only, so a placed builder's
// preflight and a whole-chain start both answer every refusal it can before
// the server is asked to create anything. base is the caller's --base value,
// "" meaning HEAD.
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
	if !is40Hex(resolved) {
		sha, ok, rerr := rt.Git.RefSHA(ctx, repo, resolved)
		if rerr != nil {
			return "", "", "", "", fmt.Errorf("resolve base %s: %w", resolved, rerr)
		}
		if !ok {
			return "", "", "", "", refuse("base %q not found", resolved)
		}
		resolved = sha
	}

	root, err := rt.Git.RootCommit(ctx, repo)
	if err != nil {
		return "", "", "", "", fmt.Errorf("root commit: %w", err)
	}
	if repoID, err = remote.RepoID(root); err != nil {
		return "", "", "", "", fmt.Errorf("repo id: %w", err)
	}

	if authorName, authorEmail, err = rt.Git.Identity(ctx, repo); err != nil {
		return "", "", "", "", fmt.Errorf("git identity for %s: %w", repo, err)
	}
	if authorName == "" || authorEmail == "" {
		return "", "", "", "", fmt.Errorf("%w for %s: a remote builder commits as you; set git config user.name and git config user.email (in the repo or --global)", ErrNoGitIdentity, repo)
	}
	return resolved, repoID, authorName, authorEmail, nil
}

// chainBuildRemoteBuilder is `addRemote`'s create half, for a chain's builder
// member: the server create (carrying the chain's own resolved gate), then the
// local branch `relevo/<name>` cut at base, then the client binding. It returns
// an unwind that removes what it made -- server Unbind first, then the branch
// -- for a later failure in the start.
//
// A failure of its own is all-or-none here: a create that succeeded but a
// branch that did not unbinds the server binding before returning; a create
// that never succeeded has nothing local to undo.
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

	// The feature probe the create needs: FeatureOrigin decides whether the two
	// link ids travel, exactly as addRemote's.
	who, err := rt.Remote.WhoAmI(ctx, server)
	if err != nil {
		return store.Binding{}, nil, err
	}
	clientInstallation := ""
	clientBindingID := ""
	if slices.Contains(who.Features, remote.FeatureOrigin) {
		inst, ierr := installation.Load(filepath.Dir(rt.Store.DBPath()))
		if ierr != nil {
			return store.Binding{}, nil, ierr
		}
		clientInstallation = inst.ID
		clientBindingID = db.NewID()
	}

	createReq := remote.CreateBindingRequest{
		Name:       name,
		RepoID:     rp.repoID,
		BaseCommit: base,
		Candidate:  rp.candidate,
		Tier:       rp.tier,
		Role:       chainWireRole(rp.role),
		Feature:    opts.Feature,
		Ticket:     plan.ticket,
		Gate:       plan.settings.Gate,
		Author:     &remote.GitIdentity{Name: rp.authorName, Email: rp.authorEmail},

		ClientInstallation: clientInstallation,
		ClientBindingID:    clientBindingID,
	}
	view, err := rt.Remote.CreateBinding(ctx, server, createReq)
	if err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == 409 {
			return store.Binding{}, nil, fmt.Errorf("binding %s already exists on %s for this client but not here; relevo serve unbind --owner <your label> %s on the server, or choose another name", name, server, name)
		}
		if errors.As(err, &httpErr) && httpErr.Status == 422 && httpErr.Body.Code == remote.CodeTierAboveMax {
			return store.Binding{}, nil, fmt.Errorf("%w: %s", ErrTierAboveMax, httpErr.Body.Message)
		}
		return store.Binding{}, nil, err
	}

	// From here the server has a binding this client does not. Every later
	// failure in this function, or in the start that follows, unbinds it on the
	// server first and then removes the local branch. armed guards a double
	// unwind: the deferred call for a failure here, and the caller's call for a
	// failure in the start, run the same closure.
	armed := true
	branchCreated := false
	unbind := func() {
		if !armed {
			return
		}
		armed = false
		if uerr := rt.Remote.Unbind(ctx, server, name); uerr != nil {
			err = fmt.Errorf("%w; server binding %s on %s could not be removed: %v", err, name, server, uerr)
		}
		if branchCreated {
			if derr := rt.Git.DeleteBranch(ctx, plan.repo, branch); derr != nil {
				err = fmt.Errorf("%w; local branch %s could not be removed: %v", err, branch, derr)
			}
		}
	}
	defer func() {
		if err != nil {
			unbind()
		}
	}()

	if err = rt.Git.CreateBranch(ctx, plan.repo, branch, base); err != nil {
		if errors.Is(err, git.ErrBranchExists) {
			return store.Binding{}, nil, fmt.Errorf("branch relevo/%s exists; delete it or pick another name", name)
		}
		return store.Binding{}, nil, err
	}
	branchCreated = true

	builderKind := ""
	if view.Candidate != "" {
		if ref, rerr := candidate.ParseRef(view.Candidate); rerr == nil {
			builderKind = ref.Harness
		}
	}
	b = store.Binding{
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
		Link:             remoteLink(view),
		RecordID:         clientBindingID,
		Gate:             plan.settings.Gate,
		Regate:           plan.settings.Regate,
	}
	return b, unbind, nil
}

// chainWireRole is the actor name the create request carries: the same
// "builder" default addRemote sends when the client's role normalises to "".
func chainWireRole(role string) string {
	if role == "" {
		return "builder"
	}
	return role
}
