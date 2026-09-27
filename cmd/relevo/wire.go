package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/classify"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// resolveHooksConfig builds the hook dispatcher environment: the hooks section
// the config store loaded, and the run log its runs are recorded in (#4.4).
// log is the machine database's kv log; a caller with no database open
// (preflight, check, serve's admin paths) passes nil, and hook output is then
// recorded nowhere.
func resolveHooksConfig(hooksMap map[string][][]string, log hooks.RunLog) (hooks.Config, error) {
	return hooks.Config{
		Hooks: hooksMap,
		Log:   log,
	}, nil
}

// hooksRunLog is the machine database's hook run log: the kv row every hook
// run and webhook failure is recorded in (P3b round 2 §4.4). A store whose
// database cannot be opened gets a nil log, which records nothing.
func hooksRunLog(st *store.Store) hooks.RunLog {
	d, err := st.DB()
	if err != nil {
		return nil
	}
	return hooks.NewKVLog(db.TxKV{DB: d}, filepath.Dir(st.DBPath()))
}

// newHooksDispatcher wires the local hooks.d script dispatcher, and, when
// config policy's notify.webhooks is non-empty, a WebhookSink beside it (#4):
// both receive every event, fanned out by a MultiDispatcher.
func newHooksDispatcher(hooksCfg hooks.Config, pol policy.Policy) hooks.Dispatcher {
	sinks := []hooks.Dispatcher{hooks.NewLocalDispatcher(hooksCfg, hooks.NewOSExecutor(hooksCfg.Log))}

	if pol.Notify != nil && len(pol.Notify.Webhooks) > 0 {
		sinks = append(sinks, &hooks.WebhookSink{
			Hooks: pol.Notify.Webhooks,
			Runs:  hooksCfg.Log,
		})
	}

	return hooks.MultiDispatcher(sinks)
}

// opencodeServiceFiles returns candidate service.json paths in preference
// order: the state dir first ($XDG_STATE_HOME/opencode/service.json or
// ~/.local/state/opencode/service.json), then the config dir
// ($XDG_CONFIG_HOME/opencode/service.json or ~/.config/opencode/service.json).
func opencodeServiceFiles() []string {
	home, _ := os.UserHomeDir()
	stateFile := filepath.Join(home, ".local", "state", "opencode", "service.json")
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		stateFile = filepath.Join(xdg, "opencode", "service.json")
	}
	configFile := filepath.Join(home, ".config", "opencode", "service.json")
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		configFile = filepath.Join(xdg, "opencode", "service.json")
	}
	return []string{stateFile, configFile}
}

// opencodeDBPath resolves $XDG_DATA_HOME/opencode/opencode.db, falling back
// to ~/.local/share/opencode/opencode.db.
func opencodeDBPath() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode", "opencode.db")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db")
}

// captureAgyEnv persists the calling agy session's agentapi credentials, when
// the environment carries them (#349). It runs for every verb rather than a
// chosen list, and it costs nothing when ANTIGRAVITY_* is absent: the machine
// database is opened only when there is something to capture, because the
// capture itself short-circuits on the same check (P3b round 2 §4.3).
//
// Both failures are deliberately silent: capture is best-effort and must never
// change a command's exit code or output, so a root relevo cannot resolve, a
// database it cannot open, and an environment with nothing to capture all
// end the same way -- nothing written, nothing printed.
func captureAgyEnv() {
	if !delivery.AgyEnvPresent(os.Getenv) {
		return
	}
	root, err := store.DefaultRoot()
	if err != nil {
		return
	}
	st := store.New(root)
	d, err := st.DB()
	if err != nil {
		return
	}
	secrets := db.SecretStore{DB: d}
	// The credentials were files under planners/.agy before this round: a file
	// that is present is imported, then removed (§4.3).
	_ = delivery.ImportAgyCreds(secrets, st.AgyCredsDir())
	_, _ = delivery.CaptureAgyCreds(os.Getenv, secrets, time.Now().UTC())
}

// newDeliverers builds Runtime.Deliverers: the agy deliverer always, and an
// OpencodeDeliverer keyed by "opencode" when sqlite3 is on PATH
// (docs/specs/2026-09-22-opencode-delivery-design.md). No sqlite3 means the
// opencode deliverer could never confirm a delivery, so an opencode planner's
// reports stay pending for the background wait. agy needs no external tool: it reads
// the captured credential secret from the machine database and runs agy
// itself, and reports its own failure as OutcomeUnavailable.
func newDeliverers() map[string]delivery.PlannerDeliverer {
	deliverers := map[string]delivery.PlannerDeliverer{}
	// store.DefaultRoot has already succeeded once in newRuntime; the guard is
	// only for the shape of the function, and a root relevo cannot resolve means
	// every verb has failed long before a delivery is attempted.
	if root, err := store.DefaultRoot(); err == nil {
		// DBIfExists, not DB: a root with no relevo.db yet -- `daemon
		// --preflight` and `--check` run against one -- must not get a
		// database conjured into it just because a deliverer was wired.
		if d, derr := store.New(root).DBIfExists(); derr == nil && d != nil {
			deliverers["agy"] = &delivery.AgyDeliverer{
				Exec:  binEnvExec{},
				Creds: db.SecretStore{DB: d},
			}
		}
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return deliverers
	}
	deliverers["opencode"] = &delivery.OpencodeDeliverer{
		Exec:       binExec{},
		StateFiles: opencodeServiceFiles(),
		DBPath:     opencodeDBPath(),
	}
	return deliverers
}

// newRuntime constructs the production runtime. Config and secrets come from
// the machine database; any file present under <userConfigRoot>/relevo is
// imported into it first and removed (#4.6). aliases.json is never read (#80).
func newRuntime() (relevo.Runtime, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return relevo.Runtime{}, err
	}

	d, err := openDB(filepath.Join(root, "relevo.db"))
	if err != nil {
		return relevo.Runtime{}, err
	}
	cs := config.Open(d)

	configDir, err := userConfigRoot()
	if err != nil {
		return relevo.Runtime{}, err
	}
	if !d.Newer() {
		dir := filepath.Join(configDir, "relevo")
		if _, err := cs.As("import", "imported "+dir).ImportFiles(dir, time.Now().UTC()); err != nil {
			return relevo.Runtime{}, err
		}
	} else {
		// A schema a newer relevo wrote is never written by this binary: read
		// what is there and skip the import (#4.6).
		slog.Warn("relevo.db schema is newer; config import skipped")
	}

	if !d.Newer() {
		// A1's migration: write a name for every stored candidate. Names are
		// derived in memory by candidate.Parse either way, so a failure here
		// is a warning, never a refusal to start.
		if _, err := cs.EnsureCandidateNames(); err != nil {
			slog.Warn("candidate names not written to config", "err", err)
		}
		// A2 round 2's one-time migration: a pre-actors config becomes actors
		// plus agents, as a "migration" revision. An error is a warning too,
		// and the old config keeps working.
		if migrated, err := cs.MigrateToActors(); err != nil {
			slog.Warn("config: roles not migrated to actors", "err", err)
		} else if migrated {
			slog.Info("config: roles migrated to actors (relevo config log)")
		}
	}

	L, err := cs.Load()
	if err != nil {
		return relevo.Runtime{}, err
	}

	rt, err := buildRuntime(root, L, true)
	if err != nil {
		return relevo.Runtime{}, err
	}
	rt.Config = cs
	return rt, nil
}

// openDB ensures path's directory exists (Open's precondition) and opens
// it, migrating as needed. It was `relevo db`'s helper and stays here for
// every other verb that opens the machine database directly (P3d D3).
func openDB(path string) (*db.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	return db.Open(path)
}

// newRuntimePeek constructs the runtime `relevo daemon --preflight` and
// `--check` need. It never creates, migrates or writes anything: it reads the
// config files when any is present, else the database read-only when one
// exists, else nothing at all (#4.6).
func newRuntimePeek() (relevo.Runtime, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return relevo.Runtime{}, err
	}
	configDir, err := userConfigRoot()
	if err != nil {
		return relevo.Runtime{}, err
	}
	dir := filepath.Join(configDir, "relevo")

	var L config.Loaded
	switch {
	case configFilesPresent(dir):
		L, err = config.LoadFiles(dir)
	case fileExists(filepath.Join(root, "relevo.db")):
		var d *db.DB
		d, err = db.OpenReadOnly(filepath.Join(root, "relevo.db"))
		if err == nil {
			L, err = config.Open(d).Load()
			_ = d.Close()
		}
	default:
		L, err = config.LoadFiles(dir)
	}
	if err != nil {
		return relevo.Runtime{}, err
	}

	// buildRuntime with openGates false: preflight and check open no database
	// and carry nil Gates/Latency, so they touch no gate record (P3b plan §4.5).
	return buildRuntime(root, L, false)
}

// configFilesPresent reports whether any file the import consumes, or a hooks
// directory, is present in dir.
func configFilesPresent(dir string) bool {
	for _, name := range config.Files() {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "hooks")); err == nil && info.IsDir() {
		return true
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// buildRuntime wires the Runtime from one loaded config, the shape both
// newRuntime and newRuntimePeek use. openGates opens the store root's database
// for the gate, availability and latency records (P3b plan §4.5): newRuntime
// passes true, and a DB() error is fatal for the verb; newRuntimePeek passes
// false, so preflight and check leave the root with no database and nil gates.
func buildRuntime(root string, L config.Loaded, openGates bool) (relevo.Runtime, error) {
	pol := L.Policy

	// Warnings are carried, never printed: every CLI command calls newRuntime,
	// so printing here would be noise (#372 §4.4). `relevo doctor` renders them
	// and the daemon logs each once.
	configWarnings := L.Warnings

	cls, _ := classify.Resolve(pol.Classify, L.Typesafe, os.Getenv)

	reader, prices := newUsageReader(L.Prices)
	home, _ := os.UserHomeDir()

	st := store.New(root)
	gitClient := git.NewClient("git", 10*time.Second, git.DefaultMaxPatchBytes)

	// Gates, availability and latency live in the store root's database; the
	// legacy directory holding ledger.json/availability.json/history.json is
	// the store root too, so LoadKV imports them on first read (P3b plan §4.5).
	// The channel claims, the planner registry and the hooks run log live in
	// the same database (P3b round 2 §4.1-§4.4), so `relevo daemon --preflight`
	// and `--check`, which pass openGates false, open no database at all.
	var (
		gates    db.KV
		gatesDir string
		claims   delivery.ClaimStore
		runLog   hooks.RunLog
	)
	if openGates {
		d, err := st.DB()
		if err != nil {
			return relevo.Runtime{}, err
		}
		gates = d
		gatesDir = root
		claims = &delivery.KVClaims{KV: db.TxKV{DB: d}, Root: st.ChannelsDir()}
		runLog = hooksRunLog(st)
	}

	hooksCfg, err := resolveHooksConfig(L.Hooks, runLog)
	if err != nil {
		return relevo.Runtime{}, err
	}
	dispatcher := newHooksDispatcher(hooksCfg, pol)

	remoteClient, err := newRemoteClient(L.Servers, L.ClientKey)
	if err != nil {
		return relevo.Runtime{}, err
	}

	var opencodeSession func(cwd string, now time.Time) (string, error)
	if _, err := exec.LookPath("sqlite3"); err == nil {
		opencodeSession = delivery.OpencodeSessionFinder{
			Exec:   binExec{},
			DBPath: opencodeDBPath(),
		}.Find
	}

	rt := relevo.Runtime{
		Git:            gitClient,
		Runner:         proc.New(),
		Store:          st,
		Candidates:     L.Candidates,
		Gates:          gates,
		GatesDir:       gatesDir,
		Latency:        gates,
		Policy:         pol,
		Registry:       L.Registry,
		ConfigWarnings: configWarnings,
		Scope:          scopeFromPolicy(pol.ScopeFor(false)),
		Classify:       cls,
		Usage:          reader,
		Sessions:       relevo.HomeSessionLocator(home),
		Prices:         prices,
		Fetcher:        release.NewHTTPFetcher(release.Source(), 5*time.Second),
		Now:            time.Now,
		Hooks:          dispatcher,
		Remote:         remoteClient,
		// The transport depends only on the git client, not the servers
		// section, so a server added while the daemon runs needs no rebuild.
		Transport:       remote.NewBundleTransport(gitClient, ""),
		Roles:           harness.OSRoleChecker(),
		Channels:        claims,
		ProcStart:       procStartUnix,
		OpencodeSession: opencodeSession,
		Deliverers:      newDeliverers(),
		SessionReaper:   relevo.NewSessionReaper(binExec{}),
	}
	// The registry needs the runtime's own store and clock, so it is wired
	// here rather than in the literal above. A runtime with no database open
	// (preflight, check) keeps a nil registry, which every caller already
	// treats as "no planners".
	if openGates {
		reg, rerr := plannerRegistry(rt)
		if rerr != nil {
			return relevo.Runtime{}, rerr
		}
		rt.Planners = reg
	}
	return rt, nil
}

// procStartUnix reads a process's start time in Unix seconds, the pid-reuse
// defence planner.Resolve's host step and relevo mcp's claim need. A read
// failure is returned, not swallowed: Resolve treats the error as "the host
// step cannot run" and falls through to the session.
func procStartUnix(pid int) (int64, error) {
	started, err := proc.StartTime(context.Background(), pid)
	if err != nil {
		return 0, err
	}
	return started.Unix(), nil
}

// newRemoteClient wires Runtime.Remote through the one rule that turns a
// servers section and a stored key into a client: nil when the section is
// absent or empty. Servers without a client key are not fatal -- every remote
// path already reports ErrRemoteUnavailable on a nil Runtime.Remote -- but the
// fix prints once, since a configured server this client cannot reach is a
// setup mistake worth naming immediately rather than only when a remote
// command is next run. The daemon's config reload builds its client through
// the same rule.
func newRemoteClient(servers remote.Servers, key []byte) (relevo.RemoteClient, error) {
	remoteClient, err := relevo.NewRemoteClient(servers, key)
	if errors.Is(err, relevo.ErrNoClientKey) {
		fmt.Fprintln(os.Stderr, "relevo: "+err.Error())
		return nil, nil
	}
	return remoteClient, err
}
