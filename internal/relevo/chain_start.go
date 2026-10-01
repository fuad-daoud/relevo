package relevo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainMaxNameLen is the longest chain name: the longest member suffix is
// "-plan", so a name that would push a member past store.ValidName's cap is
// refused rather than truncated.
const chainMaxNameLen = store.MaxAgentNameLen - len("-plan")

// ChainOptions is what one `relevo chain` start was asked for.
type ChainOptions struct {
	// Name is the chain's name, and its builder member's; it must leave room
	// for the longest member suffix.
	Name string
	// Plans is the ordered source paths; at least one. Each is readable and
	// non-empty, and each is copied into the chain's own directory at start.
	Plans []string
	// BuilderActor names the actor the builder member runs. The chain fixes it
	// to the builder actor and no flag exposes it; it stays a field so the
	// member's writer check is the one call every part's shape goes through.
	BuilderActor string
	// Feature, NoFeature and Ticket label every member, exactly as on bind.
	// Exactly one of Feature/NoFeature is required.
	Feature   string
	NoFeature bool
	Ticket    string
	// Base is the commit or ref the builder is cut from; "" is HEAD.
	Base string
	// Gate is the builder member's acceptance command, exactly as bind's --gate;
	// "" falls back to policy.gate.default when the builder role checks.
	Gate string
	// NoGate opts the builder member out of policy.gate.default; it wins over
	// Gate, exactly as bind's --no-gate.
	NoGate bool
	// Regate is the builder member's repair-round budget after a failing gate;
	// nil takes policy.gate.regate.
	Regate *int
	// MaxCorrections overrides policy.chain.max_corrections; nil takes it.
	MaxCorrections *int
	// ReviewerActor, PlannerActor and SecurityActor override the matching
	// policy.chain settings; "" takes them.
	ReviewerActor, PlannerActor, SecurityActor string
	// Security overrides policy.chain.security; nil takes it.
	Security *bool
	// MasterMindID is the caller's --mastermind value; "" resolves this
	// session's mastermind.
	MasterMindID string
	// Server runs the whole chain on this server's daemon, so it continues
	// when this machine is off. "" starts the chain locally.
	Server string
	// Workflow names the workflow the chain runs: a saved or shipped name, or
	// a YAML/JSON file. "" is the shipped default.
	Workflow string
	// Params overrides the workflow's params, keyed by param name; they win
	// over every old flag and the policy.
	Params map[string]string
	// Task is the chain's task input text, already read from --task-file when
	// that flag named one.
	Task string
	// DryRun resolves and validates the workflow and prints its graph without
	// starting anything.
	DryRun bool
}

// ChainResult is what a start produced: the chain row, its members in part
// order (builder, reviewer, planner, and security when the phase is on), and
// how many plans it holds.
type ChainResult struct {
	Chain   db.ChainRow
	Members []store.Binding
	Plans   int
	// Check is the builder member's resolved acceptance command; "" means none.
	Check string
}

// chainMember is one part of a chain: which part it fills, the binding name it
// gets, the actor it runs and the shape that actor must have.
type chainMember struct {
	part   string
	name   string
	actor  string
	shape  string
	writer bool
}

// chainBase is everything the members share, resolved once before the cut.
type chainBase struct {
	cwd          string
	mastermind   store.Endpoint
	mastermindID string
	repo         string
	repoRef      *store.RepoRef
	feature      string
	ticket       string
	worktree     string
	branch       string
	commit       string
	baseRef      string
}

// chainStartPlan is everything a start resolved before anything existed.
type chainStartPlan struct {
	settings     chain.Settings
	repo         string
	ticket       string
	bodies       [][]byte
	members      []chainMember
	resolutions  map[string]Resolution
	placements   map[string]PlacementResolution
	remote       *chainRemotePlan
	mastermind   store.Endpoint
	mastermindID string
}

// ChainStart starts a local chain: it validates everything before anything
// exists, cuts the builder's worktree, creates the members and the chain row
// atomically, copies the plans into the chain's directory and hands plan 1 to
// the builder.
//
// Preconditions:  a relevo mastermind resolves for the caller; opts.Name is a
// valid name of at most chainMaxNameLen characters; exactly one of
// opts.Feature/opts.NoFeature is set; every plan is readable and non-empty;
// every member name is free; the builder actor is a writer and the other
// actors are readers, and each resolves to a candidate.
//
// Postconditions: on success a chain row exists in status running, phase
// build, step building, awaiting (builder, 1), with its member bindings and its
// plan copies, and plan 1 has been sent to the builder. A failed handover
// leaves the chain row and the members in place; the builder member is then
// NEEDS YOU.
//
// Errors: the validation refusals above, ErrGitRequired, git.ErrBranchExists,
// ErrNoMasterMindSession, or a wrapped git, store or send failure.
func ChainStart(ctx context.Context, rt Runtime, opts ChainOptions) (ChainResult, error) {
	if opts.Server != "" {
		return chainStartServer(ctx, rt, opts)
	}
	plan, err := chainResolveStart(ctx, rt, opts)
	if err != nil {
		return ChainResult{}, err
	}
	return chainCreate(ctx, rt, opts, plan)
}

// chainResolveStart runs every precondition of a start: the settings, the
// names, the plans, the actor shapes, their candidates and the caller's
// mastermind. It reads and writes nothing.
func chainResolveStart(ctx context.Context, rt Runtime, opts ChainOptions) (chainStartPlan, error) {
	plan := chainStartPlan{settings: chainSettings(rt.Policy, opts, roleChecks(rt.RoleRegistry(), "builder"))}

	repo, err := os.Getwd()
	if err != nil {
		return chainStartPlan{}, fmt.Errorf("resolve working directory: %w", err)
	}
	plan.repo = repo
	if err := chainValidate(opts); err != nil {
		return chainStartPlan{}, err
	}
	if plan.ticket, err = chainTicket(ctx, rt, opts.Ticket, repo); err != nil {
		return chainStartPlan{}, err
	}
	if plan.bodies, err = chainPlanBodies(opts.Plans); err != nil {
		return chainStartPlan{}, err
	}

	plan.members = chainMembersFor(opts, plan.settings)
	if err := chainFreeNames(rt, plan.members); err != nil {
		return chainStartPlan{}, err
	}
	if plan.resolutions, err = chainResolveActors(rt, plan.members); err != nil {
		return chainStartPlan{}, err
	}
	if err := chainValidateDefault(rt, plan.settings, workflow.Given{Plans: len(plan.bodies) > 0}); err != nil {
		return chainStartPlan{}, err
	}
	// Every member's placement is resolved before anything is created: the
	// builder first (its list is walked exactly as `bind --actor` would), then
	// each reader's, whose server entries are skipped unprobed. A remote
	// builder also resolves its read-only preflight here, so no refusal fires
	// after the server has been asked to create anything.
	if plan.placements, err = chainResolvePlacements(ctx, rt, opts, plan); err != nil {
		return chainStartPlan{}, err
	}
	// Each resolution carries its placement, so the member's pick note records
	// where it landed and every entry its actor's list passed over -- exactly as
	// a local bind's does.
	for part, p := range plan.placements {
		res := plan.resolutions[part]
		res.Placement = p
		plan.resolutions[part] = res
	}
	if p := plan.placements[chain.MemberBuilder]; !p.local() {
		if plan.remote, err = chainRemotePreflight(ctx, rt, opts, plan, p); err != nil {
			return chainStartPlan{}, err
		}
	}

	rec, haveRec, err := resolveVerbMasterMind(rt, opts.MasterMindID)
	if err != nil {
		return chainStartPlan{}, err
	}
	if !haveRec {
		return chainStartPlan{}, ErrNoMasterMindSession
	}
	plan.mastermindID = rec.ID
	plan.mastermind = recordEndpoint(rec)
	if plan.mastermind.TranscriptLocator == "" {
		plan.mastermind.TranscriptLocator = mastermindLocator(rt, plan.mastermind.Kind, plan.mastermind.SessionID)
	}
	return plan, nil
}

// chainCreate is the half of a start that creates things: it cuts the
// builder's worktree, builds every member, writes the chain row and the
// members in one transaction, copies the plans and sends plan 1. A builder
// whose placement named a server takes the remote path instead; the local path
// is unchanged.
func chainCreate(ctx context.Context, rt Runtime, opts ChainOptions, plan chainStartPlan) (ChainResult, error) {
	if plan.remote != nil {
		return chainCreateWithRemoteBuilder(ctx, rt, opts, plan)
	}
	worktree, branch, commit, baseRef, err := cutWorktree(ctx, rt, opts.Name, plan.repo, opts.Base)
	if err != nil {
		return ChainResult{}, err
	}
	base := chainBase{
		cwd: worktree, mastermind: plan.mastermind, mastermindID: plan.mastermindID,
		repo: plan.repo, repoRef: captureRepo(ctx, rt, plan.repo), feature: opts.Feature, ticket: plan.ticket,
		worktree: worktree, branch: branch, commit: commit, baseRef: baseRef,
	}
	built, err := chainBuildMembers(ctx, rt, plan.members, plan.resolutions, base, plan.settings)
	if err != nil {
		chainRollback(ctx, rt, plan.repo, worktree, branch)
		return ChainResult{}, err
	}

	planPaths := make([]string, len(plan.bodies))
	for i := range plan.bodies {
		planPaths[i] = rt.Store.ChainPlanPath(opts.Name, i+1)
	}
	now := time.Now
	if rt.Now != nil {
		now = rt.Now
	}
	row, err := chainRow(opts, plan.settings, plan.members, base, planPaths, now())
	if err != nil {
		chainRollback(ctx, rt, plan.repo, worktree, branch)
		return ChainResult{}, err
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.CreateChain(row, built); err != nil {
			return err
		}
		return chainAppendPickNotes(tx, rt, plan.members, plan.resolutions)
	}); err != nil {
		chainRollback(ctx, rt, plan.repo, worktree, branch)
		return ChainResult{}, err
	}

	stored, err := chainStoredMembers(rt, plan.members)
	if err != nil {
		return ChainResult{}, err
	}
	if err := chainCopyPlans(rt, opts.Name, plan.bodies); err != nil {
		return ChainResult{}, fmt.Errorf("chain %q started, but copying its plans failed: %w", opts.Name, err)
	}
	// Plan 1 goes through the chain's own sender, so a running chain's
	// refusal never bites its own start. The member carries its resolved
	// candidate already, so none of Send's preflight is needed. A failure
	// leaves the chain row running and the builder member NEEDS YOU; a later
	// round's sweep turns that into a halt.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(opts.Name)
		if err != nil {
			return err
		}
		body, err := rt.Store.ReadFile(rt.Store.ChainPlanPath(opts.Name, 1))
		if err != nil {
			return err
		}
		_, err = sendChainRound(ctx, rt, tx, b, string(body))
		return err
	}); err != nil {
		return ChainResult{}, fmt.Errorf("chain %q started, but plan 1 could not be handed to %s: %w", opts.Name, opts.Name, err)
	}
	return ChainResult{Chain: row, Members: stored, Plans: len(plan.bodies), Check: chainBuilderCheck(stored, opts.Name)}, nil
}

// chainRollback removes what a failed start created: the worktree and the
// branch the cut made. A failure here is ignored, exactly as add's rollback
// ignores one.
func chainRollback(ctx context.Context, rt Runtime, repo, worktree, branch string) {
	if rt.Git == nil {
		return
	}
	_ = rt.Git.RemoveWorktree(ctx, repo, worktree, true)
	_ = rt.Git.DeleteBranch(ctx, repo, branch)
}

// chainSettings resolves the chain's settings: a flag beats the setting, and
// the setting beats the policy default. checks is the builder role's own
// check flag, which decides whether an unset gate flag takes policy's
// gate.default -- exactly as a lone binding resolves it.
func chainSettings(pol policy.Policy, opts ChainOptions, checks bool) chain.Settings {
	set := chain.Settings{
		MaxCorrections: pol.ChainMaxCorrections(),
		ReviewerActor:  pol.ChainReviewerActor(),
		PlannerActor:   pol.ChainPlannerActor(),
		SecurityActor:  pol.ChainSecurityActor(),
		Security:       pol.ChainSecurityOn(),
		Gate:           resolveGateFor(opts.Gate, opts.NoGate, pol, checks),
		Regate:         resolveRegate(opts.Regate, pol),
	}
	if opts.MaxCorrections != nil {
		set.MaxCorrections = *opts.MaxCorrections
	}
	if opts.ReviewerActor != "" {
		set.ReviewerActor = opts.ReviewerActor
	}
	if opts.PlannerActor != "" {
		set.PlannerActor = opts.PlannerActor
	}
	if opts.SecurityActor != "" {
		set.SecurityActor = opts.SecurityActor
	}
	if opts.Security != nil {
		set.Security = *opts.Security
	}
	return set
}

// chainValidate refuses a name or a feature choice before anything exists.
func chainValidate(opts ChainOptions) error {
	if err := store.ValidName(opts.Name); err != nil {
		return refuse("%v", err)
	}
	if len(opts.Name) > chainMaxNameLen {
		return refuse("chain name %q exceeds %d characters (the longest member suffix is -plan)", opts.Name, chainMaxNameLen)
	}
	if err := RequireFeatureChoice(opts.Feature, opts.NoFeature, false); err != nil {
		return err
	}
	if opts.Feature != "" {
		if err := store.ValidFeature(opts.Feature); err != nil {
			return err
		}
	}
	return nil
}

// chainTicket parses --ticket once against the caller's repository, exactly as
// bind and add do.
func chainTicket(ctx context.Context, rt Runtime, raw, repo string) (string, error) {
	if raw == "" {
		return "", nil
	}
	return parseTicket(raw, captureRepo(ctx, rt, repo))
}

// chainPlanBodies reads every plan and refuses an empty or unreadable one.
func chainPlanBodies(paths []string) ([][]byte, error) {
	if len(paths) == 0 {
		return nil, refuse("a chain needs at least one --plan <file>")
	}
	bodies := make([][]byte, len(paths))
	for i, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, refuse("read plan %s: %v", path, err)
		}
		if len(bytes.TrimSpace(body)) == 0 {
			return nil, refuse("plan %s is empty", path)
		}
		bodies[i] = body
	}
	return bodies, nil
}

// chainMembersFor names the parts: the builder is the chain's own name, and
// the security member exists only when the security phase is on.
func chainMembersFor(opts ChainOptions, set chain.Settings) []chainMember {
	members := []chainMember{
		{part: chain.MemberBuilder, name: opts.Name, actor: chainActorOrBuilder(opts.BuilderActor), shape: store.ShapeWriter, writer: true},
		{part: chain.MemberReviewer, name: opts.Name + "-rev", actor: set.ReviewerActor, shape: store.ShapeReader},
		{part: chain.MemberPlanner, name: opts.Name + "-plan", actor: set.PlannerActor, shape: store.ShapeReader},
	}
	if set.Security {
		members = append(members, chainMember{part: chain.MemberSecurity, name: opts.Name + "-sec", actor: set.SecurityActor, shape: store.ShapeReader})
	}
	return members
}

// chainActorOrBuilder is the builder member's actor: the named one, or the
// builder actor.
func chainActorOrBuilder(actor string) string {
	if actor == "" {
		return "builder"
	}
	return actor
}

// chainFreeNames refuses a member name that is invalid, already a binding, or
// already a chain.
func chainFreeNames(rt Runtime, members []chainMember) error {
	for _, m := range members {
		if err := store.ValidName(m.name); err != nil {
			return err
		}
		if _, err := rt.Store.Load(m.name); err == nil {
			return refuse("binding %q already exists: `relevo unbind %s` to start fresh", m.name, m.name)
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if _, err := rt.Store.Chain(m.name); err == nil {
			return refuse("chain %q already exists", m.name)
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}

// chainResolveActors checks every member's actor has the shape its part needs
// and resolves the candidate that member's round will run, all read-only and
// before anything is created. It returns the resolution per part, keyed by the
// part name.
func chainResolveActors(rt Runtime, members []chainMember) (map[string]Resolution, error) {
	out := make(map[string]Resolution, len(members))
	for _, m := range members {
		shape, err := actorShape(rt.RoleRegistry(), m.actor)
		if err != nil {
			return nil, err
		}
		if shape != m.shape {
			return nil, fmt.Errorf("chain %s actor %q must be a %s actor, not a %s", m.part, m.actor, m.shape, shape)
		}
		res, err := resolveRole(rt.RoleRegistry(), rt.Candidates,
			availability.Gates(AvailabilityDeps(rt)), "", bindingRole(store.Binding{Role: normRole(m.actor)}), pickFor(rt))
		if err != nil {
			return nil, err
		}
		out[m.part] = res
	}
	return out, nil
}

// chainBuildMembers builds every member's stored binding: its tier resolved
// from its actor's registry entry, its endpoint from resolveBuilder (so the
// launch is validated before anything is written), and, for the writer only,
// the check and regate budget the chain resolved.
func chainBuildMembers(ctx context.Context, rt Runtime, members []chainMember, resolutions map[string]Resolution, base chainBase, set chain.Settings) ([]store.Binding, error) {
	reg := rt.RoleRegistry()
	built := make([]store.Binding, 0, len(members))
	for _, m := range members {
		res := resolutions[m.part]
		c := res.Candidate
		role := bindingRole(store.Binding{Role: normRole(m.actor)})
		tier := resolveRoleTier("", c, reg, role)
		if !m.writer {
			var err error
			if tier, err = readerTier(tier, c.Harness, rt.Policy); err != nil {
				return nil, err
			}
		}
		if err := checkTierCap(tier, rt.Policy, false); err != nil {
			return nil, err
		}

		opts := BindOptions{Name: m.name, Candidate: c.Ref().String(), Role: m.actor, CWD: base.worktree, Tier: string(tier)}
		ep, _, err := resolveBuilder(ctx, rt, nil, opts, m.name)
		if err != nil {
			return nil, err
		}
		b := chainMemberBinding(m, ep, c.Ref().String(), base)
		b.BuilderAccount = res.Account
		b.Tier = string(tier)
		if m.writer {
			b.Gate = set.Gate
			b.Regate = set.Regate
		}
		built = append(built, b)
	}
	return built, nil
}

// chainMemberBinding is one member's binding. The builder mirrors add's
// literal: its own worktree and branch, the builder role, a writer shape, the
// builder actor's check and regate budget. A reader shares the builder's tree,
// carries its actor name as its role and its shape, and holds no worktree and
// no gate.
func chainMemberBinding(m chainMember, ep store.Endpoint, token string, base chainBase) store.Binding {
	b := store.Binding{
		Name:             m.name,
		CWD:              base.cwd,
		MasterMind:       base.mastermind,
		MasterMindID:     base.mastermindID,
		Builder:          ep,
		BuilderCandidate: token,
		Round:            1,
		State:            store.StateActive,
		Repo:             base.repo,
		RepoRef:          base.repoRef,
		Role:             normRole(m.actor),
		Shape:            m.shape,
		Feature:          base.feature,
		Ticket:           base.ticket,
	}
	if m.writer {
		b.Worktree = base.worktree
		b.Branch = base.branch
		b.Base = base.commit
		b.BaseRef = base.baseRef
	}
	return b
}

// chainRow is the chain row a start writes: status running, phase build, step
// building, plan 1, corrections 0, awaiting the builder's first round, with
// the resolved settings and the four part names.
func chainRow(opts ChainOptions, set chain.Settings, members []chainMember, base chainBase, planPaths []string, now time.Time) (db.ChainRow, error) {
	paths, err := json.Marshal(planPaths)
	if err != nil {
		return db.ChainRow{}, fmt.Errorf("encode chain paths: %w", err)
	}
	settings, err := json.Marshal(set)
	if err != nil {
		return db.ChainRow{}, fmt.Errorf("encode chain settings: %w", err)
	}
	names := map[string]string{}
	for _, m := range members {
		names[m.part] = m.name
	}
	at := now.UTC()
	return db.ChainRow{
		ID:             db.NewID(),
		Name:           opts.Name,
		Status:         string(chain.StatusRunning),
		Phase:          string(chain.PhaseBuild),
		Step:           string(chain.StepBuilding),
		Plan:           1,
		Plans:          len(planPaths),
		PlanPathsJSON:  paths,
		Corrections:    0,
		SettingsJSON:   settings,
		AwaitingMember: chain.MemberBuilder,
		AwaitingRound:  1,
		Builder:        names[chain.MemberBuilder],
		Reviewer:       names[chain.MemberReviewer],
		Planner:        names[chain.MemberPlanner],
		Security:       names[chain.MemberSecurity],
		Base:           base.commit,
		Branch:         base.branch,
		Repo:           base.repo,
		Worktree:       base.worktree,
		Feature:        opts.Feature,
		Ticket:         base.ticket,
		MasterMindID:   base.mastermindID,
		// Plan 1 starts here: the plan-start commit is the commit the
		// builder's worktree was cut from, so plan 1's review can diff the
		// plan's whole span.
		PlanStartCommit: base.commit,
		CreatedAt:       at,
		UpdatedAt:       at,
	}, nil
}
