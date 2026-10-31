package relevo

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/resolve"
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
	childDef, _, err := resolve.ResolveWorkflow(rt.Config, spec.Workflow)
	if err != nil {
		return childCreated{}, err
	}
	actors := rt.RoleRegistry().WorkflowActors()
	planned, err := chain.MemberNames(childName, childDef, actors)
	if err != nil {
		return childCreated{}, err
	}
	if cap := chain.NameCap(childName, planned); len(childName) > cap {
		return childCreated{}, refusal.New("chain name %q exceeds %d characters (the longest member suffix is %s)", childName, cap, chain.LongestMemberSuffix(childName, planned))
	}

	bodies, err := chainPlanBodiesFor(ChainOptions{Plans: spec.Plans})
	if err != nil {
		return childCreated{}, err
	}
	given := workflow.Given{Plans: len(bodies) > 0, Task: spec.Task != ""}
	if err := chainValidateWorkflow(rt, childDef, given); err != nil {
		return childCreated{}, err
	}

	keeper := chain.WriterKeeper(childDef, workflow.UsedActors(childDef), actors)
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
	// The child's repository is the parent's: the child's branch is cut from
	// the parent's branch, so the settings that name a gate read the same
	// tree's own settings the parent's start read.
	childRepoSet, err := RepoSettingsFor(ctx, rt, parent.Repo)
	if err != nil {
		return childCreated{}, err
	}
	childPlan := chainWFStart{
		def:          childDef,
		settings:     chainSettings(rt.Policy, childOpts, resolve.RoleChecks(rt.RoleRegistry(), "builder"), childRepoSet),
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
	if rec, haveRec, rerr := resolveVerbMasterMindHeal(rt, parent.MasterMindID, false); rerr == nil && haveRec {
		childPlan.mastermind = recordEndpoint(rec)
	}
	// The child's members are built beside the parent's builder member, so
	// they inherit its subdirectory rather than re-resolving one: the fork
	// cuts from parent.Repo, the root checkout, whose own prefix names an
	// offset nothing here started a shell in.
	if parentBuilder, lerr := tx.Load(parent.Builder); lerr == nil {
		childPlan.subdir = parentBuilder.Subdir
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
