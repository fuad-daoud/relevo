package doctor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/classify"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
)

// serverProbeChecks builds the pre-run rows for the configured servers and
// reports whether any server is configured. No servers section means no
// rows.
func serverProbeChecks(rt relevo.Runtime, L config.Loaded) (checks []Check, hasServers bool) {
	if len(L.Servers) == 0 {
		return nil, false
	}
	enrollLine := ""
	if key, kerr := remote.ParsePrivate(L.ClientKey); kerr == nil {
		enrollLine = client.EnrollLine(key)
	}
	return serverChecks(relevo.ProbeServers(context.Background(), rt, L.Servers, enrollLine)), true
}

// appendInfraRows adds the machine-local global rows.
func appendInfraRows(rep *Report, rt relevo.Runtime, env Env, deps Deps, stateRoot string, storeErr error) {
	// Whether a restart right now would kill anything, read from the cgroup
	// every local process sits in; one call, placed directly after the
	// daemon row.
	rep.Checks = deps.InsertRestartRow(rep.Checks, deps.RestartCheck(rt))
	// One `database` row carries the migrated state: every open migrates.
	rep.Checks = InsertGlobalCheck(rep.Checks, databaseCheck(rt.Store))
	// The sync quota row, read from the local file; no reading, no row.
	if check, ok := syncQuotaRow(rt, deps.SyncMeter); ok {
		rep.Checks = InsertGlobalCheck(rep.Checks, check)
	}
	if machineDB := serveMachineDB(rt); machineDB != nil {
		if rounds, bindings, err := machineDB.OpenRoundsOnFinishedBindings(); err == nil {
			rep.Checks = InsertGlobalCheck(rep.Checks, openRoundsCheck(rounds, bindings))
		}
	}
	// The engine row sits next to the database row, so an unloadable
	// library is visible beside the database it would open.
	rep.Checks = InsertGlobalCheck(rep.Checks, engineCheck(db.EngineStatus(stateRoot)))
	// The client-id row: a fleet's stamped key replacing this machine's own
	// means the enrolled servers cease to recognise it. Read from the
	// machine's own file, so it answers with the network blackholed.
	if check, ok := ClientKeyCheck(clientKeyReplaced(rt.Config)); ok {
		rep.Checks = InsertGlobalCheck(rep.Checks, check)
	}
	// The sandbox rows: the xdg one for every user, the linger and port ones
	// only where the state root carries a dev-sandbox marker.
	rep.Checks = sandboxChecks(env, serveMachineDB(rt), stateRoot, deps.ConfigRoot, rep.Checks)
	// The owner row dials the socket the daemon serves the way the database
	// row reads the file: a missing socket warns with the fix.
	if sock, sockErr := owner.SocketPath(stateRoot); sockErr != nil {
		rep.Checks = InsertGlobalCheck(rep.Checks, ownerCheck(db.OwnerStatus{}, sockErr))
	} else {
		status, probeErr := db.ProbeOwner(sock)
		rep.Checks = InsertGlobalCheck(rep.Checks, ownerCheck(status, probeErr))
	}
	if storeErr != nil {
		rep.Checks = InsertGlobalCheck(rep.Checks, Check{
			Name:        "bindings",
			Severity:    SevWarn,
			Detail:      storeErr.Error(),
			ProbeFailed: true,
		})
	}
}

// appendIdentityRows adds the git-identity row and the no-candidates row.
// The identity row exists only where a server is configured and the cwd is
// inside a repo; a git failure is no row.
func appendIdentityRows(rep *Report, rt relevo.Runtime, hasServers bool) {
	identity := GitIdentityInput{HasServers: hasServers}
	if rt.Git != nil {
		if wd, wdErr := os.Getwd(); wdErr == nil {
			if name, email, identErr := rt.Git.Identity(context.Background(), wd); identErr == nil {
				identity.InRepo, identity.Name, identity.Email = true, name, email
			}
		}
	}
	if c, ok := GitIdentityCheck(identity); ok {
		rep.Checks = InsertGlobalCheck(rep.Checks, c)
	}

	if rt.Candidates.Len() == 0 {
		rep.NoCandidates = true
		rep.Checks = InsertGlobalCheck(rep.Checks, candidatesCheck())
	}
}

// appendServeScopeRows adds the serve rows and the scope rows. The serve
// checks read the same database rt.Store holds; a store whose open failed
// leaves the serve rows off.
func appendServeScopeRows(rep *Report, rt relevo.Runtime, env Env) {
	if stateRoot, err := tmpdir.DefaultRoot(); err == nil {
		serveRoot := filepath.Join(stateRoot, "serve")
		serveDB := serveMachineDB(rt)
		rep.Checks = append(rep.Checks, ServeChecks(env, serveDB, serveRoot, time.Now(), rt.Policy.ServeIsolation(), rt.Policy.ServeIsolationImage(), rt.Policy.ServeIsolationSharedLogins())...)
	}

	// When a scope block asks for cpu pinning, doctor confirms cpuset is
	// delegated (a silently ignored AllowedCPUs shows no exit code anywhere
	// else) and the pool names only this host's cores. A block whose scope
	// is off is skipped.
	var scopeBlocks []ScopeBlock
	if sc := enabledScope(rt.Policy.Scope); sc != nil {
		scopeBlocks = append(scopeBlocks, scopeBlock("scope.allowed_cpus", sc))
	}
	if rt.Policy.Serve != nil {
		if sc := enabledScope(rt.Policy.Serve.Scope); sc != nil {
			scopeBlocks = append(scopeBlocks, scopeBlock("serve.scope.allowed_cpus", sc))
		}
	}
	rep.Checks = append(rep.Checks, ScopeChecks(env, scopeBlocks, UserManagerControllersPath(os.Getuid()), runtime.NumCPU())...)
}

// appendPolicyRows adds the ledger, policy, role-source, skill,
// binding-role, classify and refusal rows, and records the builder refusal
// on the report. A binding whose role is missing from roles.json fails at
// round start, so doctor names it. A store error skips the rows silently.
func appendPolicyRows(rep *Report, rt relevo.Runtime, env Env, L config.Loaded) {
	rep.Checks = append(rep.Checks, ledgerChecks(availability.Gates(relevo.AvailabilityDeps(rt)))...)
	rep.Checks = append(rep.Checks, policyChecks(resolve.PolicyWarningsFor(rt.RoleRegistry(), rt.Candidates, rt.Policy))...)
	rep.Checks = append(rep.Checks, roleSourceChecks(rt.RoleRegistry(), rt.Candidates, rt.Policy)...)
	rep.Checks = append(rep.Checks, skillChecks(env, rt.RoleRegistry(), rt.Candidates)...)
	if bindings, err := rt.Store.List(); err == nil {
		rep.Checks = append(rep.Checks, BindingRoleChecks(bindings, func(r string) bool {
			_, ok := rt.RoleRegistry().Role(r)
			return ok
		})...)
	}
	_, st := classify.Resolve(rt.Policy.Classify, L.Typesafe, os.Getenv)
	rep.Checks = append(rep.Checks, ClassifyCheck(st))
	refusals := resolve.RoleRefusalsFor(rt.RoleRegistry(), rt.Candidates, rt.Policy, availability.Gates(relevo.AvailabilityDeps(rt)))
	rep.Checks = append(rep.Checks, refusalChecks(refusals)...)
	for _, r := range refusals {
		if r.Role == "builder" {
			rep.BuilderRefusal = r.Text
			break
		}
	}
}

// appendSessionRows adds the mastermind, account and login rows. The muse
// plugin rows probe through this Env's Command, the same seam every other
// command-backed row uses.
func appendSessionRows(rep *Report, rt relevo.Runtime, env Env, kinds []string, deps Deps) {
	masterMindIn := mastermindCheckInput(rt, kinds, deps)
	masterMindIn.MuseInspect = env.Command
	rep.Checks = append(rep.Checks, MasterMindChecks(masterMindIn)...)

	// One row per configured login, saying whether its home exists and holds a
	// login, so a round cannot fail to authenticate with no surface naming
	// which account was checked. No accounts means no rows.
	rep.Checks = append(rep.Checks, accountChecks(context.Background(), rt.Accounts, env)...)

	rep.Checks = append(rep.Checks, LoginChecks(env, kinds, WithHomeEnv(accountHomes(rt.Accounts), os.Getenv))...)
}

// appendHookRows adds the hooks, hook-files and agent-files rows. A
// database relevo cannot read leaves the row off.
func appendHookRows(rep *Report, rt relevo.Runtime) {
	d, derr := rt.Store.DB()
	if derr != nil {
		return
	}
	if runs, rerr := hooks.NewKVLog(db.TxKV{DB: d}).Runs(); rerr == nil {
		rep.Checks = append(rep.Checks, HooksCheck(HooksCheckInput{Runs: runs, Now: rt.Now()}))
	}
	// The hook-files row: the scripts this machine's config carries and the
	// ones a person edited on disk and the applier kept.
	rep.Checks = append(rep.Checks, hookFileCheck(relevo.MachineConfig))
	// The agent-files row: the harness files this machine's config carries
	// and the ones a person edited, which neither the applier nor the
	// re-render overwrites.
	rep.Checks = append(rep.Checks, agentFileCheck(relevo.MachineConfig))
}
