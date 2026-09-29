package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/classify"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// bindPreflightTimeout bounds the bind-time preflight. The hot path must not be
// slowed by a hung probe: it reads the filesystem and PATH only.
const bindPreflightTimeout = 2 * time.Second

// assembleKinds is the scope: every kind named by a configured candidate,
// plus every existing binding's builder kind. storeErr is returned rather than
// aborting -- a diagnostic that refuses to diagnose because one of its own
// inputs is unreadable is worse than one that reports the gap, so the caller
// renders it as a row and checks the kinds it did find.
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

// builderDefinitions is what a builder needs installed on its harness:
// the plan-executor and the researcher it dispatches to (#166 §3).
func builderDefinitions() []string {
	spec, _ := harness.RoleByName("builder")
	return append([]string(nil), spec.Definitions...)
}

// legacyRegistryFor is the roles registry derived from set and policy: what
// every caller meant before roles.json existed (#374 §3.5). Legacy mode never
// errors, so the error is discarded.
func legacyRegistryFor(set *candidate.Set) *roles.Registry {
	reg, _ := roles.Build(nil, set, policy.Policy{})
	return reg
}

// assembleRoleDefinitions is doctor's per-kind role scope read from the
// registry (#374 §3.5): the definitions some candidate on that harness would
// load, given the roles the registry says serve it. A kind in kinds that no
// candidate names reached doctor through a binding, and a binding is always a
// builder, so it gets the builder's definitions for that kind.
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
				// A Spec error is data, not a failure: the kind is skipped
				// (#374 §4).
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

// assembleDefinitions is assembleRoleDefinitions over the legacy registry: the
// behaviour every pre-roles.json caller had.
func assembleDefinitions(set *candidate.Set, kinds []string) map[string][]string {
	return assembleRoleDefinitions(legacyRegistryFor(set), set, kinds)
}

func renderReport(w io.Writer, rep doctor.Report) {
	// 1. Global rows
	for _, c := range rep.Checks {
		if c.Group == "" {
			fmt.Fprintf(w, "%-26s%-9s%s\n", c.Name, c.Severity.String(), c.Detail)
			if c.Fix != "" {
				fmt.Fprintf(w, "    fix: %s\n", c.Fix)
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
		fmt.Fprintln(w)
		fmt.Fprintln(w, g)
		for _, c := range rep.Checks {
			if c.Group == g {
				fmt.Fprintf(w, "  %-24s%-9s%s\n", c.Name, c.Severity.String(), c.Detail)
				if c.Fix != "" {
					fmt.Fprintf(w, "    fix: %s\n", c.Fix)
				}
			}
		}
	}

	// 3. Footer
	fmt.Fprintln(w)
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
			fmt.Fprintf(w, "%s, %s -- no candidates configured; set them with relevo config set candidates first.\n", warnPart, failPart)
		} else if !rep.UsableBuilder {
			// Say why. Every row can be `ok` and still leave no usable builder --
			// a machine whose only alias names a kind relevo was not taught reads
			// as entirely healthy, so a bare verdict would point at nothing.
			fmt.Fprintf(w, "%s, %s -- could not establish a usable builder: no checked harness has its binary on PATH.\n", warnPart, failPart)
		} else if rep.BuilderRefusal != "" {
			fmt.Fprintf(w, "%s, %s -- relevo cannot pick a builder: %s.\n", warnPart, failPart, rep.BuilderRefusal)
		} else {
			fmt.Fprintf(w, "%s, %s -- relevo can run.\n", warnPart, failPart)
		}
	} else {
		fixPart := "Fix the failures above."
		if failCount == 1 {
			fixPart = "Fix the failure above."
		}
		fmt.Fprintf(w, "%s, %s -- no usable builder. %s\n", failPart, warnPart, fixPart)
	}
}

// roleSourceChecks is the one global row saying where the roles came from,
// plus one warning row per legacy field roles.json makes irrelevant (#374
// §3.5). In legacy mode there is nothing stale to warn about, so only the OK
// row is printed.
func roleSourceChecks(reg *roles.Registry, set *candidate.Set, pol policy.Policy) []doctor.Check {
	detail := "legacy: candidates.json roles, policy.json order and tier"
	if reg.FileMode() {
		detail = "config actors"
	}
	out := []doctor.Check{{Name: "actor source", Severity: doctor.SevOK, Detail: detail}}
	for _, w := range relevo.LegacyRoleFieldWarnings(reg, set, pol) {
		out = append(out, doctor.Check{
			Name:     "actor source",
			Severity: doctor.SevWarn,
			Detail:   w,
			Fix:      "delete it; config actors is the source",
		})
	}
	return out
}

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("relevo doctor", flag.ContinueOnError)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	kinds, storeErr := assembleKinds(rt.Candidates, rt.Store)
	env := doctor.NewEnv(rt.Store, releaseInputs())

	opencodeConfigured := false
	for _, k := range kinds {
		if k == "opencode" {
			opencodeConfigured = true
		}
	}
	L, err := rt.Config.Load()
	if err != nil {
		return err
	}

	// One doctor row per configured server (remote-builders spec §5.5):
	// reachable and enrolled, not enrolled (with the line to give the
	// admin), unreachable, or a changed certificate. No servers section ->
	// no rows.
	hasServers := false
	var extraChecks []doctor.Check
	if len(L.Servers) > 0 {
		hasServers = true
		enrollLine := ""
		if key, kerr := remote.ParsePrivate(L.ClientKey); kerr == nil {
			enrollLine = client.EnrollLine(key)
		}
		extraChecks = serverChecks(relevo.ProbeServers(context.Background(), rt, L.Servers, enrollLine))
	}

	// #236: the opencode branch checks opencode's own external_directory
	// allowlist against the state root relevo stages plans and reports under.
	// store has no root accessor on rt.Store, so resolve it the way
	// newRuntime did (main.go). A failure here just leaves the check off.
	stateRoot, _ := store.DefaultRoot()

	pricesBody, _, err := rt.Config.Body(config.Prices)
	if err != nil {
		return err
	}

	rep := doctor.Run(context.Background(), env, kinds,
		doctor.WithDefinitions(assembleRoleDefinitions(rt.RoleRegistry(), rt.Candidates, kinds)),
		doctor.WithUsage(pricesBody, opencodeConfigured),
		doctor.WithConfigWarnings(rt.ConfigWarnings),
		doctor.WithExtraChecks(extraChecks),
		doctor.WithStateRoot(stateRoot))
	// #370: whether a restart right now would kill anything, read from the
	// cgroup every running local process actually sits in. One call, placed
	// directly after the daemon row.
	rep.Checks = insertRestartRow(rep.Checks, restartCheck(rt))
	// #P3d §4.7: one `database` row replaces `relevo db path` and
	// `relevo db stats`. Every open migrates, so there is no separate
	// migrate row.
	rep.Checks = insertGlobalCheck(rep.Checks, databaseCheck(rt.Store))
	if storeErr != nil {
		rep.Checks = insertGlobalCheck(rep.Checks, doctor.Check{
			Name:        "bindings",
			Severity:    doctor.SevWarn,
			Detail:      storeErr.Error(),
			ProbeFailed: true,
		})
	}
	// #335: a remote builder commits as the client, so a repo whose effective
	// user.name/user.email is unset makes `relevo bind --server` refuse. The row
	// exists only where a server is configured and the cwd is inside a repo; a
	// git failure (no repo, no git) is not established, so it is no row.
	identity := doctor.GitIdentityInput{HasServers: hasServers}
	if rt.Git != nil {
		if wd, wdErr := os.Getwd(); wdErr == nil {
			if name, email, identErr := rt.Git.Identity(context.Background(), wd); identErr == nil {
				identity.InRepo, identity.Name, identity.Email = true, name, email
			}
		}
	}
	if c, ok := doctor.GitIdentityCheck(identity); ok {
		rep.Checks = insertGlobalCheck(rep.Checks, c)
	}

	if rt.Candidates.Len() == 0 {
		rep.NoCandidates = true
		rep.Checks = insertGlobalCheck(rep.Checks, doctor.Check{
			Name:     "candidates",
			Severity: doctor.SevWarn,
			Detail:   "none configured",
			Fix:      `relevo config set candidates '[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]'`,
		})
	}

	if stateRoot, err := store.DefaultRoot(); err == nil {
		serveRoot := filepath.Join(stateRoot, "serve")
		// The serve checks read the machine database (P5 §4.7). It is the same
		// database rt.Store holds; a store whose open failed leaves the serve
		// rows off, as every other best-effort row does.
		var serveDB *db.DB
		if d, derr := rt.Store.DB(); derr == nil {
			serveDB = d
		}
		rep.Checks = append(rep.Checks, doctor.ServeChecks(env, serveDB, serveRoot, time.Now())...)
	}

	// #314: when a scope block asks for cpu pinning, doctor confirms the user
	// manager has cpuset delegated (a silently ignored AllowedCPUs shows no
	// exit code anywhere else) and that the pool names only this host's cores.
	// A block whose scope is off is skipped, the same rule scopeFromPolicy
	// applies.
	var scopeBlocks []doctor.ScopeBlock
	if sc := enabledScope(rt.Policy.Scope); sc != nil {
		scopeBlocks = append(scopeBlocks, scopeBlock("scope.allowed_cpus", sc))
	}
	if rt.Policy.Serve != nil {
		if sc := enabledScope(rt.Policy.Serve.Scope); sc != nil {
			scopeBlocks = append(scopeBlocks, scopeBlock("serve.scope.allowed_cpus", sc))
		}
	}
	rep.Checks = append(rep.Checks, doctor.ScopeChecks(env, scopeBlocks, doctor.UserManagerControllersPath(os.Getuid()), runtime.NumCPU())...)

	rep.Checks = append(rep.Checks, ledgerChecks(availability.Gates(relevo.AvailabilityDeps(rt)))...)
	rep.Checks = append(rep.Checks, policyChecks(relevo.PolicyWarningsFor(rt.RoleRegistry(), rt.Candidates, rt.Policy))...)
	rep.Checks = append(rep.Checks, roleSourceChecks(rt.RoleRegistry(), rt.Candidates, rt.Policy)...)
	// #382 §5.4: a binding whose role roles.json no longer defines fails at
	// round start, so doctor names it. A store error skips the rows silently,
	// as mastermindCheckInput treats its own store read.
	if bindings, err := rt.Store.List(); err == nil {
		rep.Checks = append(rep.Checks, doctor.BindingRoleChecks(bindings, func(r string) bool {
			_, ok := rt.RoleRegistry().Role(r)
			return ok
		})...)
	}
	_, st := classify.Resolve(rt.Policy.Classify, L.Typesafe, os.Getenv)
	rep.Checks = append(rep.Checks, doctor.ClassifyCheck(st))
	refusals := relevo.RoleRefusalsFor(rt.RoleRegistry(), rt.Candidates, rt.Policy, availability.Gates(relevo.AvailabilityDeps(rt)))
	rep.Checks = append(rep.Checks, refusalChecks(refusals)...)
	for _, r := range refusals {
		if r.Role == "builder" {
			rep.BuilderRefusal = r.Text
			break
		}
	}

	rep.Checks = append(rep.Checks, doctor.MasterMindChecks(mastermindCheckInput(rt, kinds))...)

	// The hooks row: how many runs the machine database's run log holds, how
	// many failed in the last day and what the last failure was (P3b round 2
	// §4.4). A database relevo cannot read leaves the row off, as every other
	// best-effort row does.
	if d, derr := rt.Store.DB(); derr == nil {
		if runs, rerr := hooks.NewKVLog(db.TxKV{DB: d}, filepath.Dir(rt.Store.DBPath())).Runs(); rerr == nil {
			rep.Checks = append(rep.Checks, doctor.HooksCheck(doctor.HooksCheckInput{Runs: runs, Now: rt.Now()}))
		}
	}

	renderReport(os.Stdout, rep)

	if rep.Failures() > 0 {
		return exitCodeErr{code: 1}
	}
	return nil
}
