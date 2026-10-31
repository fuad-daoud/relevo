package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/installation"
	"github.com/fuad-daoud/relevo/internal/isolate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/spawn"
	usagepkg "github.com/fuad-daoud/relevo/internal/usage"
)

// logServeBuilderTier logs the builder tier the serve runtime resolved: a
// harness tier warns, anything else is one info line.
func logServeBuilderTier(builderTierRT relevo.Runtime) {
	if builderTier := relevo.ServedBuilderTier(builderTierRT); builderTier == harness.TierHarness {
		slog.Warn(relevo.ServerTierWarning(relevo.ServerProbe{TierAware: true, BuilderTier: string(builderTier)}))
	} else {
		slog.Info("builder tier", "tier", builderTier)
	}
}

// logRoleCoverage logs each candidate harness's role-file coverage once at
// startup: a candidate whose kind is missing role files is gated on every
// pick.
func logRoleCoverage(candidates *candidate.Set, reg *roles.Registry, roles harness.RoleChecker) {
	seenKinds := map[string]bool{}
	for _, ref := range candidates.Refs() {
		r, err := candidate.ParseRef(ref)
		if err != nil || seenKinds[r.Harness] {
			continue
		}
		seenKinds[r.Harness] = true
		// The gate checks the builder's resolved definitions: in roles.json
		// mode a custom name may be what the row launches.
		spec, err := reg.Spec("builder", r.Harness)
		if err != nil {
			continue
		}
		if missing := roles.Missing(r.Harness, spec.Definitions); len(missing) > 0 {
			slog.Warn("candidate agent definitions missing; those candidates will be skipped", "harness", r.Harness, "missing", missing, "fix", "relevo config agents --kind "+r.Harness)
		} else {
			slog.Info("agent definitions present", "harness", r.Harness)
		}
	}
}

// resolveServeScope resolves the scope served rounds launch under: user mode
// runs scopes off, container mode carries bounds on the podman command, and
// otherwise a startup probe confirms systemd scopes. The cap applies in every
// mode.
func resolveServeScope(pol policy.Policy, userMode, containerMode bool) (*spawn.ScopeSpec, string) {
	scope := scopeFromPolicy(pol.ScopeFor(true))
	if userMode {
		// User mode runs scopes off: systemd-run --user would target root's
		// own user manager, not the tenant's. The cap still applies.
		return nil, "off (isolation=user)"
	}
	if containerMode {
		// Container mode runs no systemd scope: bounds travel on the podman
		// command, and the scope here is only the carrier of those bounds and
		// the round's container name. No probe is taken. The cap still applies.
		return nil, "off (isolation=container)"
	}
	if scope == nil {
		return nil, "off"
	}
	if err := proc.ProbeScopes(context.Background(), scope.Slice); err != nil {
		slog.Warn("scopes unavailable; builders will run in the daemon's cgroup", "err", err)
		return nil, "unavailable"
	}
	return scope, scopeStatusText(scope)
}

// loadServeTLS loads the server's certificate unless --insecure-http skips
// it, returning the fingerprint the listening line prints.
func loadServeTLS(d *db.DB, insecureHTTP bool) (*tls.Certificate, string, error) {
	var cert *tls.Certificate
	var fp string
	if !insecureHTTP {
		c, err := serve.LoadTLS(serve.SecretStore{DB: d})
		if err != nil {
			return nil, "", err
		}
		cert = &c
		fp = client.FingerprintOf(c.Leaf.Raw)
	}
	return cert, fp, nil
}

// serveLoadedConfig bundles what cmdServeRun loads before building the
// server: the state root, the loaded config, its database, the tick's reload
// source and the isolation decision.
type serveLoadedConfig struct {
	root          string
	L             config.Loaded
	d             *db.DB
	stateRoot     string
	reloader      *serve.ConfigRefresher
	runner        spawn.Runner
	isoMode       isolate.Mode
	userMode      bool
	containerMode bool
}

// openServeConfig resolves the state root and loads the serve config: the
// database, the tick's reload source and the isolation decision. A load that
// fails after the database opened closes it again, the way the caller's
// deferred close would have. A user-mode server keeps its state in one fixed
// root-owned place when --state is unset, so the daemon and its tenants agree
// on it regardless of whose HOME the daemon was started with.
func openServeConfig(fs *flag.FlagSet) (serveLoadedConfig, error) {
	var s serveLoadedConfig
	root, err := serveRoot(fs)
	if err != nil {
		return s, err
	}
	L, d, stateRoot, err := loadServeConfig()
	if err != nil {
		return s, err
	}
	// The tick's reload source, over the database and dir just loaded.
	reloader, err := serveConfigReloader(d, "")
	if err != nil {
		_ = d.Close()
		return s, err
	}
	runner, isoMode, err := resolveIsolation(L.Policy.ServeIsolation(), os.Geteuid(), stateRoot)
	if err != nil {
		_ = d.Close()
		return s, fail(codeNotAvailable, "%v", err)
	}
	userMode := isoMode == isolate.ModeUser
	containerMode := isoMode == isolate.ModeContainer
	if userMode && fs.Lookup("state").Value.String() == "" {
		root = serve.UserModeRoot
	}
	s = serveLoadedConfig{
		root: root, L: L, d: d, stateRoot: stateRoot, reloader: reloader,
		runner: runner, isoMode: isoMode,
		userMode: userMode, containerMode: containerMode,
	}
	return s, nil
}

// serveBuilderProbes logs the builder tier and each candidate harness's
// role-file coverage once at startup, returning the checker the server
// config carries.
func serveBuilderProbes(candidates *candidate.Set, pol policy.Policy, reg *roles.Registry, root string, d *db.DB) harness.RoleChecker {
	builderTierRT := serveTierRuntime(candidates, pol, reg, root, d)
	logServeBuilderTier(builderTierRT)
	roles := harness.OSRoleChecker()
	logRoleCoverage(candidates, reg, roles)
	return roles
}

// buildServeDispatcher wires the hooks dispatcher on the machine database's
// run log, the same kv `hooks.log` the daemon writes, so a served round's
// hook runs are visible beside the local ones.
func buildServeDispatcher(hooksMap map[string][][]string, d *db.DB, pol policy.Policy) (hooks.Dispatcher, error) {
	hooksCfg, err := resolveHooksConfig(hooksMap, hooks.NewKVLog(db.TxKV{DB: d}))
	if err != nil {
		return nil, err
	}
	return newHooksDispatcher(hooksCfg, pol), nil
}

// newServeServerConfig builds the server's config from the loaded state and
// the wired pieces: candidates and policy, the runner and scope, the hooks
// dispatcher and the reload source.
func newServeServerConfig(s serveLoadedConfig, sf *serveFlags, inst installation.Installation, reader usagepkg.Reader, prices usagepkg.Prices, roles harness.RoleChecker, dispatcher hooks.Dispatcher, scope *spawn.ScopeSpec, audiences []string) serve.Config {
	candidates, pol, reg := s.L.Candidates, s.L.Policy, s.L.Registry
	return serve.Config{
		Root:           s.root,
		DB:             s.d,
		Candidates:     candidates,
		Accounts:       s.L.Accounts,
		Policy:         pol,
		Runner:         s.runner,
		Git:            git.NewClient("git", 0, 0),
		Now:            time.Now,
		Interval:       sf.interval,
		MaxBundleBytes: sf.maxBundleBytes,
		Usage:          reader,
		Prices:         prices,
		StartedAt:      time.Now(),
		Roles:          roles,
		Registry:       reg,
		MaxBuilders:    sf.maxBuilders,
		Reloader:       s.reloader,
		Hooks:          dispatcher,
		Scope:          scope,
		Isolation:      s.isoMode,
		IsolationImage: s.L.Policy.ServeIsolationImage(),
		SessionReaper:  relevo.NewSessionReaper(binExec{}),
		LookupUser:     lookupUnixUser,
		SharedLogins:   s.L.Policy.ServeIsolationSharedLogins(),
		ReaperFor:      userReaperFor(s.L.Policy.ServeIsolationSharedLogins()),
		Installation:   inst,
		Audiences:      audiences,
	}
}

// logServeStartup logs the startup lines: the builder cap with the scopes
// and isolation modes, and the accepted audiences.
func logServeStartup(pol policy.Policy, maxBuilders int, scopesStatus string, isoMode isolate.Mode, audiences []string) {
	if maxBuilders <= 0 {
		maxBuilders = pol.MaxBuildersOrDefault()
	}
	slog.Info(fmt.Sprintf("builders cap=%d scopes=%s isolation=%s", maxBuilders, scopesStatus, isoMode))
	slog.Info("accepted audiences", "audiences", strings.Join(audiences, ", "))
}

// writeServeDaemonPointer records the daemon pointer for the absolute root,
// warning when it cannot: the pointer is advisory, never fatal.
func writeServeDaemonPointer(d *db.DB, root, listen string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if err := db.WriteDaemonPointer(d, db.DaemonPointer{
		Root:      absRoot,
		PID:       os.Getpid(),
		Listen:    listen,
		StartedAt: time.Now(),
	}); err != nil {
		slog.Warn("daemon pointer not written", "err", err)
	}
	return nil
}

// announceServeAddr prints the listening line once the server binds: the
// address, marked INSECURE for plain http or with the TLS fingerprint.
func announceServeAddr(srv *serve.Server, insecureHTTP bool, fp string) {
	go func() {
		for i := 0; i < 100; i++ {
			if addr := srv.Addr(); addr != nil {
				if insecureHTTP {
					fmt.Printf("relevo serve listening on %s (INSECURE http)\n", addr)
				} else {
					fmt.Printf("relevo serve listening on %s (tls %s)\n", addr, fp)
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
}
