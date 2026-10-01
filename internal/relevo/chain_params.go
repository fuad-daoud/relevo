package relevo

import (
	"sort"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainParamFlag is one old chain flag and the workflow param it fills. A flag
// whose param the chosen workflow does not carry is refused, so a human never
// believes a setting took effect when the workflow has no slot for it.
type chainParamFlag struct {
	flag  string
	param string
	value string
	given bool
}

// chainParamsFor resolves the values a workflow's params take, in precedence
// order: the workflow's own defaults, then policy where the workflow carries
// the slot, then the old flags, then --param. It returns only the params it
// overrides; a caller merges them with workflow.WithParams, which is what
// parses and validates the values. A flag the workflow has no slot for is
// refused, naming the params the workflow does take.
func chainParamsFor(def workflow.Definition, pol policy.Policy, opts ChainOptions) (map[string]string, error) {
	out := map[string]string{}

	// Policy fills only the slots the workflow carries; a workflow with no
	// reviewer param simply ignores policy's reviewer.
	policyValues := map[string]string{
		"reviewer":        pol.ChainReviewerActor(),
		"planner":         pol.ChainPlannerActor(),
		"security":        pol.ChainSecurityActor(),
		"scan":            strconv.FormatBool(pol.ChainSecurityOn()),
		"gate":            pol.GateDefault(),
		"regate":          strconv.Itoa(pol.GateRegate()),
		"max_corrections": strconv.Itoa(pol.ChainMaxCorrections()),
	}
	for name, value := range policyValues {
		if hasChainParam(def, name) {
			out[name] = value
		}
	}

	for _, f := range chainParamFlags(opts) {
		if !f.given {
			continue
		}
		if !hasChainParam(def, f.param) {
			return nil, refuse("%s: workflow %q has no param %q; it takes: %s",
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

// chainParamFlags is the old flags that fill workflow params, each carrying
// only the params it was actually given. A flag that was not given is left out,
// so the tier above it stands.
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

// hasChainParam reports whether a workflow declares the named param.
func hasChainParam(def workflow.Definition, name string) bool {
	_, ok := def.Params[name]
	return ok
}

// chainParamNames lists a workflow's params, sorted, for the refusal that names
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
