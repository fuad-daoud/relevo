package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
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

// serveRunLoaded carries cmdServeRun's loaded state: the serve root, the
// machine state root, the imported config, the open machine database and the
// tick's reload source.
type serveRunLoaded struct {
	root      string
	stateRoot string
	loaded    config.Loaded
	d         *db.DB
	reloader  *serve.ConfigRefresher
}

// loadServeRunState opens the serve root and the machine database, imports
// any config file present, and returns the tick's reload source over them.
func loadServeRunState(fs *flag.FlagSet) (serveRunLoaded, error) {
	var st serveRunLoaded
	root, err := serveRoot(fs)
	if err != nil {
		return st, err
	}
	L, d, stateRoot, err := loadServeConfig()
	if err != nil {
		return st, err
	}
	reloader, err := serveConfigReloader(d, "")
	if err != nil {
		_ = d.Close()
		return st, err
	}
	st.root, st.stateRoot, st.loaded, st.d, st.reloader = root, stateRoot, L, d, reloader
	return st, nil
}

// resolveServeRunIsolation resolves the runner and, in user mode with no
// --state, moves the server to the one fixed root-owned place the daemon and
// its tenants agree on.
func resolveServeRunIsolation(fs *flag.FlagSet, raw string, euid int, stateRoot, root string) (spawn.Runner, isolate.Mode, string, error) {
	runner, isoMode, err := resolveIsolation(raw, euid, stateRoot)
	if err != nil {
		return nil, "", root, fail(codeNotAvailable, "%v", err)
	}
	if isoMode == isolate.ModeUser && fs.Lookup("state").Value.String() == "" {
		root = serve.UserModeRoot
	}
	return runner, isoMode, root, nil
}

// serveCandidateRoles logs each candidate harness's role-file coverage once
// at startup and returns the checker the server runs on: a kind missing role
// files is gated on every pick, and this line is the only notice.
func serveCandidateRoles(candidates *candidate.Set, reg *roles.Registry) harness.RoleChecker {
	roles := harness.OSRoleChecker()
	seenKinds := map[string]bool{}
	for _, ref := range candidates.Refs() {
		r, err := candidate.ParseRef(ref)
		if err != nil || seenKinds[r.Harness] {
			continue
		}
		seenKinds[r.Harness] = true
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
	return roles
}

// resolveServeRunScope enables scopes by policy and confirms them with a
// startup probe; when systemd-run is missing or the user manager refuses,
// scopes stay off for the daemon's lifetime.
func resolveServeRunScope(userMode, containerMode bool, pol policy.Policy) (*spawn.ScopeSpec, string) {
	if userMode {
		return nil, "off (isolation=user)"
	}
	if containerMode {
		return nil, "off (isolation=container)"
	}
	scope := scopeFromPolicy(pol.ScopeFor(true))
	if scope == nil {
		return nil, "off"
	}
	if err := proc.ProbeScopes(context.Background(), scope.Slice); err != nil {
		slog.Warn("scopes unavailable; builders will run in the daemon's cgroup", "err", err)
		return nil, "unavailable"
	}
	return scope, scopeStatusText(scope)
}

// serveRunWiring carries cmdServeRun's wired state into the server config.
type serveRunWiring struct {
	root       string
	d          *db.DB
	loaded     config.Loaded
	runner     spawn.Runner
	reloader   *serve.ConfigRefresher
	dispatcher hooks.Dispatcher
	scope      *spawn.ScopeSpec
	isoMode    isolate.Mode
	sf         *serveFlags
	audiences  []string
	inst       installation.Installation
	reader     usagepkg.Reader
	prices     usagepkg.Prices
	roles      harness.RoleChecker
}

// buildServeConfig wires the loaded state into the server config, keeping
// assembly order.
func buildServeConfig(w serveRunWiring) serve.Config {
	L := w.loaded
	return serve.Config{
		Root:           w.root,
		DB:             w.d,
		Candidates:     L.Candidates,
		Accounts:       L.Accounts,
		Policy:         L.Policy,
		Runner:         w.runner,
		Git:            git.NewClient("git", 0, 0),
		Now:            time.Now,
		Interval:       w.sf.interval,
		MaxBundleBytes: w.sf.maxBundleBytes,
		Usage:          w.reader,
		Prices:         w.prices,
		StartedAt:      time.Now(),
		Roles:          w.roles,
		Registry:       L.Registry,
		MaxBuilders:    w.sf.maxBuilders,
		Reloader:       w.reloader,
		Hooks:          w.dispatcher,
		Scope:          w.scope,
		Isolation:      w.isoMode,
		IsolationImage: L.Policy.ServeIsolationImage(),
		SessionReaper:  relevo.NewSessionReaper(binExec{}),
		LookupUser:     lookupUnixUser,
		SharedLogins:   L.Policy.ServeIsolationSharedLogins(),
		ReaperFor:      userReaperFor(L.Policy.ServeIsolationSharedLogins()),
		Installation:   w.inst,
		Audiences:      w.audiences,
	}
}

// recordServeDaemonPointer records the daemon pointer for the absolute serve
// root. A write failure warns, never failing startup.
func recordServeDaemonPointer(d *db.DB, root, listen string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if err := db.WriteDaemonPointer(d, db.DaemonPointer{
		Root:      root,
		PID:       os.Getpid(),
		Listen:    listen,
		StartedAt: time.Now(),
	}); err != nil {
		slog.Warn("daemon pointer not written", "err", err)
	}
	return nil
}

// logServeBuilderTier logs the tier served rounds get: the harness tier warns,
// anything else informs.
func logServeBuilderTier(candidates *candidate.Set, pol policy.Policy, reg *roles.Registry, root string, d *db.DB) {
	builderTierRT := serveTierRuntime(candidates, pol, reg, root, d)
	if builderTier := relevo.ServedBuilderTier(builderTierRT); builderTier == harness.TierHarness {
		slog.Warn(relevo.ServerTierWarning(relevo.ServerProbe{TierAware: true, BuilderTier: string(builderTier)}))
	} else {
		slog.Info("builder tier", "tier", builderTier)
	}
}

// serveHooksDispatcher runs the hooks dispatcher on the machine database's
// run log, the same kv the daemon writes, so a served round's hook runs are
// visible beside the local ones.
func serveHooksDispatcher(L config.Loaded, pol policy.Policy, d *db.DB) (hooks.Dispatcher, error) {
	hooksCfg, err := resolveHooksConfig(L.Hooks, hooks.NewKVLog(db.TxKV{DB: d}))
	if err != nil {
		return nil, err
	}
	return newHooksDispatcher(hooksCfg, pol), nil
}

// serveListenerTLS loads the TLS identity unless the server runs plain HTTP.
func serveListenerTLS(d *db.DB, insecureHTTP bool) (*tls.Certificate, string, error) {
	if insecureHTTP {
		return nil, "", nil
	}
	c, err := serve.LoadTLS(serve.SecretStore{DB: d})
	if err != nil {
		return nil, "", err
	}
	return &c, client.FingerprintOf(c.Leaf.Raw), nil
}

// serveBuilderCap resolves the builder cap: the flag when positive, else the
// policy default.
func serveBuilderCap(maxBuilders int, pol policy.Policy) int {
	if maxBuilders <= 0 {
		return pol.MaxBuildersOrDefault()
	}
	return maxBuilders
}

// serveUntilSignal runs the server until interrupted, announcing the listen
// address once the server has one.
func serveUntilSignal(srv *serve.Server, sf *serveFlags, cert *tls.Certificate, fp string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	announceServe(srv, sf.insecureHTTP, fp)
	return srv.ListenAndServe(ctx, serve.ListenConfig{
		Addr:         sf.listen,
		TLS:          cert,
		InsecureHTTP: sf.insecureHTTP,
	})
}

// announceServe prints the listen address once the server has one: plain
// HTTP warns, TLS names its fingerprint.
func announceServe(srv *serve.Server, insecureHTTP bool, fp string) {
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
