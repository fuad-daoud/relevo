package doctor

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
)

// bindPreflightTimeout bounds the bind-time preflight. The hot path must not be
// slowed by a hung probe: it reads the filesystem and PATH only.
const bindPreflightTimeout = 2 * time.Second

// assembleKinds is the scope: every kind named by a configured candidate,
// plus every existing binding's builder kind. storeErr is returned rather
// than aborting so the caller renders it as a row.
func assembleKinds(set *candidate.Set, st *store.Store) (kinds []string, storeErr error) {
	seen := make(map[string]bool)
	if set != nil {
		for _, ref := range set.Refs() {
			// Refs() is the set's own canonical keys, so ParseRef cannot fail.
			parsed, _ := candidate.ParseRef(ref)
			c, err := set.Lookup(parsed)
			if err != nil {
				continue
			}
			if c.Harness != "" {
				seen[c.Harness] = true
			}
		}
	}
	if st != nil {
		bindings, err := st.List()
		if err != nil {
			storeErr = fmt.Errorf("could not list bindings: %w", err)
		}
		for _, b := range bindings {
			if b.Builder.Kind != "" {
				seen[b.Builder.Kind] = true
			}
		}
	}
	kinds = make([]string, 0, len(seen))
	for k := range seen {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds, storeErr
}

func accountHomes(accounts account.Set) map[string][]string {
	homes := make(map[string][]string)
	for _, a := range accounts {
		if home, ok := harness.AccountHome(a); ok {
			homes[string(a.Harness)] = append(homes[string(a.Harness)], home)
		}
	}
	return homes
}

// builderDefinitions is what a builder needs installed on its harness:
// the plan-executor and the researcher it dispatches to.
func builderDefinitions() []string {
	spec, _ := harness.RoleByName("builder")
	return append([]string(nil), spec.Definitions...)
}

// legacyRegistryFor is the roles registry derived from set and policy. It
// never errors, so the error is discarded.
func legacyRegistryFor(set *candidate.Set) *roles.Registry {
	reg, _ := roles.Build(nil, set, policy.Policy{})
	return reg
}

// assembleRoleDefinitions is doctor's per-kind role scope read from the
// registry. A kind no candidate names reached doctor through a binding,
// and a binding is always a builder, so it gets the builder's definitions.
func assembleRoleDefinitions(reg *roles.Registry, set *candidate.Set, kinds []string) map[string][]string {
	seen := make(map[string]map[string]bool)
	add := func(kind string, defs []string) {
		if seen[kind] == nil {
			seen[kind] = make(map[string]bool)
		}
		for _, d := range defs {
			seen[kind][d] = true
		}
	}
	if set != nil {
		for _, ref := range set.Refs() {
			parsed, _ := candidate.ParseRef(ref)
			c, err := set.Lookup(parsed)
			if err != nil {
				continue
			}
			for _, role := range reg.Names() {
				if !reg.Serves(role, c.Ref()) {
					continue
				}
				// A Spec error is data, not a failure: the kind is skipped.
				if spec, err := reg.Spec(role, c.Harness); err == nil {
					add(c.Harness, spec.Definitions)
				}
			}
		}
	}
	for _, kind := range kinds {
		if seen[kind] == nil {
			if spec, err := reg.Spec("builder", kind); err == nil {
				add(kind, spec.Definitions)
			}
		}
	}
	out := make(map[string][]string, len(seen))
	for kind, defs := range seen {
		list := make([]string, 0, len(defs))
		for d := range defs {
			list = append(list, d)
		}
		sort.Strings(list)
		out[kind] = list
	}
	return out
}

// assembleDefinitions is assembleRoleDefinitions over the legacy registry.
func assembleDefinitions(set *candidate.Set, kinds []string) map[string][]string {
	return assembleRoleDefinitions(legacyRegistryFor(set), set, kinds)
}

func Render(w io.Writer, rep Report) {
	// 1. Global rows
	for _, c := range rep.Checks {
		if c.Group == "" {
			_, _ = fmt.Fprintf(w, "%-26s%-9s%s\n", c.Name, c.Severity.String(), c.Detail)
			if c.Fix != "" {
				_, _ = fmt.Fprintf(w, "    fix: %s\n", c.Fix)
			}
		}
	}

	// 2. Kind blocks
	var groups []string
	seen := make(map[string]bool)
	for _, c := range rep.Checks {
		if c.Group != "" && !seen[c.Group] {
			seen[c.Group] = true
			groups = append(groups, c.Group)
		}
	}

	for _, g := range groups {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, g)
		for _, c := range rep.Checks {
			if c.Group == g {
				_, _ = fmt.Fprintf(w, "  %-24s%-9s%s\n", c.Name, c.Severity.String(), c.Detail)
				if c.Fix != "" {
					_, _ = fmt.Fprintf(w, "    fix: %s\n", c.Fix)
				}
			}
		}
	}

	// 3. Footer
	_, _ = fmt.Fprintln(w)
	failCount := rep.Failures()
	warnCount := rep.Warnings()

	failPart := fmt.Sprintf("%d failures", failCount)
	if failCount == 1 {
		failPart = "1 failure"
	}
	warnPart := fmt.Sprintf("%d warnings", warnCount)
	if warnCount == 1 {
		warnPart = "1 warning"
	}

	if failCount == 0 {
		if rep.NoCandidates {
			_, _ = fmt.Fprintf(w, "%s, %s -- no candidates configured; run relevo setup.\n", warnPart, failPart)
		} else if !rep.UsableBuilder {
			// Say why: every row can be `ok` and still leave no usable builder,
			// so a bare verdict would point at nothing.
			_, _ = fmt.Fprintf(w, "%s, %s -- could not establish a usable builder: no checked harness has its binary on PATH.\n", warnPart, failPart)
		} else if rep.BuilderRefusal != "" {
			_, _ = fmt.Fprintf(w, "%s, %s -- relevo cannot pick a builder: %s.\n", warnPart, failPart, rep.BuilderRefusal)
		} else {
			_, _ = fmt.Fprintf(w, "%s, %s -- relevo can run.\n", warnPart, failPart)
		}
	} else {
		fixPart := "Fix the failures above."
		if failCount == 1 {
			fixPart = "Fix the failure above."
		}
		_, _ = fmt.Fprintf(w, "%s, %s -- relevo is not ready. %s\n", failPart, warnPart, fixPart)
	}
}

// candidatesCheck is the no-candidates row. Its fix is the one-time setup
// wizard, so the machine's next steps all point at one command.
func candidatesCheck() Check {
	return Check{
		Name:     "candidates",
		Severity: SevWarn,
		Detail:   "none configured",
		Fix:      "relevo setup",
	}
}

// clientKeyReplaced reads what this machine's own file recorded. A row
// that will not read is reported as no swap: a corrupt row is worse to
// interrupt an operator with than a missing warning.
func clientKeyReplaced(cfg *config.Store) ClientKeyReplacedInput {
	swapped, ok, err := cfg.ReadClientKeyReplaced()
	if err != nil || !ok {
		return ClientKeyReplacedInput{}
	}
	return ClientKeyReplacedInput{Swapped: true, From: swapped.From, To: swapped.To}
}

// RunReport runs every check `relevo doctor` has. The cmd-owned facts
// arrive through deps; the bundle calls this so both carry the same report.
// (Named RunReport because Report is the report type.)
func RunReport(rt relevo.Runtime, L config.Loaded, deps Deps) (Report, error) {
	kinds, storeErr := assembleKinds(rt.Candidates, rt.Store)
	env := deps.Env

	opencodeConfigured := false
	for _, k := range kinds {
		if k == "opencode" {
			opencodeConfigured = true
		}
	}

	probeChecks, hasServers := serverProbeChecks(rt, L)

	// The opencode branch checks opencode's own external_directory
	// allowlist against the state root relevo stages plans and reports under.
	// There is no root accessor on rt.Store, so resolve the default root
	// directly. A failure here just leaves the check off.
	stateRoot, _ := tmpdir.DefaultRoot()

	extraChecks := append(probeChecks, runDiskChecks(rt)...)

	pricesBody, _, err := rt.Config.Body(config.Prices)
	if err != nil {
		return Report{}, err
	}

	rep := Run(context.Background(), env, kinds,
		WithDefinitions(assembleRoleDefinitions(rt.RoleRegistry(), rt.Candidates, kinds)),
		WithUsage(pricesBody, opencodeConfigured),
		WithConfigWarnings(rt.ConfigWarnings),
		WithExtraChecks(extraChecks),
		WithStateRoot(stateRoot))
	appendInfraRows(&rep, rt, env, deps, stateRoot, storeErr)
	appendIdentityRows(&rep, rt, hasServers)
	appendServeScopeRows(&rep, rt, env)
	appendPolicyRows(&rep, rt, env, L)
	appendSessionRows(&rep, rt, env, kinds, deps)
	appendHookRows(&rep, rt)

	return rep, nil
}

// agentFileCheck reads the config's carried harness files and the drifted
// ones, plus the rendered definitions of the config's own agents. Anything
// it cannot open leaves what it could not read out.
func agentFileCheck(open func() (*config.Store, error)) Check {
	store, err := open()
	if err != nil {
		return agentFilesUnavailable()
	}
	carried, edited, err := store.CarriedAgentFiles()
	if err != nil {
		return agentFilesUnavailable()
	}
	input := AgentFilesCheckInput{Carried: carried}
	for _, file := range edited {
		input.Edited = append(input.Edited, EditedFile{Path: file.Path, Hash: file.Hash})
	}
	input.Rendered = editedRenderedAgents(store)
	return AgentFilesCheck(input)
}

func agentFilesUnavailable() Check {
	return Check{
		Name: "agent-files", Detail: "agent-files: config store unavailable",
		Severity: SevOK, ProbeFailed: true,
	}
}

// editedRenderedAgents names the rendered definitions of the config's own
// source agents that the role manifest does not vouch for. An install env
// that cannot be opened yields nothing: the native half is still worth
// showing.
func editedRenderedAgents(store *config.Store) []EditedFile {
	env, err := relevo.AgentInstallEnv()
	if err != nil {
		return nil
	}
	loaded, err := store.Load()
	if err != nil {
		return nil
	}
	var out []EditedFile
	for name, entry := range loaded.Agents {
		if entry.Source == "" {
			continue
		}
		files, err := relevo.CustomAgentFiles(store, env, name)
		if err != nil {
			continue
		}
		for _, file := range files {
			if file.State == harness.FileEdited || file.State == harness.FileEditedNewer {
				out = append(out, EditedFile{Path: file.Path})
			}
		}
	}
	return out
}

// hookFileCheck reads the config's carried hook scripts and the drifted
// ones. A config store that cannot be opened leaves the row off.
func hookFileCheck(open func() (*config.Store, error)) Check {
	store, err := open()
	if err != nil {
		return Check{Name: "hook-files", Detail: "hook-files: config store unavailable", Severity: SevOK, ProbeFailed: true}
	}
	doc, err := store.HookDoc()
	if err != nil {
		return Check{Name: "hook-files", Detail: "hook-files: config store unavailable", Severity: SevOK, ProbeFailed: true}
	}
	edited, err := store.DriftedHookFiles()
	if err != nil {
		return Check{Name: "hook-files", Detail: "hook-files: config store unavailable", Severity: SevOK, ProbeFailed: true}
	}
	files := make([]EditedFile, 0, len(edited))
	for _, file := range edited {
		files = append(files, EditedFile{Path: file.Path, Hash: file.Hash})
	}
	return HookFilesCheck(HookFilesCheckInput{Carried: len(doc.Files), Edited: files})
}
