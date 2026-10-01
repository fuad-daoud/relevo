package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/installation"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainServerPlan is everything a server-chain start resolved before anything
// existed: the local policy settings, the plans, the member names, the caller's
// mastermind, the git facts the create needs and the server's answer about
// itself.
type chainServerPlan struct {
	settings     chain.Settings
	repo         string
	ticket       string
	bodies       [][]byte
	members      []chainMember
	mastermind   store.Endpoint
	mastermindID string
	base         string
	repoID       string
	authorName   string
	authorEmail  string
	server       string
	// origin is whether the server advertises FeatureOrigin, which decides
	// whether the client mints the member ids the create carries.
	origin bool
}

// chainServerIDs is the client's own identifiers for a server chain: the
// installation id the create names and one record id per member part.
type chainServerIDs struct {
	installation string
	byPart       map[string]string
}

// chainStartServer starts a chain on a server: the local refusals run first,
// then one create hands the whole chain and its base bundle over, and finally
// this machine records a mirror of the row and its members. Every refusal
// fires before the server is asked to create anything.
func chainStartServer(ctx context.Context, rt Runtime, opts ChainOptions) (ChainResult, error) {
	plan, err := chainServerResolve(ctx, rt, opts)
	if err != nil {
		return ChainResult{}, err
	}
	return chainServerCreate(ctx, rt, opts, plan)
}

// chainServerResolve runs every refusal a server-chain start can make without
// touching the server's state: the local policy, the names, the branch and the
// caller's mastermind, then the server's advertised features and the base,
// repo id and identity the create needs. It reads git and the server; it
// creates nothing.
func chainServerResolve(ctx context.Context, rt Runtime, opts ChainOptions) (chainServerPlan, error) {
	if rt.Remote == nil {
		return chainServerPlan{}, ErrRemoteUnavailable
	}
	if rt.Transport == nil {
		return chainServerPlan{}, errors.New("no remote transport configured")
	}
	plan := chainServerPlan{
		settings: chainSettings(rt.Policy, opts, roleChecks(rt.RoleRegistry(), "builder")),
		server:   opts.Server,
	}
	repo, err := os.Getwd()
	if err != nil {
		return chainServerPlan{}, fmt.Errorf("resolve working directory: %w", err)
	}
	plan.repo = repo
	if err := chainValidate(opts); err != nil {
		return chainServerPlan{}, err
	}
	if plan.ticket, err = chainTicket(ctx, rt, opts.Ticket, repo); err != nil {
		return chainServerPlan{}, err
	}
	if plan.bodies, err = chainPlanBodies(opts.Plans); err != nil {
		return chainServerPlan{}, err
	}
	plan.members = chainMembersFor(opts, plan.settings)
	if err := chainFreeNames(rt, plan.members); err != nil {
		return chainServerPlan{}, err
	}
	if err := chainServerBranchFree(ctx, rt, repo, opts.Name); err != nil {
		return chainServerPlan{}, err
	}
	if plan.mastermind, plan.mastermindID, err = chainServerMasterMind(rt, opts.MasterMindID); err != nil {
		return chainServerPlan{}, err
	}
	who, err := rt.Remote.WhoAmI(ctx, opts.Server)
	if err != nil {
		return chainServerPlan{}, err
	}
	if missing := missingChainFeatures(who); len(missing) > 0 {
		return chainServerPlan{}, fmt.Errorf("server %s cannot run a chain (missing the %s); upgrade it, or start the chain without --server",
			opts.Server, chainFeatureList(missing))
	}
	plan.origin = hasFeature(who, remote.FeatureOrigin)
	if plan.base, plan.repoID, plan.authorName, plan.authorEmail, err = chainRemoteFacts(ctx, rt, repo, opts.Base); err != nil {
		return chainServerPlan{}, err
	}
	return plan, nil
}

// chainServerBranchFree refuses a start whose branch relevo/<name> already
// exists: the mirror needs to cut it, so an existing one is a conflict rather
// than something to adopt.
func chainServerBranchFree(ctx context.Context, rt Runtime, repo, name string) error {
	if rt.Git == nil {
		return ErrGitRequired
	}
	branch := "relevo/" + name
	exists, err := rt.Git.BranchExists(ctx, repo, branch)
	if err != nil {
		return fmt.Errorf("branch %s: %w", branch, err)
	}
	if exists {
		return fmt.Errorf("branch %s exists; delete it or pick another name", branch)
	}
	return nil
}

// chainServerMasterMind resolves the caller's mastermind record for the
// mirror, exactly as the local start does.
func chainServerMasterMind(rt Runtime, id string) (store.Endpoint, string, error) {
	rec, have, err := resolveVerbMasterMind(rt, id)
	if err != nil {
		return store.Endpoint{}, "", err
	}
	if !have {
		return store.Endpoint{}, "", ErrNoMasterMindSession
	}
	ep := recordEndpoint(rec)
	if ep.TranscriptLocator == "" {
		ep.TranscriptLocator = mastermindLocator(rt, ep.Kind, ep.SessionID)
	}
	return ep, rec.ID, nil
}

// chainServerCreate hands the resolved chain to the server and records the
// mirror here. The order is pinned: the out ref and the base bundle, the
// create, then the local branch, the mirror row with its members, and the plan
// copies. A local step that fails after the server answered deletes the branch
// this call created and asks for the same command again -- the server dedupes
// the retry and answers with the chain's current view.
func chainServerCreate(ctx context.Context, rt Runtime, opts ChainOptions, plan chainServerPlan) (ChainResult, error) {
	name := opts.Name
	base := plan.base
	branch := "relevo/" + name

	bundle, closeBundle, err := chainServerBundle(ctx, rt, plan.repo, name, base)
	if err != nil {
		return ChainResult{}, err
	}
	defer closeBundle()
	ids, err := chainServerBindingIDs(rt, plan)
	if err != nil {
		return ChainResult{}, err
	}
	view, err := rt.Remote.CreateChain(ctx, plan.server, chainServerRequest(opts, plan, ids), bundle)
	if err != nil {
		return ChainResult{}, err
	}

	branchCreated := false
	localFail := func(err error) (ChainResult, error) {
		if branchCreated {
			_ = rt.Git.DeleteBranch(ctx, plan.repo, branch)
		}
		return ChainResult{}, fmt.Errorf("chain %s was accepted by %s but this machine could not record it: %w; run the same relevo chain command again to record it",
			name, plan.server, err)
	}
	if err := rt.Git.CreateBranch(ctx, plan.repo, branch, base); err != nil {
		if errors.Is(err, git.ErrBranchExists) {
			return ChainResult{}, fmt.Errorf("branch %s exists; delete it or pick another name", branch)
		}
		return localFail(err)
	}
	branchCreated = true

	planPaths := make([]string, len(plan.bodies))
	for i := range plan.bodies {
		planPaths[i] = rt.Store.ChainPlanPath(name, i+1)
	}
	now := time.Now
	if rt.Now != nil {
		now = rt.Now
	}
	row, err := chainMirrorRow(opts, plan, view, planPaths, now())
	if err != nil {
		return localFail(err)
	}
	members := chainMirrorMembers(ctx, rt, opts, plan, view, ids)
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.CreateChain(row, members)
	}); err != nil {
		return localFail(err)
	}
	if err := chainCopyPlans(rt, name, plan.bodies); err != nil {
		return localFail(err)
	}
	stored, err := chainStoredMembers(rt, plan.members)
	if err != nil {
		return ChainResult{}, err
	}
	return ChainResult{Chain: row, Members: stored, Plans: len(plan.bodies), Check: chainBuilderCheck(stored, name)}, nil
}

// chainServerBundle names the out ref at base and snapshots it, the two steps
// a remote send makes before it ships a round. The bundle the snapshot carries
// is what the server cuts the builder's worktree from; a snapshot that is
// empty sends no bundle part. The returned closer releases the snapshot body.
func chainServerBundle(ctx context.Context, rt Runtime, repo, name, base string) (io.Reader, func(), error) {
	outRef := "refs/relevo/" + name + "/out"
	if err := rt.Git.UpdateRef(ctx, repo, outRef, base, ""); err != nil {
		return nil, nil, fmt.Errorf("update ref %s: %w", outRef, err)
	}
	snap, err := rt.Transport.Snapshot(ctx, repo, []string{outRef}, "")
	if err != nil {
		return nil, nil, fmt.Errorf("snapshot %s: %w", outRef, err)
	}
	closer := func() {
		if snap.Body != nil {
			_ = snap.Body.Close()
		}
	}
	var bundle io.Reader
	if !snap.Empty && snap.Body != nil {
		bundle = snap.Body
	}
	return bundle, closer, nil
}

// chainServerRequest is the create a server-chain start posts: the resolved
// settings, the plans as text, the labels, the author and the client's ids.
func chainServerRequest(opts ChainOptions, plan chainServerPlan, ids chainServerIDs) remote.CreateChainRequest {
	return remote.CreateChainRequest{
		Name:               opts.Name,
		RepoID:             plan.repoID,
		BaseCommit:         plan.base,
		Plans:              chainPlanTexts(plan.bodies),
		Settings:           chainWireSettings(plan.settings),
		Feature:            opts.Feature,
		Ticket:             plan.ticket,
		Author:             &remote.GitIdentity{Name: plan.authorName, Email: plan.authorEmail},
		ClientInstallation: ids.installation,
		ClientBindingIDs:   ids.byPart,
	}
}

// chainServerBindingIDs mints the client's identifiers for the create when the
// server advertises FeatureOrigin; a pre-origin server gets none, exactly as a
// remote binding create does.
func chainServerBindingIDs(rt Runtime, plan chainServerPlan) (chainServerIDs, error) {
	if !plan.origin {
		return chainServerIDs{}, nil
	}
	inst, err := installation.Load(filepath.Dir(rt.Store.DBPath()))
	if err != nil {
		return chainServerIDs{}, err
	}
	byPart := make(map[string]string, len(plan.members))
	for _, m := range plan.members {
		byPart[m.part] = db.NewID()
	}
	return chainServerIDs{installation: inst.ID, byPart: byPart}, nil
}

// chainPlanTexts is the plan copies as text, in order.
func chainPlanTexts(bodies [][]byte) []string {
	out := make([]string, len(bodies))
	for i, b := range bodies {
		out[i] = string(b)
	}
	return out
}

// chainWireSettings converts the chain's resolved settings to the wire type:
// internal/remote does not import internal/chain, so the two are converted at
// this edge.
func chainWireSettings(s chain.Settings) remote.ChainSettings {
	return remote.ChainSettings{
		MaxCorrections: s.MaxCorrections,
		ReviewerActor:  s.ReviewerActor,
		PlannerActor:   s.PlannerActor,
		SecurityActor:  s.SecurityActor,
		Security:       s.Security,
		Gate:           s.Gate,
		Regate:         s.Regate,
	}
}

// chainMirrorRow is the client's mirror of a server chain's row: the server's
// state, this machine's plan copies and settings, and the server that drives
// it. The worktree is empty -- the server holds the builder's tree -- and the
// branch is the one the mirror cut.
func chainMirrorRow(opts ChainOptions, plan chainServerPlan, view remote.ChainView, planPaths []string, now time.Time) (db.ChainRow, error) {
	paths, err := json.Marshal(planPaths)
	if err != nil {
		return db.ChainRow{}, fmt.Errorf("encode chain paths: %w", err)
	}
	settings, err := json.Marshal(plan.settings)
	if err != nil {
		return db.ChainRow{}, fmt.Errorf("encode chain settings: %w", err)
	}
	names := make(map[string]string, len(plan.members))
	for _, m := range plan.members {
		names[m.part] = m.name
	}
	at := now.UTC()
	return db.ChainRow{
		ID:              db.NewID(),
		Name:            opts.Name,
		Status:          chainOr(view.Status, string(chain.StatusRunning)),
		Reason:          view.Reason,
		Phase:           chainOr(view.Phase, string(chain.PhaseBuild)),
		Step:            chainOr(view.Step, string(chain.StepBuilding)),
		Plan:            chainIntOr(view.Plan, 1),
		Plans:           chainIntOr(view.Plans, len(planPaths)),
		PlanPathsJSON:   paths,
		Corrections:     view.Corrections,
		SettingsJSON:    settings,
		AwaitingMember:  chainOr(view.AwaitingMember, chain.MemberBuilder),
		AwaitingRound:   chainIntOr(view.AwaitingRound, 1),
		Builder:         names[chain.MemberBuilder],
		Reviewer:        names[chain.MemberReviewer],
		Planner:         names[chain.MemberPlanner],
		Security:        names[chain.MemberSecurity],
		Base:            chainOr(view.Base, plan.base),
		Branch:          "relevo/" + opts.Name,
		Repo:            plan.repo,
		Feature:         opts.Feature,
		Ticket:          plan.ticket,
		Server:          plan.server,
		MasterMindID:    plan.mastermindID,
		PlanStartCommit: view.PlanStartCommit,
		CreatedAt:       at,
		UpdatedAt:       at,
	}, nil
}

// chainMirrorMembers builds the mirror bindings in part order, one per member,
// from the server's member views.
func chainMirrorMembers(ctx context.Context, rt Runtime, opts ChainOptions, plan chainServerPlan, view remote.ChainView, ids chainServerIDs) []store.Binding {
	views := make(map[string]remote.ChainMemberView, len(view.Members))
	for _, v := range view.Members {
		views[v.Part] = v
	}
	repoRef := captureRepo(ctx, rt, plan.repo)
	out := make([]store.Binding, 0, len(plan.members))
	for _, m := range plan.members {
		out = append(out, chainMirrorMember(opts, plan, m, views[m.part], ids.byPart[m.part], repoRef))
	}
	return out
}

// chainMirrorMember is one member's mirror binding: a remote member on the
// chain's server, built from its BindingView. The builder carries the branch,
// the base, the check and the branch head; a reader carries no branch, because
// it reads the builder's artifacts on the server.
func chainMirrorMember(opts ChainOptions, plan chainServerPlan, m chainMember, v remote.ChainMemberView, id string, repoRef *store.RepoRef) store.Binding {
	bv := v.View
	kind := ""
	if bv.Candidate != "" {
		if ref, err := candidate.ParseRef(bv.Candidate); err == nil {
			kind = ref.Harness
		}
	}
	b := store.Binding{
		Name:             m.name,
		CWD:              plan.repo,
		Repo:             plan.repo,
		MasterMind:       plan.mastermind,
		MasterMindID:     plan.mastermindID,
		Builder:          store.Endpoint{Mode: store.ModeRemote, Server: plan.server, Kind: kind, AgentName: m.name},
		BuilderCandidate: bv.Candidate,
		Round:            chainIntOr(bv.Round, 1),
		State:            chainMemberState(bv.State),
		Tier:             bv.Tier,
		Role:             normRole(m.actor),
		Shape:            chainOr(bv.Shape, m.shape),
		RepoRef:          repoRef,
		Feature:          opts.Feature,
		Ticket:           plan.ticket,
		Link:             remoteLink(bv),
		RecordID:         id,
	}
	if m.writer {
		b.Branch = "relevo/" + m.name
		b.Base = plan.base
		b.Gate = plan.settings.Gate
		b.Regate = plan.settings.Regate
		b.Builder.LastShipped = plan.base
		b.Builder.LastKnown = plan.base
	}
	return b
}

// chainMemberState reads a member view's state as the store's, defaulting to
// active when the server named none.
func chainMemberState(state string) store.State {
	s := store.State(state)
	if !store.KnownState(s) {
		return store.StateActive
	}
	return s
}

// chainOr is s when it is not empty, fallback otherwise.
func chainOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// chainIntOr is n when it is not zero, fallback otherwise.
func chainIntOr(n, fallback int) int {
	if n == 0 {
		return fallback
	}
	return n
}
