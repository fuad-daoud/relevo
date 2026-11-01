package relevo

import (
	"sort"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainParamFlag is one old flag and the workflow param it fills. A flag the
// chosen workflow has no slot for is refused, so no setting silently takes no
// effect.
type chainParamFlag struct {
	flag  string
	param string
	value string
	given bool
}

// chainParamsFor resolves a workflow's params in precedence order: the
// workflow's own defaults, then the repository's own gate, then policy for
// the shipped default, then the old flags, then --param. It returns only the
// params it overrides; the caller merges them with workflow.WithParams. A
// flag the workflow has no slot for is refused, naming the params it takes.
//
// Policy fills slots only for the shipped default: a custom workflow's own
// defaults are the human's intent, so policy never overwrites them. The
// repository's own gate sits exactly where the policy tier sat, behind the
// flags; the old flags and --param override either.
func chainParamsFor(def workflow.Definition, pol policy.Policy, opts ChainOptions, repo RepoSettings) (map[string]string, error) {
	out := map[string]string{}

	// Policy fills only the shipped default's slots: a custom workflow keeps
	// its own defaults.
	if def.Name == workflow.Default().Name {
		// The gate param takes the repository's own gate before the policy
		// default: the repo tier sits exactly where the policy tier sat.
		gate := pol.GateDefault()
		if repo.Gate != "" {
			gate = repo.Gate
		}
		policyValues := map[string]string{
			"reviewer":        pol.ChainReviewerActor(),
			"planner":         pol.ChainPlannerActor(),
			"security":        pol.ChainSecurityActor(),
			"scan":            strconv.FormatBool(pol.ChainSecurityOn()),
			"gate":            gate,
			"regate":          strconv.Itoa(pol.GateRegate()),
			"max_corrections": strconv.Itoa(pol.ChainMaxCorrections()),
		}
		for name, value := range policyValues {
			if chain.HasParam(def, name) {
				out[name] = value
			}
		}
	}

	for _, f := range chainParamFlags(opts) {
		if !f.given {
			continue
		}
		if !chain.HasParam(def, f.param) {
			return nil, refusal.New("%s: workflow %q has no param %q; it takes: %s",
				f.flag, def.Name, f.param, strings.Join(chainParamNames(def), ", "))
		}
		out[f.param] = f.value
	}

	// --param wins every slot, including one the workflow alone defines.
	for key, value := range opts.Params {
		out[key] = value
	}
	return out, nil
}

// chainParamFlags are the old flags that fill workflow params, each carrying
// only a given flag's params, so the tier above an ungiven flag stands.
func chainParamFlags(opts ChainOptions) []chainParamFlag {
	flags := []chainParamFlag{
		{flag: "--builder-actor", param: "builder", value: opts.BuilderActor, given: opts.BuilderActor != ""},
		{flag: "--reviewer-actor", param: "reviewer", value: opts.ReviewerActor, given: opts.ReviewerActor != ""},
		{flag: "--planner-actor", param: "planner", value: opts.PlannerActor, given: opts.PlannerActor != ""},
		{flag: "--security-actor", param: "security", value: opts.SecurityActor, given: opts.SecurityActor != ""},
	}
	if opts.Security != nil {
		flags = append(flags, chainParamFlag{flag: "--security/--no-security", param: "scan", value: strconv.FormatBool(*opts.Security), given: true})
	}
	switch {
	case opts.NoGate:
		flags = append(flags, chainParamFlag{flag: "--no-gate", param: "gate", given: true})
	case opts.Gate != "":
		flags = append(flags, chainParamFlag{flag: "--gate", param: "gate", value: opts.Gate, given: true})
	}
	if opts.Regate != nil {
		flags = append(flags, chainParamFlag{flag: "--regate", param: "regate", value: strconv.Itoa(*opts.Regate), given: true})
	}
	if opts.MaxCorrections != nil {
		flags = append(flags, chainParamFlag{flag: "--max-corrections", param: "max_corrections", value: strconv.Itoa(*opts.MaxCorrections), given: true})
	}
	return flags
}

// chainParamNames lists a workflow's params, sorted, for the refusal naming
// an old flag the workflow cannot hold.
func chainParamNames(def workflow.Definition) []string {
	names := make([]string, 0, len(def.Params))
	for name := range def.Params {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return []string{"(none)"}
	}
	return names
}
