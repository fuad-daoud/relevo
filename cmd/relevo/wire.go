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
	"github.com/fuad-daoud/relevo/internal/installation"
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
	return hooks.NewKVLog(db.TxKV{DB: d})
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
	// The credentials were files under masterminds/.agy before this round: a file
	// that is present is imported, then removed (§4.3).
	_ = delivery.ImportAgyCreds(secrets, st.AgyCredsDir())
	_, _ = delivery.CaptureAgyCreds(os.Getenv, secrets, time.Now().UTC())
}

// newDeliverers builds Runtime.Deliverers: the agy deliverer always, and an
// OpencodeDeliverer keyed by "opencode" when sqlite3 is on PATH
// (docs/specs/2026-09-22-opencode-delivery-design.md). No sqlite3 means the
// opencode deliverer could never confirm a delivery, so an opencode mastermind's
// reports stay pending for the background wait. agy needs no external tool: it reads
// the captured credential secret from the machine database and runs agy
// itself, and reports its own failure as OutcomeUnavailable.
//
// It takes the runtime's own store, so the deliverer reads and writes the same
// database the rest of the runtime does: for a client that is one more dialled
// owner connection, and for the daemon the one shared handle. The peek verbs
// pass openGates false and wire no deliverers at all, which is what keeps
// `--preflight`/`--check` from opening the database a deliverer would.
func newDeliverers(st *store.Store) map[string]delivery.MasterMindDeliverer {
	deliverers := map[string]delivery.MasterMindDeliverer{}
	// DBIfExists, not DB: a root with no relevo.db yet -- `daemon
	// --preflight` and `--check` run against one -- must not get a database
	// conjured into it just because a deliverer was wired.
	if d, derr := st.DBIfExists(); derr == nil && d != nil {
		deliverers["agy"] = &delivery.AgyDeliverer{
			Exec:  binEnvExec{},
			Creds: db.SecretStore{DB: d},
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

// newRuntime constructs the production runtime a CLI verb uses. Config and
// secrets come from the machine database; any file present under
// <userConfigRoot>/relevo is imported into it first and removed (#4.6).
// aliases.json is never read (#80).
//
// The handle is opened through openDB, so a client reaches the machine database
// through the owner once the route is installed, while the store the runtime
// carries opens its own dialled handle on first use. The daemon instead hands
// its one shared handle to newRuntimeOn.
func newRuntime() (relevo.Runtime, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return relevo.Runtime{}, err
	}

	d, err := openDB(filepath.Join(root, "relevo.db"))
	if err != nil {
		return relevo.Runtime{}, err
	}
	return newRuntimeWith(root, d, store.New(root))
}

// newRuntimeOn constructs the daemon's runtime over the one handle it opened
// and serves: the runtime's store borrows d (store.NewShared), so nothing in
// the daemon dials its own socket and the file is opened exactly once.
func newRuntimeOn(root string, d *db.DB) (relevo.Runtime, error) {
	rt, err := newRuntimeWith(root, d, store.NewShared(root, "", d))
	if err != nil {
		return relevo.Runtime{}, err
	}
	// The one-time chain conversion, at daemon start: every row written before
	// chains carried a workflow gains the default one and its engine state, so
	// the new engine drives it. Each row converts in its own transaction; a row
	// that cannot convert is halted with the failure as its reason and the rows
	// after it still convert, so only a store-level read or write failure warns
	// here.
	if err := relevo.ConvertLegacyChains(rt); err != nil {
		slog.Warn("chains not converted to workflows", "err", err)
	}
	return rt, nil
}

// newRuntimeWith is the one body both constructors share: the config store
// reads, imports and migrates through d, and buildRuntime wires the runtime
// over st -- the store the caller's shape needs, a routed New for a client and
// a shared one for the daemon.
func newRuntimeWith(root string, d *db.DB, st *store.Store) (relevo.Runtime, error) {
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

	rt, err := buildRuntime(root, L, st, true)
	if err != nil {
		return relevo.Runtime{}, err
	}
	rt.Config = cs
	return rt, nil
}

// openDB opens path: the machine database dials the owner when one is installed
// and is opened directly otherwise; every other path -- tests, e2e roots,
// serve's per-owner roots -- is opened directly.
func openDB(path string) (*db.DB, error) {
	if d, ok, err := openDBRoute(path); ok {
		return d, err
	}
	return openDBDirect(path)
}

// openDBDirect opens path itself, ensuring its directory exists (Open's
// precondition) and minting the installation file beside it. It is the opener
// the daemon and the tests use.
func openDBDirect(path string) (*db.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	inst, err := installation.Load(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	return db.OpenWith(path, db.Options{Origin: inst.ID})
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

	L, err := loadConfigReadOnly(root, filepath.Join(configDir, "relevo"), lockedDial)
	if err != nil {
		return relevo.Runtime{}, err
	}

	// buildRuntime with openGates false: preflight and check open no database
	// and carry nil Gates/Latency, so they touch no gate record (P3b plan §4.5).
	return buildRuntime(root, L, store.New(root), false)
}

// lockedPolicy is what a read-only config load does when relevo's open lock
// says another process -- the daemon -- holds the file.
type lockedPolicy int

const (
	// lockedSkip leaves the database alone and reads the config files or the
	// defaults: the daemon's own pre-lock load, which must not dial the daemon
	// it is about to become.
	lockedSkip lockedPolicy = iota
	// lockedDial reads the database through the owner, so a peek or a bundle
	// sees the live config. It never starts the owner.
	lockedDial
)

// openReadOnlyDB is the direct read-only opener the config load uses. It is a
// var so a test can make a held file's open fail with db.ErrLocked without a
// real second process.
var openReadOnlyDB = func(path string, o db.Options) (*db.DB, error) {
	return db.OpenReadOnlyWith(path, o)
}

// loadConfigReadOnly reads config without creating, migrating or writing
// anything: the config files when any is present, else the database read-only
// when one exists, else the files again (a load of defaults). It is the one
// read-only load the `--preflight`/`--check` peek, the bugreport bundle and the
// daemon's pre-lock phase share (#4.6).
//
// When the file is held -- the daemon has it -- onLocked decides: lockedSkip
// reads the files or defaults, and lockedDial reads config through the owner,
// erroring when the owner does not answer.
func loadConfigReadOnly(root, dir string, onLocked lockedPolicy) (config.Loaded, error) {
	switch {
	case configFilesPresent(dir):
		return config.LoadFiles(dir)
	case fileExists(filepath.Join(root, "relevo.db")):
		L, err := loadConfigFromFile(filepath.Join(root, "relevo.db"))
		if err == nil {
			return L, nil
		}
		// A file another process holds, or one this build has not converted
		// yet, both leave the database alone: the former's reader is the owner
		// and the latter is converted by a writable open, not by this peek.
		if !errors.Is(err, db.ErrLocked) && !errors.Is(err, db.ErrNotConverted) {
			return config.Loaded{}, err
		}
		if onLocked == lockedSkip {
			return config.LoadFiles(dir)
		}
		return loadConfigFromOwner(root, err)
	default:
		return config.LoadFiles(dir)
	}
}

// loadConfigFromFile opens path read-only and loads the config through it.
func loadConfigFromFile(path string) (config.Loaded, error) {
	d, err := openReadOnlyDB(path, db.Options{})
	if err != nil {
		return config.Loaded{}, err
	}
	defer func() { _ = d.Close() }()
	return config.Open(d).Load()
}

// loadConfigFromOwner reads the config through the daemon that holds the file.
// The dial carries the verb budget and never starts the owner; a failure names
// both the held file and the failed dial.
func loadConfigFromOwner(root string, lockErr error) (config.Loaded, error) {
	d, err := dialOwner(context.Background(), root, verbDialBudget)
	if err != nil {
		return config.Loaded{}, fmt.Errorf("relevo.db is held by the daemon and the owner did not answer: %w (the file open failed with: %v)", err, lockErr)
	}
	defer func() { _ = d.Close() }()
	return config.Open(d).Load()
}

// dialOwner dials the owner on root's socket within budget, without starting
// it: a missing or unanswering socket is an error. The caller's ctx bounds the
// whole call, so a command deadline can end the dial sooner than the budget.
func dialOwner(ctx context.Context, root string, budget time.Duration) (*db.DB, error) {
	sock, err := ownerSocket(root)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return db.DialContext(ctx, sock)
}

// dialOwnerAdHoc is dialOwner for the ad-hoc read path: the pool's connections
// mark themselves in the handshake, so the owner may refuse a request while it
// is reaping an abandoned statement. The caller refuses on that answer rather
// than falling back, because its dial succeeded.
func dialOwnerAdHoc(ctx context.Context, root string, budget time.Duration) (*db.DB, error) {
	sock, err := ownerSocket(root)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return db.DialContextAdHoc(ctx, sock)
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

// buildRuntime wires the Runtime from one loaded config and one store, the
// shape newRuntimeWith uses. openGates opens the store's database for the gate,
// availability and latency records (P3b plan §4.5): newRuntime passes true, and
// a DB() error is fatal for the verb; newRuntimePeek passes false, so preflight
// and check leave the root with no database, nil gates and no deliverers.
func buildRuntime(root string, L config.Loaded, st *store.Store, openGates bool) (relevo.Runtime, error) {
	pol := L.Policy

	// Warnings are carried, never printed: every CLI command calls newRuntime,
	// so printing here would be noise (#372 §4.4). `relevo doctor` renders them
	// and the daemon logs each once.
	configWarnings := L.Warnings

	cls, _ := classify.Resolve(pol.Classify, L.Typesafe, os.Getenv)

	reader, prices := newUsageReader(L.Prices)

	gitClient := git.NewClient("git", 10*time.Second, git.DefaultMaxPatchBytes)

	// Gates and latency live in the store root's database (P3b plan §4.5).
	// The channel claims, the mastermind registry and the hooks run log live in
	// the same database (P3b round 2 §4.1-§4.4), so `relevo daemon --preflight`
	// and `--check`, which pass openGates false, open no database at all.
	var (
		gates  db.KV
		claims delivery.ClaimStore
		runLog hooks.RunLog
	)
	if openGates {
		d, err := st.DB()
		if err != nil {
			return relevo.Runtime{}, err
		}
		gates = d
		claims = &delivery.KVClaims{KV: db.TxKV{DB: d}}
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
	var opencodeSessionDir func(sessionID string) (string, error)
	if _, err := exec.LookPath("sqlite3"); err == nil {
		finder := delivery.OpencodeSessionFinder{
			Exec:   binExec{},
			DBPath: opencodeDBPath(),
		}
		opencodeSession = finder.Find
		opencodeSessionDir = finder.Directory
	}

	rt := relevo.Runtime{
		Git:            gitClient,
		Runner:         proc.New(),
		Store:          st,
		Candidates:     L.Candidates,
		Accounts:       L.Accounts,
		OpencodeAuth:   relevo.OSOpencodeAuth(opencodeDBPath()),
		Gates:          gates,
		Latency:        gates,
		Policy:         pol,
		Registry:       L.Registry,
		ConfigWarnings: configWarnings,
		Scope:          scopeFromPolicy(pol.ScopeFor(false)),
		Classify:       cls,
		Usage:          reader,
		Prices:         prices,
		Fetcher:        release.NewHTTPFetcher(release.Source(), 5*time.Second),
		Now:            time.Now,
		Hooks:          dispatcher,
		Remote:         remoteClient,
		// The transport depends only on the git client, not the servers
		// section, so a server added while the daemon runs needs no rebuild.
		Transport:          remote.NewBundleTransport(gitClient, ""),
		Roles:              harness.OSRoleChecker(),
		Channels:           claims,
		ProcStart:          procStartUnix,
		OpencodeSession:    opencodeSession,
		OpencodeSessionDir: opencodeSessionDir,
		SessionReaper:      relevo.NewSessionReaper(binExec{}),
	}
	// Deliverers need the runtime's store, and a runtime with no database open
	// (preflight, check) carries none at all: wiring one would open -- and
	// migrate -- the very database the peek must leave alone.
	if openGates {
		rt.Deliverers = newDeliverers(st)
	}
	// The registry needs the runtime's own store and clock, so it is wired
	// here rather than in the literal above. A runtime with no database open
	// (preflight, check) keeps a nil registry, which every caller already
	// treats as "no masterminds".
	if openGates {
		reg, rerr := mastermindRegistry(rt)
		if rerr != nil {
			return relevo.Runtime{}, rerr
		}
		rt.MasterMinds = reg
	}
	return rt, nil
}

// procStartUnix reads a process's start time in Unix seconds, the pid-reuse
// defence mastermind.Resolve's host step and relevo mcp's claim need. A read
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
