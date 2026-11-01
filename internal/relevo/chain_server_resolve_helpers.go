package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainServerResolveLocal resolves the workflow and the chain's repository
// read-only: the definition source, the working directory, and its settings.
func chainServerResolveLocal(ctx context.Context, rt Runtime, opts ChainOptions) (workflow.Definition, string, string, RepoSettings, error) {
	fail := func(err error) (workflow.Definition, string, string, RepoSettings, error) {
		return workflow.Definition{}, "", "", RepoSettings{}, err
	}
	if rt.Remote == nil {
		return fail(ErrRemoteUnavailable)
	}
	if rt.Transport == nil {
		return fail(errors.New("no remote transport configured"))
	}
	def, origin, err := resolve.ResolveWorkflow(rt.Config, opts.Workflow)
	if err != nil {
		return fail(err)
	}
	// The chain's repository is resolved before the params are computed: the
	// gate param and the chain settings both read the repository's own
	// settings.
	repo, err := os.Getwd()
	if err != nil {
		return fail(fmt.Errorf("resolve working directory: %w", err))
	}
	repoSet, err := RepoSettingsFor(ctx, rt, repo)
	if err != nil {
		return fail(err)
	}
	return def, origin, repo, repoSet, nil
}

// chainServerResolveBodies computes the workflow params and reads the plan
// bodies read-only: a custom workflow takes any plan count, the shipped
// default takes at least one --plan.
func chainServerResolveBodies(ctx context.Context, rt Runtime, opts ChainOptions, def workflow.Definition, origin, repo string, repoSet RepoSettings) (chainServerPlan, error) {
	values, err := chainParamsFor(def, rt.Policy, opts, repoSet)
	if err != nil {
		return chainServerPlan{}, err
	}
	if def, err = workflow.WithParams(def, values); err != nil {
		return chainServerPlan{}, refusal.New("%v", err)
	}
	checks := resolve.RoleChecks(rt.RoleRegistry(), "builder")
	plan := chainServerPlan{
		def:      def,
		settings: chainSettings(rt.Policy, opts, checks, repoSet),
		gateRes:  resolveGateFor(opts.Gate, opts.NoGate, rt.Policy, checks, repoSet),
		server:   opts.Server,
	}
	plan.repo = repo
	// From the same repository value, not a second working-directory probe.
	plan.subdir = housekeep.ResolveSubdir(ctx, rt.Git, repo)
	plan.trimpath = repoSet.Trimpath
	if origin != "shipped" {
		if err := chainValidateWorkflowOpts(opts); err != nil {
			return chainServerPlan{}, err
		}
		if plan.bodies, err = chainPlanBodiesFor(opts); err != nil {
			return chainServerPlan{}, err
		}
	} else {
		if err := chainValidate(opts); err != nil {
			return chainServerPlan{}, err
		}
		if plan.bodies, err = chainPlanBodies(opts.Plans); err != nil {
			return chainServerPlan{}, err
		}
	}
	if plan.ticket, err = chainTicket(ctx, rt, opts.Ticket, repo); err != nil {
		return chainServerPlan{}, err
	}
	return plan, nil
}

// chainServerResolveGates checks the branch, the mastermind, and the server's
// own features read-only: a server missing what the chain needs is refused
// before anything exists.
func chainServerResolveGates(ctx context.Context, rt Runtime, opts ChainOptions, plan chainServerPlan, origin string) (chainServerPlan, remote.WhoAmI, error) {
	fail := func(err error) (chainServerPlan, remote.WhoAmI, error) {
		return chainServerPlan{}, remote.WhoAmI{}, err
	}
	if err := chainServerBranchFree(ctx, rt, plan.repo, opts.Name); err != nil {
		return fail(err)
	}
	var err error
	if plan.mastermind, plan.mastermindID, err = chainServerMasterMind(rt, opts.MasterMindID); err != nil {
		return fail(err)
	}
	who, err := rt.Remote.WhoAmI(ctx, opts.Server)
	if err != nil {
		return fail(err)
	}
	if missing := remote.MissingChainFeatures(who); len(missing) > 0 {
		return fail(fmt.Errorf("server %s cannot run a chain (missing the %s); upgrade it, or start the chain without --server",
			opts.Server, remote.ChainFeatureList(missing)))
	}
	// The same negative applies to a prefix: the server must store the
	// subdirectory it is to join into every member's rounds, and a server
	// without the feature would drop it silently.
	if plan.subdir != "" && !remote.HasFeature(who, remote.FeatureSubdir) {
		return fail(fmt.Errorf("server %s does not carry binding subdirectories (pre-subdir server); upgrade it or start the chain at the repository root", opts.Server))
	}
	hasWorkflow := remote.HasFeature(who, remote.FeatureWorkflow)
	if origin != "shipped" && !hasWorkflow {
		return fail(fmt.Errorf("server %s cannot run workflow %s (missing the %s)",
			opts.Server, opts.Workflow, remote.ChainFeatureList([]string{remote.FeatureWorkflow})))
	}
	plan.hasWorkflow = hasWorkflow
	if len(opts.Params) > 0 && !hasWorkflow {
		return fail(fmt.Errorf("server %s does not take --param: the server's workflow feature is not available yet; upgrade it, or start the chain without --server", opts.Server))
	}
	if !hasWorkflow {
		plan.members = chainMembersFor(opts, plan.settings)
		if err := chainFreeNames(rt, plan.members); err != nil {
			return fail(err)
		}
	}
	return plan, who, nil
}

// chainServerResolveFacts resolves the git facts a server create needs
// read-only: the base commit, the repository id, and the committing identity.
func chainServerResolveFacts(ctx context.Context, rt Runtime, opts ChainOptions, plan chainServerPlan, who remote.WhoAmI) (chainServerPlan, error) {
	plan.origin = remote.HasFeature(who, remote.FeatureOrigin)
	var err error
	if plan.base, plan.repoID, plan.authorName, plan.authorEmail, err = chainRemoteFacts(ctx, rt, plan.repo, opts.Base); err != nil {
		return chainServerPlan{}, err
	}
	return plan, nil
}
