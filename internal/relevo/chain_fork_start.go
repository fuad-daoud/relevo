package relevo

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// childCreated records what one successfully started child created, so a
// later failure can unwind it.
type childCreated struct {
	name     string
	worktree string
	branch   string
	members  []string
}

// chainStartChild prepares and starts one child workflow chain inside the
// caller's transaction, cutting its branch from the parent writer's tip.
func chainStartChild(ctx context.Context, rt Runtime, tx *store.Tx, parent db.ChainRow, spec workflow.ChildSpec) (childCreated, error) {
	childName := parent.Name + "." + spec.Key
	childDef, _, err := ResolveWorkflow(rt, spec.Workflow)
	if err != nil {
		return childCreated{}, err
	}
	actors := rt.RoleRegistry().WorkflowActors()
	planned, err := chainMemberNames(childName, childDef, actors)
	if err != nil {
		return childCreated{}, err
	}
	if cap := chainNameCap(childName, planned); len(childName) > cap {
		return childCreated{}, refuse("chain name %q exceeds %d characters (the longest member suffix is %s)", childName, cap, longestMemberSuffix(childName, planned))
	}

	bodies, err := chainPlanBodiesFor(ChainOptions{Plans: spec.Plans})
	if err != nil {
		return childCreated{}, err
	}
	given := workflow.Given{Plans: len(bodies) > 0, Task: spec.Task != ""}
	if err := chainValidateWorkflow(rt, childDef, given); err != nil {
		return childCreated{}, err
	}

	keeper := chainWriterKeeper(childDef, workflow.UsedActors(childDef), actors)
	members := chainWorkflowMembers(childDef, planned, keeper)
	if err := chainFreeNames(rt, members); err != nil {
		return childCreated{}, err
	}
	resolutions, err := chainResolveActors(rt, members)
	if err != nil {
		return childCreated{}, err
	}

	childOpts := ChainOptions{
		Name:         childName,
		Parent:       parent.Name,
		Workflow:     spec.Workflow,
		Plans:        spec.Plans,
		Task:         spec.Task,
		Base:         parent.Branch,
		Feature:      parent.Feature,
		NoFeature:    parent.Feature == "",
		Ticket:       parent.Ticket,
		MasterMindID: parent.MasterMindID,
		Server:       parent.Server,
	}

	items := make([]string, len(bodies))
	for i := range bodies {
		items[i] = rt.Store.ChainPlanPath(childName, i+1)
	}
	childPlan := chainWFStart{
		def:          childDef,
		settings:     chainSettings(rt.Policy, childOpts, roleChecks(rt.RoleRegistry(), "builder")),
		repo:         parent.Repo,
		ticket:       parent.Ticket,
		bodies:       bodies,
		planned:      planned,
		keeper:       keeper,
		members:      members,
		resolutions:  resolutions,
		mastermindID: parent.MasterMindID,
		in:           workflow.StartInputs{Plans: items, Task: spec.Task},
	}
	if rec, haveRec, rerr := resolveVerbMasterMind(rt, parent.MasterMindID); rerr == nil && haveRec {
		childPlan.mastermind = recordEndpoint(rec)
	}

	prep, err := chainPrepareWorkflow(ctx, rt, childOpts, childPlan)
	if err != nil {
		return childCreated{}, err
	}
	if _, err := chainPersistAndStartWorkflow(ctx, rt, tx, prep); err != nil {
		chainRollback(ctx, rt, parent.Repo, prep.rowBase.worktree, prep.rowBase.branch)
		return childCreated{}, err
	}

	var memberNames []string
	for _, m := range members {
		memberNames = append(memberNames, m.name)
	}
	return childCreated{
		name:     childName,
		worktree: prep.rowBase.worktree,
		branch:   prep.rowBase.branch,
		members:  memberNames,
	}, nil
}
