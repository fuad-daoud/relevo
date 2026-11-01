package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/classify"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/installation"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/setup"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
	"github.com/fuad-daoud/relevo/internal/uplink"
)

// hooksRunLog is the hook run log over d's machine-local file. A nil log
// records nothing, so a caller with no database open wires the nil it already
// gets from a nil handle.
func hooksRunLog(d *db.DB) hooks.RunLog {
	if d == nil {
		return nil
	}
	return hooks.NewKVLog(db.TxKV{DB: d.LocalOrSelf()})
}

// newHooksDispatcher fans every event out to the local hooks.d dispatcher and,
// when notify.webhooks is non-empty, a WebhookSink beside it.
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

// opencodeServiceFiles returns candidate service.json paths in preference order:
// the state dir first, then the config dir.
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

// captureAgyEnv persists the calling agy session's agentapi credentials when the
// environment carries them. It runs for every verb but opens the machine
// database only when there is something to capture. Failures stay silent:
// capture is best-effort and must never change a command's exit code or output.
func captureAgyEnv() {
	if !delivery.AgyEnvPresent(os.Getenv) {
		return
	}
	root, err := tmpdir.DefaultRoot()
	if err != nil {
		return
	}
	st := store.New(root)
	d, err := st.DB()
	if err != nil {
		return
	}
	secrets := db.SecretStore{DB: d}
	// A credential file that is present is imported, then removed.
	_ = delivery.ImportAgyCreds(secrets, st.AgyCredsDir())
	_, _ = delivery.CaptureAgyCreds(os.Getenv, secrets, time.Now().UTC())
}

// newDeliverers builds Runtime.Deliverers over the runtime's own store, so they
// read and write the same database the rest of the runtime does: the agy
// deliverer always (agy needs no external tool), and an OpencodeDeliverer
// keyed by "opencode" when sqlite3 is on PATH. Without sqlite3 the opencode
// deliverer could never confirm a
// delivery, so its reports stay pending. The peek verbs wire no deliverers at
// all, which keeps --preflight/--check from opening the database a deliverer
// would.
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
// <userConfigRoot>/relevo is imported into it first and removed.
// aliases.json is never read.
//
// The handle is opened through openDB, so a client reaches the machine database
// through the owner once the route is installed, while the store the runtime
// carries opens its own dialled handle on first use. The daemon instead hands
// its one shared handle to newRuntimeOn.
func newRuntime() (relevo.Runtime, error) {
	root, err := tmpdir.DefaultRoot()
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
// and serves: the runtime's store borrows d, so the file is opened exactly once.
func newRuntimeOn(root string, d *db.DB) (relevo.Runtime, error) {
	rt, err := newRuntimeWith(root, d, store.NewShared(root, "", d))
	if err != nil {
		return relevo.Runtime{}, err
	}
	// The chain conversion at daemon start gives every row without a workflow
	// the default one, each in its own transaction, so one bad row halts with
	// its reason while the rest still convert and only a store-level failure warns.
	if err := relevo.ConvertLegacyChains(rt); err != nil {
		slog.Warn("chains not converted to workflows", "err", err)
	}
	return rt, nil
}

// newRuntimeWith is the one body both constructors share: the config store reads,
// imports and migrates through d, and buildRuntime wires the runtime over st.
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
		// A schema a newer relevo wrote is never written by this binary.
		slog.Warn("relevo.db schema is newer; config import skipped")
	}

	if !d.Newer() {
		// Names are derived in memory by candidate.Parse either way, so a
		// failure here is a warning, never a refusal to start.
		if _, err := cs.EnsureCandidateNames(); err != nil {
			slog.Warn("candidate names not written to config", "err", err)
		}
		// One-time migration of a pre-actors config to actors plus agents; an
		// error is a warning too, and the old config keeps working.
		if migrated, err := cs.MigrateToActors(); err != nil {
			slog.Warn("config: roles not migrated to actors", "err", err)
		} else if migrated {
			slog.Info("config: roles migrated to actors (relevo config log)")
		}
		// The theme seed writes the release's two palettes into the themes
		// section once per database, so a later unset stays unset; a failure
		// is a warning like the others.
		if seeded, err := cs.EnsureThemesSeed(); err != nil {
			slog.Warn("config: themes not seeded", "err", err)
		} else if seeded {
			slog.Info("config: board palettes seeded into themes (relevo config log)")
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
	setup.NoteUnconfigured(activeVerb, rt.Candidates.Len() > 0)
	return rt, nil
}

// openDB opens path: the machine database dials the owner when one is installed
// and is opened directly otherwise, as is every other path.
func openDB(path string) (*db.DB, error) {
	if d, ok, err := openDBRoute(path); ok {
		return d, err
	}
	return openDBDirect(path)
}

// openDBDirect opens path itself, ensuring its directory exists and minting the
// installation file beside it. The machine-local file beside the shared
// database is opened with it and runs the same migrations, so the split is a
// placement decision rather than a schema fork.
func openDBDirect(path string) (*db.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	inst, err := installation.Load(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	return db.OpenSplit(path, db.Options{Origin: inst.ID})
}

// newRuntimePeek constructs the runtime `relevo daemon --preflight` and
// `--check` need. It never creates, migrates or writes anything.
func newRuntimePeek() (relevo.Runtime, error) {
	root, err := tmpdir.DefaultRoot()
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
	// and carry nil Gates/Latency, so they touch no gate record.
	return buildRuntime(root, L, store.New(root), false)
}

// runtimeStores carries the database-backed seams buildRuntime wires: the
// gates and latency records with the history handle beside them.
type runtimeStores struct {
	gates   db.KV
	gatesDB *db.DB
	history *db.DB
	claims  delivery.ClaimStore
	waits   delivery.WaitClaimStore
	runLog  hooks.RunLog
}

// openRuntimeStores opens the store's database for the gate, availability and
// latency records. The history tables stay on the shared handle while the
// machine's own records bind the machine-local file. When openGates is false
// it returns zero stores: preflight and check open no database at all.
func openRuntimeStores(st *store.Store, openGates bool) (runtimeStores, error) {
	var s runtimeStores
	if !openGates {
		return s, nil
	}
	d, err := st.DB()
	if err != nil {
		return s, err
	}
	// These are this machine's own records, so they bind the machine-local
	// file: the binding happens once here, so a later surface cannot reach
	// the shared file by accident.
	local := d.LocalOrSelf()
	s.gates = local
	// The gates document needs the handle that can open a transaction,
	// rather than the bare kv surface the ledger reads and writes through.
	s.gatesDB = local
	s.claims = &delivery.KVClaims{KV: db.TxKV{DB: local}}
	s.waits = &delivery.KVWaitClaims{KV: db.TxKV{DB: local}}
	s.runLog = hooksRunLog(d)
	// The one-time move of the two history documents into their tables runs
	// here, before anything reads either history, so one pass covers every
	// reader. A failure warns and leaves the documents in place for the next
	// start to retry.
	if moved, merr := availability.MigrateHistoryDocuments(local, d, d.Origin()); merr != nil {
		slog.Warn("availability history documents not migrated", "err", merr)
	} else if moved.Events+moved.Samples > 0 {
		slog.Info("availability history documents moved into their tables",
			"events", moved.Events, "samples", moved.Samples)
	}
	// The two history tables live in the shared file, so this is the handle
	// every gate and latency display reads them through.
	s.history = d
	return s, nil
}

// wireRuntimeServices builds the hook dispatcher and the remote client over
// the loaded config. A missing client key is not fatal: the doctor names it
// once, and every remote path reports it when one is next run.
func wireRuntimeServices(L config.Loaded, runLog hooks.RunLog, pol policy.Policy) (hooks.Dispatcher, relevo.RemoteClient, error) {
	hooksCfg, err := resolveHooksConfig(L.Hooks, runLog)
	if err != nil {
		return nil, nil, err
	}
	dispatcher := newHooksDispatcher(hooksCfg, pol)

	remoteClient, err := newRemoteClient(L.Servers, L.ClientKey)
	if err != nil {
		return nil, nil, err
	}
	return dispatcher, remoteClient, nil
}

// opencodeSessionSeams wires the opencode session lookup when sqlite3 is on
// PATH; without it the seams stay nil and no opencode session is ever found.
func opencodeSessionSeams() (func(cwd string, now time.Time) (string, error), func(sessionID string) (string, error)) {
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
	return opencodeSession, opencodeSessionDir
}

// buildRuntime wires the Runtime from one loaded config and one store.
// openGates opens the store's database for the gate, availability and latency
// records: newRuntime passes true, and newRuntimePeek false, so preflight and
// check leave the root with no database, nil gates and no deliverers.
func buildRuntime(root string, L config.Loaded, st *store.Store, openGates bool) (relevo.Runtime, error) {
	pol := L.Policy

	// Warnings are carried, never printed: every CLI command calls newRuntime,
	// so printing here would be noise. `relevo doctor` renders them
	// and the daemon logs each once.
	configWarnings := L.Warnings

	cls, _ := classify.Resolve(pol.Classify, L.Typesafe, os.Getenv)

	reader, prices := newUsageReader(L.Prices)

	gitClient := git.NewClient("git", 10*time.Second, git.DefaultMaxPatchBytes)

	stores, err := openRuntimeStores(st, openGates)
	if err != nil {
		return relevo.Runtime{}, err
	}

	dispatcher, remoteClient, err := wireRuntimeServices(L, stores.runLog, pol)
	if err != nil {
		return relevo.Runtime{}, err
	}

	opencodeSession, opencodeSessionDir := opencodeSessionSeams()

	tmpDir, runner, err := newRuntimeRunner(root)
	if err != nil {
		return relevo.Runtime{}, err
	}

	rt := relevo.Runtime{
		Git:            gitClient,
		Runner:         runner,
		Store:          st,
		TmpRoot:        tmpDir,
		Candidates:     L.Candidates,
		Accounts:       L.Accounts,
		OpencodeAuth:   relevo.OSOpencodeAuth(opencodeDBPath()),
		Gates:          stores.gates,
		GatesDB:        stores.gatesDB,
		History:        stores.history,
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
		Transport:          remote.NewBundleTransport(gitClient, tmpDir),
		Roles:              harness.OSRoleChecker(),
		Channels:           stores.claims,
		Waits:              stores.waits,
		ProcStart:          procStartUnix,
		MuseHost:           mastermind.MuseHostSeam(museCommOf, musePPidOf),
		OpencodeSession:    opencodeSession,
		OpencodeSessionDir: opencodeSessionDir,
		SessionReaper:      relevo.NewSessionReaper(binExec{}),
	}
	return finishRuntime(rt, st, openGates)
}

// newRuntimeRunner builds the runner whose children share the state tmp dir
// the transport writes into, so a parallel round never fills the system temp.
func newRuntimeRunner(root string) (string, *proc.Runner, error) {
	tmpDir, err := tmpdir.TempDir(root)
	if err != nil {
		return "", nil, err
	}
	runner := proc.New()
	runner.TmpDir = tmpDir
	return tmpDir, runner, nil
}

// finishRuntime wires the database-backed seams onto the assembled runtime:
// the deliverers and the mastermind registry when a database is open, and the
// serve and remote-send seams always.
func finishRuntime(rt relevo.Runtime, st *store.Store, openGates bool) (relevo.Runtime, error) {
	// A runtime with no database open (preflight, check) carries no deliverers:
	// wiring one would open the very database the peek must leave alone.
	if openGates {
		rt.Deliverers = newDeliverers(st)
	}
	// A runtime with no database open keeps a nil registry, which every caller
	// already treats as "no masterminds".
	if openGates {
		reg, err := mastermindRegistry(rt)
		if err != nil {
			return relevo.Runtime{}, err
		}
		rt.MasterMinds = reg
	}
	rt.CloseServedRound = serve.CloseServedRound
	// The remote send runs above the engine, so one wiring point here covers
	// both the CLI and the daemon.
	rt.RemoteSend = relevo.RemoteSendDeps{
		Send:   uplink.SendRemote,
		Ship:   uplink.Ship,
		Record: uplink.Record,
	}
	return rt, nil
}

// procStartUnix reads a process's start time in Unix seconds, the pid-reuse
// defence behind mastermind.Resolve's host step. A failure is returned, not
// swallowed: Resolve treats it as "the host step cannot run".
func procStartUnix(pid int) (int64, error) {
	started, err := proc.StartTime(context.Background(), pid)
	if err != nil {
		return 0, err
	}
	return started.Unix(), nil
}

// museCommOf reads one process's command name from /proc/<pid>/comm. A process
// that has gone reads as missing.
func museCommOf(pid int) (string, bool) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return "", false
	}
	if comm := strings.TrimSpace(string(raw)); comm != "" {
		return comm, true
	}
	return "", false
}

// museEnvironOf reads one process's environment from /proc/<pid>/environ, the
// scrubbed-environment fallback's eyes. A process that has gone reads as missing.
func museEnvironOf(pid int) (map[string]string, bool) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil, false
	}
	return mastermind.ParseEnviron(raw), true
}

// musePPidOf reads one process's parent pid from /proc/<pid>/status. A process
// that has gone, or a status without a PPid line, reads as missing.
func musePPidOf(pid int) (int, bool) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != "PPid" {
			continue
		}
		if ppid, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && ppid > 0 {
			return ppid, true
		}
		return 0, false
	}
	return 0, false
}

// newRemoteClient wires Runtime.Remote through the one rule the daemon's config
// reload shares: nil when the servers section is absent or empty. Servers without
// a client key are not fatal -- every remote path already reports
// ErrRemoteUnavailable on a nil Runtime.Remote -- but the fix prints once, since
// a configured server this client cannot reach is a setup mistake worth naming
// immediately rather than only when a remote command is next run.
func newRemoteClient(servers remote.Servers, key []byte) (relevo.RemoteClient, error) {
	remoteClient, err := relevo.NewRemoteClient(servers, key)
	if errors.Is(err, relevo.ErrNoClientKey) {
		fmt.Fprintln(os.Stderr, "relevo: "+err.Error())
		return nil, nil
	}
	return remoteClient, err
}
