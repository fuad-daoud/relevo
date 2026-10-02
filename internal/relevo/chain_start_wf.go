package relevo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainWFStart is everything a workflow start resolved before anything existed.
type chainWFStart struct {
	def          workflow.Definition
	settings     chain.Settings
	repo         string
	ticket       string
	bodies       [][]byte
	in           workflow.StartInputs
	planned      []plannedMember
	members      []chainMember
	resolutions  map[string]Resolution
	placements   map[string]PlacementResolution
	remote       *chainRemotePlan
	mastermind   store.Endpoint
	mastermindID string
	keeper       string
}

// chainStartWorkflow starts a chain that runs the named user workflow: it
// resolves and validates the definition against the actors present, cuts the
// worktree, creates one member per actor the workflow uses, stores the
// definition and the engine's start state, and runs the start actions.
func chainStartWorkflow(ctx context.Context, rt Runtime, opts ChainOptions) (ChainResult, error) {
	plan, err := chainResolveWorkflowStart(ctx, rt, opts)
	if err != nil {
		return ChainResult{}, err
	}
	return chainCreateWorkflow(ctx, rt, opts, plan)
}

// chainResolveWorkflowStart runs every precondition of a workflow start
// read-only: the definition, its params, the inputs, the actors, the members
// and the caller's mastermind.
func chainResolveWorkflowStart(ctx context.Context, rt Runtime, opts ChainOptions) (chainWFStart, error) {
	def, _, err := ResolveWorkflow(rt, opts.Workflow)
	if err != nil {
		return chainWFStart{}, err
	}
	values, err := chainParamsFor(def, rt.Policy, opts)
	if err != nil {
		return chainWFStart{}, err
	}
	if def, err = workflow.WithParams(def, values); err != nil {
		return chainWFStart{}, refuse("%v", err)
	}
	plan := chainWFStart{def: def, settings: chainSettings(rt.Policy, opts, roleChecks(rt.RoleRegistry(), "builder"))}
	if err := chainRefuseReaderBuilder(def, rt.RoleRegistry().WorkflowActors()); err != nil {
		return chainWFStart{}, err
	}

	repo, err := os.Getwd()
	if err != nil {
		return chainWFStart{}, fmt.Errorf("resolve working directory: %w", err)
	}
	plan.repo = repo
	if err := chainValidateWorkflowOpts(opts); err != nil {
		return chainWFStart{}, err
	}
	if plan.ticket, err = chainTicket(ctx, rt, opts.Ticket, repo); err != nil {
		return chainWFStart{}, err
	}
	if plan.bodies, err = chainPlanBodiesFor(opts); err != nil {
		return chainWFStart{}, err
	}
	given := workflow.Given{Plans: len(plan.bodies) > 0, Task: opts.Task != ""}
	if err := chainValidateWorkflow(rt, def, given); err != nil {
		return chainWFStart{}, err
	}

	actors := rt.RoleRegistry().WorkflowActors()
	planned, err := chainMemberNames(opts.Name, def, actors)
	if err != nil {
		return chainWFStart{}, err
	}
	plan.planned = planned
	plan.keeper = chainWriterKeeper(def, workflow.UsedActors(def), actors)
	if cap := chainNameCap(opts.Name, planned); len(opts.Name) > cap {
		return chainWFStart{}, refuse("chain name %q exceeds %d characters (the longest member suffix is %s)", opts.Name, cap, longestMemberSuffix(opts.Name, planned))
	}
	plan.members = chainWorkflowMembers(def, planned, plan.keeper)
	if err := chainFreeNames(rt, plan.members); err != nil {
		return chainWFStart{}, err
	}
	if plan.resolutions, err = chainResolveActors(rt, plan.members); err != nil {
		return chainWFStart{}, err
	}
	synth := chainStartPlan{repo: repo, ticket: plan.ticket, members: plan.members, resolutions: plan.resolutions}
	if plan.placements, err = chainResolvePlacements(ctx, rt, opts, synth); err != nil {
		return chainWFStart{}, err
	}
	for part, p := range plan.placements {
		res := plan.resolutions[part]
		res.Placement = p
		plan.resolutions[part] = res
	}
	if err := chainRefuseTwoCheckCommandsOnPlacedWriter(def, plan); err != nil {
		return chainWFStart{}, err
	}

	rec, haveRec, err := resolveVerbMasterMind(rt, opts.MasterMindID)
	if err != nil {
		return chainWFStart{}, err
	}
	if !haveRec {
		return chainWFStart{}, ErrNoMasterMindSession
	}
	plan.mastermindID = rec.ID
	plan.mastermind = recordEndpoint(rec)

	items := make([]string, len(plan.bodies))
	for i := range plan.bodies {
		items[i] = rt.Store.ChainPlanPath(opts.Name, i+1)
	}
	plan.in = workflow.StartInputs{Plans: items, Task: opts.Task}

	if !plan.placements[chain.MemberBuilder].local() {
		ropts := opts
		ropts.BuilderActor = plan.keeper
		synth.repo = repo
		if plan.remote, err = chainRemotePreflight(ctx, rt, ropts, synth, plan.placements[chain.MemberBuilder]); err != nil {
			return chainWFStart{}, err
		}
	}
	return plan, nil
}

// chainRefuseReaderBuilder refuses a workflow whose builder param names an
// actor that is not a writer: that param names the member that owns the chain's
// tree, so a reader there would leave the chain with no writer member at all.
// A workflow with no builder param is free to name no writer, and every member
// then takes the "<chain>-<actor>" form.
func chainRefuseReaderBuilder(def workflow.Definition, actors map[string]workflow.ActorInfo) error {
	p, ok := def.Params["builder"]
	if !ok || p.Kind != workflow.ParamString {
		return nil
	}
	name := workflow.RenderParams(def, "{{params.builder}}")
	if info, ok := actors[name]; ok && info.Shape != workflow.ShapeWriter {
		return refuse("chain builder actor %q must be a writer actor, not a reader", name)
	}
	return nil
}

// chainValidateWorkflowOpts refuses a bad name or feature choice before
// anything exists. The chain-name cap is the workflow's own, so it is checked
// after the members are resolved.
func chainValidateWorkflowOpts(opts ChainOptions) error {
	if err := store.ValidName(opts.Name); err != nil {
		return refuse("%v", err)
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

// chainPlanBodiesFor reads the plan copies a workflow start holds: none when
// none were given, the bodies otherwise. The workflow's own input rule decides
// whether that many is allowed.
func chainPlanBodiesFor(opts ChainOptions) ([][]byte, error) {
	if len(opts.Plans) == 0 {
		return nil, nil
	}
	return chainPlanBodies(opts.Plans)
}

// chainValidateWorkflow validates a workflow against the runtime's actors and
// the inputs given, refusing with the first problem.
func chainValidateWorkflow(rt Runtime, def workflow.Definition, given workflow.Given) error {
	env := workflow.Env{
		Actors: rt.RoleRegistry().WorkflowActors(),
		Given:  &given,
		Seeds:  workflow.ShippedSeeds(),
		Workflow: func(name string) (workflow.Definition, bool) {
			d, _, err := ResolveWorkflow(rt, name)
			return d, err == nil
		},
	}
	problems := workflow.Validate(def, env)
	if len(problems) > 0 {
		p := problems[0]
		if p.Rule == workflow.RuleKind && strings.Contains(p.Detail, "unknown actor") {
			return classed(ErrUnknownRole, p.String())
		}
		return refuse("%s", p)
	}
	return nil
}

// chainWorkflowMembers converts the planned members into the chainMember shape
// the member builders take. The keeper writer takes the builder part; the
// shipped default's reviewer, planner and security params name their parts;
// every other actor keeps its own name as its part.
func chainWorkflowMembers(def workflow.Definition, planned []plannedMember, keeper string) []chainMember {
	parts := chainDefaultParts(def)
	out := make([]chainMember, 0, len(planned))
	for _, m := range planned {
		shape := store.ShapeReader
		if m.Writer {
			shape = store.ShapeWriter
		}
		part := m.Actor
		if p, ok := parts[m.Actor]; ok {
			part = p
		}
		if keeper != "" && m.Actor == keeper {
			part = chain.MemberBuilder
		}
		out = append(out, chainMember{part: part, name: m.Name, actor: m.Actor, shape: shape, writer: m.Writer})
	}
	// The legacy four parts keep their historical order -- builder, reviewer,
	// planner, security -- so a terminal delivery names the member it always
	// did; every other actor follows, in the sorted actor order planned holds.
	slices.SortStableFunc(out, func(a, b chainMember) int {
		return chainPartRank(a.part) - chainPartRank(b.part)
	})
	return out
}

// chainPartRank orders a member's part after the legacy four, which come first
// in the order the fixed state machine named them. Any other part is last.
func chainPartRank(part string) int {
	switch part {
	case chain.MemberBuilder:
		return 0
	case chain.MemberReviewer:
		return 1
	case chain.MemberPlanner:
		return 2
	case chain.MemberSecurity:
		return 3
	}
	return 4
}

// chainRefuseTwoCheckCommandsOnPlacedWriter refuses a workflow that places a
// writer on a server and carries two different non-empty check commands: the
// client can answer one command from the writer's pulled gate record, but not
// two. The refusal names the server and the feature the server would need.
func chainRefuseTwoCheckCommandsOnPlacedWriter(def workflow.Definition, plan chainWFStart) error {
	placed := ""
	for _, m := range plan.members {
		if m.writer {
			if p := plan.placements[m.part]; !p.local() {
				placed = p.Name
			}
		}
	}
	if placed == "" {
		return nil
	}
	if len(chainCheckCommands(def)) <= 1 {
		return nil
	}
	return refuse("chain %s places a writer on server %s, whose workflow runs two different check commands: the server feature %q is not available yet", plan.planned[0].Name, placed, "check")
}

// chainCheckCommands returns the distinct non-empty check commands a workflow's
// check steps render.
func chainCheckCommands(def workflow.Definition) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range workflow.ResumeTargets(def) {
		step := def.Steps[id]
		if step.Check == "" {
			continue
		}
		if cmd := workflow.RenderParams(def, step.Check); cmd != "" && !seen[cmd] {
			seen[cmd] = true
			out = append(out, cmd)
		}
	}
	return out
}

// chainSingleCheckCommand is the one non-empty check command a placed writer's
// binding carries as its own gate, or "" when the workflow has none.
func chainSingleCheckCommand(def workflow.Definition) string {
	cmds := chainCheckCommands(def)
	if len(cmds) == 1 {
		return cmds[0]
	}
	return ""
}

// chainCreateWorkflow is the half of a workflow start that creates things.
func chainCreateWorkflow(ctx context.Context, rt Runtime, opts ChainOptions, plan chainWFStart) (ChainResult, error) {
	var (
		built   []store.Binding
		rowBase chainBase
		unwind  func()
	)
	remote := len(plan.placements) > 0 && !plan.placements[chain.MemberBuilder].local()
	if remote {
		synth := chainStartPlan{
			settings: plan.settings, repo: plan.repo, ticket: plan.ticket,
			members: plan.members, resolutions: plan.resolutions, remote: plan.remote,
		}
		wb, un, err := chainBuildRemoteBuilder(ctx, rt, opts, synth)
		if err != nil {
			return ChainResult{}, err
		}
		unwind = un
		built = append(built, wb)
		readersBase := chainBase{
			cwd: plan.repo, mastermind: plan.mastermind, mastermindID: plan.mastermindID,
			repo: plan.repo, repoRef: captureRepo(ctx, rt, plan.repo), feature: opts.Feature, ticket: plan.ticket,
			worktree: plan.repo,
		}
		var readers []chainMember
		for _, m := range plan.members {
			if !m.writer {
				readers = append(readers, m)
			}
		}
		rb, err := chainBuildMembers(ctx, rt, readers, plan.resolutions, readersBase, plan.settings)
		if err != nil {
			unwind()
			return ChainResult{}, err
		}
		built = append(built, rb...)
		rowBase = readersBase
		rowBase.worktree = ""
		rowBase.branch = "relevo/" + opts.Name
		rowBase.commit = plan.remote.base
	} else {
		worktree, branch, commit, baseRef, err := cutWorktree(ctx, rt, opts.Name, plan.repo, opts.Base)
		if err != nil {
			return ChainResult{}, err
		}
		rowBase = chainBase{
			cwd: worktree, mastermind: plan.mastermind, mastermindID: plan.mastermindID,
			repo: plan.repo, repoRef: captureRepo(ctx, rt, plan.repo), feature: opts.Feature, ticket: plan.ticket,
			worktree: worktree, branch: branch, commit: commit, baseRef: baseRef,
		}
		locals, berr := chainBuildMembers(ctx, rt, plan.members, plan.resolutions, rowBase, plan.settings)
		if berr != nil {
			chainRollback(ctx, rt, plan.repo, worktree, branch)
			return ChainResult{}, berr
		}
		built = locals
	}
	defer func() {
		if unwind != nil {
			unwind()
		}
	}()

	// A workflow chain runs its checks as steps, so a local writer carries no
	// gate of its own; a placed writer's binding carries the one check command
	// its server runs on the writer's completion marker.
	gate := ""
	if remote {
		gate = chainSingleCheckCommand(plan.def)
	}
	for i := range built {
		if chainMemberWriter(plan.members, built[i].Name) {
			built[i].Gate = gate
			built[i].Regate = 0
		}
	}

	planPaths := make([]string, len(plan.bodies))
	for i := range plan.bodies {
		planPaths[i] = rt.Store.ChainPlanPath(opts.Name, i+1)
	}
	now := time.Now
	if rt.Now != nil {
		now = rt.Now
	}
	row, err := chainRow(opts, plan.settings, plan.members, rowBase, planPaths, now())
	if err != nil {
		if !remote {
			chainRollback(ctx, rt, plan.repo, rowBase.worktree, rowBase.branch)
		}
		return ChainResult{}, err
	}
	defJSON, err := json.Marshal(plan.def)
	if err != nil {
		return ChainResult{}, fmt.Errorf("encode chain workflow: %w", err)
	}
	start, acts := workflow.Start(plan.def, plan.in)
	stateJSON, err := json.Marshal(start)
	if err != nil {
		return ChainResult{}, fmt.Errorf("encode chain state: %w", err)
	}
	row.WorkflowJSON = defJSON
	row.StateJSON = stateJSON
	row.Status = string(start.Status)
	row.Plans = len(planPaths)
	row.Plan = 1
	applyChainLegacy(&row, plan.def, start)

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.CreateChain(row, built); err != nil {
			return err
		}
		return chainAppendPickNotes(tx, rt, plan.members, plan.resolutions)
	}); err != nil {
		if !remote {
			chainRollback(ctx, rt, plan.repo, rowBase.worktree, rowBase.branch)
		}
		return ChainResult{}, err
	}
	unwind = nil

	stored, err := chainStoredMembers(rt, plan.members)
	if err != nil {
		return ChainResult{}, err
	}
	if err := chainCopyPlans(rt, opts.Name, plan.bodies); err != nil {
		return ChainResult{}, fmt.Errorf("chain %q started, but copying its plans failed: %w", opts.Name, err)
	}
	if opts.Task != "" {
		if err := writeChainTask(rt, opts.Name, opts.Task); err != nil {
			return ChainResult{}, fmt.Errorf("chain %q started, but writing its task failed: %w", opts.Name, err)
		}
	}

	if err := chainRunStartActions(ctx, rt, opts.Name, plan.def, start, acts); err != nil {
		return ChainResult{}, err
	}
	if remote {
		if err := chainSendPending(ctx, rt); err != nil {
			return ChainResult{}, fmt.Errorf("chain %q started, but its remote member could not be handed its round: %w", opts.Name, err)
		}
	}
	// The start actions opened the first round; the result carries the row as
	// the store now holds it, so its awaiting round is the one just sent.
	if current, cerr := rt.Store.Chain(opts.Name); cerr == nil {
		row = current
	}
	return ChainResult{Chain: row, Members: stored, Plans: len(plan.bodies), Check: gate}, nil
}

// chainRunStartActions runs a start's actions in one critical section, after
// the row exists.
func chainRunStartActions(ctx context.Context, rt Runtime, name string, def workflow.Definition, start workflow.State, acts []workflow.Action) error {
	return rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain(name)
		if err != nil {
			return err
		}
		st, err := chainWorkflowState(c)
		if err != nil {
			return err
		}
		for _, act := range acts {
			if err := chainRunAction(ctx, rt, tx, c, def, st, &st, workflow.Event{}, act); err != nil {
				return err
			}
		}
		return nil
	})
}

// chainMemberWriter reports whether the named member is one the workflow's
// writers.
func chainMemberWriter(members []chainMember, name string) bool {
	for _, m := range members {
		if m.name == name {
			return m.writer
		}
	}
	return false
}

// writeChainTask writes a chain's task-input copy so {{task}} can render it.
func writeChainTask(rt Runtime, name, task string) error {
	return os.WriteFile(rt.Store.ChainTaskPath(name), []byte(task), 0o644)
}
