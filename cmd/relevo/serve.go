package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/installation"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
)

type hostSlice []string

func (h *hostSlice) String() string {
	return strings.Join(*h, ",")
}

func (h *hostSlice) Set(val string) error {
	*h = append(*h, val)
	return nil
}

type serveFlags struct {
	listen         string
	state          string
	interval       time.Duration
	insecureHTTP   bool
	maxBundleBytes int64
	maxBuilders    int
	publicHosts    hostSlice
}

func installServeFlags(fs *flag.FlagSet) *serveFlags {
	sf := &serveFlags{}
	fs.StringVar(&sf.listen, "listen", ":7777", "listen address")
	fs.StringVar(&sf.state, "state", "", "state directory (defaults to $XDG_STATE_HOME/relevo)")
	fs.DurationVar(&sf.interval, "interval", 2*time.Second, "poll interval")
	fs.BoolVar(&sf.insecureHTTP, "insecure-http", false, "serve plain HTTP without TLS")
	fs.Int64Var(&sf.maxBundleBytes, "max-bundle-bytes", 512<<20, "maximum bundle size in bytes")
	fs.IntVar(&sf.maxBuilders, "max-builders", 0, "headless builders running at once across all owners (0 = policy.json serve.max_builders, else max(1, NumCPU-1))")
	fs.Var(&sf.publicHosts, "public-host", "a host clients reach this server by, for CA or insecure mode; repeatable")
	return sf
}

func serveFlagSet() (*flag.FlagSet, *serveFlags) {
	fs := flag.NewFlagSet("relevo serve", flag.ContinueOnError)
	return fs, installServeFlags(fs)
}

// The registry only walks a flag set, so serveRunFlagSet discards the parsed
// values.
func serveRunFlagSet(fs *flag.FlagSet) { installServeFlags(fs) }

func serveRoot(fs *flag.FlagSet) (string, error) {
	var stateDir string
	if f := fs.Lookup("state"); f != nil {
		stateDir = f.Value.String()
	}
	if stateDir != "" {
		return filepath.Join(stateDir, "serve"), nil
	}
	return defaultServeRoot()
}

func defaultServeRoot() (string, error) {
	root, err := tmpdir.DefaultRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "serve"), nil
}

func pidAlive(pid int) bool {
	// Windows does not support the Unix signal-zero liveness probe, so daemon
	// pointers are ignored there and the default root is used.
	if runtime.GOOS == "windows" {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// openMachineDB opens the machine database every serve verb reads clients,
// TLS, the daemon pointer and config from. The caller closes it. It returns
// the state root too, because the hook run log is keyed on it.
func openMachineDB() (*db.DB, string, error) {
	root, err := tmpdir.DefaultRoot()
	if err != nil {
		return nil, "", err
	}
	d, err := openDB(filepath.Join(root, "relevo.db"))
	if err != nil {
		return nil, "", err
	}
	return d, root, nil
}

// adminRoot resolves the serve root an administrative verb should use, and
// returns the machine database it read the daemon pointer and state markers
// from. The caller closes the database.
func adminRoot(fs *flag.FlagSet) (string, *db.DB, error) {
	var state string
	if f := fs.Lookup("state"); f != nil {
		state = f.Value.String()
	}
	return adminRootFor(state)
}

// adminRootFor is adminRoot for a caller that has already read --state from
// its own flag set. The `show`/`history` --owner routes take parsed values
// rather than a FlagSet, and resolve their root through this so the four
// resolution steps (daemon pointer, default root, stale note, initialised
// check) stay in one place.
func adminRootFor(state string) (string, *db.DB, error) {
	def, err := defaultServeRoot()
	if err != nil {
		return "", nil, err
	}
	d, _, err := openMachineDB()
	if err != nil {
		return "", nil, err
	}
	root, note, err := serve.ResolveAdminRoot(state, d, def, pidAlive)
	if err != nil {
		_ = d.Close()
		return "", nil, err
	}
	if note != "" {
		fmt.Fprintf(os.Stderr, "relevo serve: %s\n", note)
	}
	initialised, err := serve.Initialised(root, d)
	if err != nil {
		_ = d.Close()
		return "", nil, err
	}
	if !initialised {
		_ = d.Close()
		return "", nil, fmt.Errorf("no serve state at %s: run relevo serve init, or pass --state <dir> matching the daemon's", root)
	}
	return root, d, nil
}

func cmdServe(args []string) error {
	const usage = `usage: relevo serve [--listen :7777] [--state <dir>] [--interval 2s] [--insecure-http] [--max-bundle-bytes N] [--max-builders N]
       relevo serve init [--host <name>]... [--state <dir>]
       relevo serve enroll --label <label> --key "<ed25519 line>" [--user <unix user>] [--state <dir>]
       relevo serve clients [--state <dir>]
       relevo serve revoke <id> [--state <dir>]
       relevo serve fingerprint [--state <dir>]
       relevo serve status [--json] [--state <dir>]
       relevo gate --serve [--state <dir>]        (gates, available and unavailable moved to relevo gate)
       relevo serve ui [--state <dir>] [--interval 2s]
       relevo serve gc --abandoned <duration> [--dry-run] [--state <dir>]
       relevo serve unbind --owner <label|id> <name> [--state <dir>] [--force]`

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}

	switch args[0] {
	case "init":
		return cmdServeInit(args[1:])
	case "enroll":
		return cmdServeEnroll(args[1:])
	case "clients":
		return cmdServeClients(args[1:])
	case "revoke":
		return cmdServeRevoke(args[1:])
	case "fingerprint":
		return cmdServeFingerprint(args[1:])
	case "status":
		return cmdServeStatus(args[1:])
	case "log":
		return fail(codeUsage, "relevo serve log was removed; use relevo show <name> --owner <label> --log")
	case "show":
		return fail(codeUsage, "relevo serve show was removed; use relevo show <name> --owner <label>")
	case "tab":
		return fail(codeUsage, "relevo serve tab was removed; use relevo history --tab --owner <label|all>")
	case "gates", "available", "unavailable":
		return fail(codeUsage, "relevo serve %s was removed; use relevo gate --serve …", args[0])
	case "ui":
		return cmdServeUI(args[1:])
	case "gc":
		return cmdServeGC(args[1:])
	case "unbind":
		return cmdServeUnbind(args[1:])
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		if strings.HasPrefix(args[0], "-") {
			return cmdServeRun(args)
		}
		return fail(codeUsage, "relevo serve: unknown command %q", args[0])
	}
}

// serveTierRuntime is the Runtime cmdServeRun uses only to log the builder
// tier at startup: candidates, policy, the server's gates and a clock.
// reg is passed so the logged tier is the tier served rounds get: they resolve
// through the same registry. Gates is the `serve.`-prefixed view of the
// machine database, so a tier check that reads the gate record sees the
// server-wide one.
func serveTierRuntime(candidates *candidate.Set, pol policy.Policy, reg *roles.Registry, root string, d *db.DB) relevo.Runtime {
	var gates db.KV
	if d != nil {
		gates = db.PrefixKV{KV: d.LocalOrSelf(), Prefix: "serve."}
	}
	return relevo.Runtime{
		Candidates:    candidates,
		Policy:        pol,
		Registry:      reg,
		Gates:         gates,
		Now:           time.Now,
		SessionReaper: relevo.NewSessionReaper(binExec{}),
	}
}

// scopeStatusText is the startup line's scopes word for a resolved spec:
// "on (slice relevo.slice, 200%)", "on (200%)", "on (slice relevo.slice)",
// "on", or "off" for nil. A spec carrying a gate quota gains
// ", gate <quota>" inside the parentheses: "on (slice relevo.slice,
// 200%, gate 300%)". A spec carrying an allowed_cpus pool gains
// ", cpus <pool>, one per round": "on (cpus 0-2, one per round)".
func scopeStatusText(sc *spawn.ScopeSpec) string {
	if sc == nil {
		return "off"
	}
	var parts []string
	if sc.Slice != "" {
		parts = append(parts, "slice "+sc.Slice)
	}
	if sc.CPUQuota != "" {
		parts = append(parts, sc.CPUQuota)
	}
	if sc.GateCPUQuota != "" {
		parts = append(parts, "gate "+sc.GateCPUQuota)
	}
	if sc.AllowedCPUs != "" {
		parts = append(parts, "cpus "+sc.AllowedCPUs+", one per round")
	}
	if len(parts) == 0 {
		return "on"
	}
	return "on (" + strings.Join(parts, ", ") + ")"
}

func serveAdminConfig(root string, d *db.DB) serve.Config {
	return serve.Config{
		Root:   root,
		DB:     d,
		Runner: proc.New(),
		Now:    time.Now,
	}
}

// loadConfig imports any config file present into the open machine database
// and returns the loaded config: the set every candidate-aware command needs.
func loadConfig(d *db.DB) (config.Loaded, error) {
	cs := config.Open(d)

	configDir, err := userConfigRoot()
	if err != nil {
		return config.Loaded{}, err
	}
	if !d.Newer() {
		dir := filepath.Join(configDir, "relevo")
		if _, err := cs.As("import", "imported "+dir).ImportFiles(dir, time.Now().UTC()); err != nil {
			return config.Loaded{}, err
		}
		// Write a name for every stored candidate. Names are derived in
		// memory by candidate.Parse either way, so a failure here is a
		// warning, never a refusal to serve.
		if _, err := cs.EnsureCandidateNames(); err != nil {
			slog.Warn("candidate names not written to config", "err", err)
		}
		// A pre-actors config becomes actors plus agents. An error is a
		// warning; the old config keeps working.
		if migrated, err := cs.MigrateToActors(); err != nil {
			slog.Warn("config: roles not migrated to actors", "err", err)
		} else if migrated {
			slog.Info("config: roles migrated to actors (relevo config log)")
		}
	} else {
		slog.Warn("relevo.db schema is newer; config import skipped")
	}
	return cs.Load()
}

// loadServeConfig opens the machine database exactly as newRuntime does
// (tmpdir.DefaultRoot(), not --state), imports any config file present, and
// returns the loaded config, the handle it read from and the state root; the
// caller closes the handle. It is the one copy of that loading: cmdServeRun
// starts a daemon with it. An admin verb that already holds the machine
// database uses loadConfig directly.
func loadServeConfig() (config.Loaded, *db.DB, string, error) {
	d, root, err := openMachineDB()
	if err != nil {
		return config.Loaded{}, nil, "", err
	}
	L, err := loadConfig(d)
	if err != nil {
		_ = d.Close()
		return config.Loaded{}, nil, "", err
	}
	return L, d, root, nil
}

// serveAdminConfigFrom is serveAdminConfig plus the loaded policy and roles
// registry, which the census needs: `serve status` reports the builder cap the
// server enforces, and that cap comes from serve.max_builders. It takes an
// already-loaded config so a caller that also needs the candidates loads once:
// loading is not a pure read.
func serveAdminConfigFrom(root string, d *db.DB, L config.Loaded) serve.Config {
	cfg := serveAdminConfig(root, d)
	cfg.Policy = L.Policy
	cfg.Registry = L.Registry
	cfg.Accounts = L.Accounts
	return cfg
}

// Unlike serveAdminConfigWithCandidates, an empty candidates set is not an
// error: the census is meaningful with no candidates.
func serveAdminConfigWithPolicy(root string, d *db.DB) (serve.Config, error) {
	L, err := loadConfig(d)
	if err != nil {
		return serve.Config{}, err
	}
	return withIsolation(serveAdminConfigFrom(root, d, L), L.Policy.ServeIsolation()), nil
}

// serveAdminConfigWithCandidates is serveAdminConfigFrom plus the configured
// candidates, which the gate verbs need: `relevo serve gates` projects the
// ledger onto the candidate set, and the two edit verbs refuse a token no
// candidate names.
//
// Unlike cmdServeRun, an empty candidates set is an error here: with no
// candidate set to project onto, a ledger full of gates would render as
// "no gates", which reads as "nothing is gated".
func serveAdminConfigWithCandidates(root string, d *db.DB) (serve.Config, error) {
	L, err := loadConfig(d)
	if err != nil {
		return serve.Config{}, err
	}
	if L.Candidates.Len() == 0 {
		return serve.Config{}, errors.New("no candidates configured")
	}
	cfg := serveAdminConfigFrom(root, d, L)
	cfg.Candidates = L.Candidates
	return cfg, nil
}

func cmdServeRun(args []string) error {
	fs, sf := serveFlagSet()
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	s, err := openServeConfig(fs)
	if err != nil {
		return err
	}
	defer func() { _ = s.d.Close() }()
	root, L, d := s.root, s.L, s.d
	candidates, pol, reg := L.Candidates, L.Policy, L.Registry

	// The server's own installation: its rows carry its id as their origin,
	// and WhoAmI advertises it so a client can link its row to this server's
	// copy.
	inst, err := installation.Load(s.stateRoot)
	if err != nil {
		return err
	}

	roles := serveBuilderProbes(candidates, pol, reg, root, d)

	reader, prices := newUsageReader(L.Prices)

	dispatcher, err := buildServeDispatcher(L.Hooks, d, pol)
	if err != nil {
		return err
	}

	// Scopes are enabled by policy (default on) and confirmed by a startup
	// probe; if systemd-run is missing or the user manager refuses, scopes
	// are off for the daemon's lifetime with one log line.
	scope, scopesStatus := resolveServeScope(pol, s.userMode, s.containerMode)

	audiences, err := serve.Audiences(serve.SecretStore{DB: d}, sf.publicHosts)
	if err != nil {
		return err
	}

	cfg := newServeServerConfig(s, sf, inst, reader, prices, roles, dispatcher, scope, audiences)

	srv, err := serve.New(cfg)
	if err != nil {
		return err
	}

	logServeStartup(pol, cfg.MaxBuilders, scopesStatus, s.isoMode, audiences)

	if err := writeServeDaemonPointer(d, root, sf.listen); err != nil {
		return err
	}
	defer func() { _ = db.RemoveDaemonPointer(d) }()

	cert, fp, err := loadServeTLS(d, sf.insecureHTTP)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	announceServeAddr(srv, sf.insecureHTTP, fp)

	return srv.ListenAndServe(ctx, serve.ListenConfig{
		Addr:         sf.listen,
		TLS:          cert,
		InsecureHTTP: sf.insecureHTTP,
	})
}
