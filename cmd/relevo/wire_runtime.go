package main

import (
	"log/slog"
	"os"
	"os/exec"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/classify"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
	"github.com/fuad-daoud/relevo/internal/uplink"
	usagepkg "github.com/fuad-daoud/relevo/internal/usage"
)

// runtimeGateHandles bundles the handles buildRuntime wires over the shared
// file when openGates holds: the gate and latency records, the push claims,
// and the hooks run log.
type runtimeGateHandles struct {
	gates   db.KV
	gatesDB *db.DB
	history *db.DB
	claims  delivery.ClaimStore
	waits   delivery.WaitClaimStore
	runLog  hooks.RunLog
}

// openRuntimeGates opens the store's database for the gate, availability and
// latency records: the push claims and the hooks run log live in the same
// database.
func openRuntimeGates(st *store.Store) (runtimeGateHandles, error) {
	var h runtimeGateHandles
	d, err := st.DB()
	if err != nil {
		return h, err
	}
	// Gates, latency, the claims and the run log are all this machine's own
	// records -- a pid, a listen address, a row about a round that ran here
	// -- so they bind the machine-local file. The binding happens once, here,
	// rather than in each surface, so a surface added later cannot reach the
	// shared file by accident.
	local := d.LocalOrSelf()
	h.gates = local
	// The gates document records a write's stamp and its export signal in
	// this same file, so it needs the handle that can open a transaction
	// rather than the bare kv surface the ledger reads and writes through.
	h.gatesDB = local
	h.claims = &delivery.KVClaims{KV: db.TxKV{DB: local}}
	h.waits = &delivery.KVWaitClaims{KV: db.TxKV{DB: local}}
	h.runLog = hooksRunLog(d)
	// The one-time move of the two history documents into their tables runs
	// here, where the runtime opens both files and before anything reads or
	// writes either history. Every reader below -- the daemon, every verb, the
	// join `db sync enable` performs on the daemon's side -- goes through
	// this block, so one pass covers them all and none of them can see a
	// document the tables have replaced. A failure warns and leaves the
	// documents in place: the marker says the move did not happen, and the
	// next start retries it.
	if moved, err := availability.MigrateHistoryDocuments(local, d, d.Origin()); err != nil {
		slog.Warn("availability history documents not migrated", "err", err)
	} else if moved.Events+moved.Samples > 0 {
		slog.Info("availability history documents moved into their tables",
			"events", moved.Events, "samples", moved.Samples)
	}
	// The two history tables live in the shared file, so this is the handle
	// every gate and latency display reads them through.
	h.history = d
	return h, nil
}

// opencodeSessionFuncs wires the session resolvers when sqlite3 is on PATH:
// the opencode server resolves each tool call from its session.
func opencodeSessionFuncs() (session func(cwd string, now time.Time) (string, error), dir func(sessionID string) (string, error)) {
	if _, err := exec.LookPath("sqlite3"); err == nil {
		finder := delivery.OpencodeSessionFinder{
			Exec:   binExec{},
			DBPath: opencodeDBPath(),
		}
		return finder.Find, finder.Directory
	}
	return nil, nil
}

// finishRuntimeWiring wires what the runtime literal cannot: the deliverers
// and the registry over the runtime's own store when a database is open, and
// the round-close and remote-send hooks in every mode. A runtime with no
// database open (preflight, check) carries none of the gated pieces: wiring
// one would open -- and migrate -- the very database the peek must leave
// alone, and the registry stays nil, which every caller already treats as
// "no masterminds".
func finishRuntimeWiring(rt *relevo.Runtime, st *store.Store, openGates bool) error {
	// Deliverers need the runtime's store.
	if openGates {
		rt.Deliverers = newDeliverers(st)
	}
	// The registry needs the runtime's own store and clock, so it is wired
	// here rather than in the literal above.
	if openGates {
		reg, rerr := mastermindRegistry(*rt)
		if rerr != nil {
			return rerr
		}
		rt.MasterMinds = reg
	}
	rt.CloseServedRound = serve.CloseServedRound
	// The remote send runs above the engine: one wiring point here covers the
	// CLI and the daemon, which share buildRuntime.
	rt.RemoteSend = relevo.RemoteSendDeps{
		Send:   uplink.SendRemote,
		Ship:   uplink.Ship,
		Record: uplink.Record,
	}
	return nil
}

// runtimeAssembly bundles the wired pieces assembleRuntime sets on the
// runtime literal: every surface buildRuntime resolves before the literal.
type runtimeAssembly struct {
	gitClient          *git.Client
	runner             *proc.Runner
	tmpDir             string
	cls                classify.Classifier
	reader             usagepkg.Reader
	prices             usagepkg.Prices
	dispatcher         hooks.Dispatcher
	remoteClient       relevo.RemoteClient
	opencodeSession    func(cwd string, now time.Time) (string, error)
	opencodeSessionDir func(sessionID string) (string, error)
}

// buildRuntimeSurfaces resolves the policy classifier, the usage reader and
// the git client: the read-only surfaces every verb shares.
func buildRuntimeSurfaces(L config.Loaded, pol policy.Policy) (classify.Classifier, usagepkg.Reader, usagepkg.Prices, *git.Client) {
	cls, _ := classify.Resolve(pol.Classify, L.Typesafe, os.Getenv)
	reader, prices := newUsageReader(L.Prices)
	return cls, reader, prices, git.NewClient("git", 10*time.Second, git.DefaultMaxPatchBytes)
}

// buildRuntimeServices wires the hooks dispatcher and the remote client: the
// dispatcher runs hook runs on the machine database's run log, and the
// client needs the servers section and the client key.
func buildRuntimeServices(L config.Loaded, runLog hooks.RunLog, pol policy.Policy) (hooks.Dispatcher, relevo.RemoteClient, error) {
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

// buildRuntimeRunner returns the process runner with its temp dir under the
// state root: the runner's children keep their temp files under the same
// state tmp dir the transport writes bundles into, so a parallel round never
// fills the system temp a serve daemon happens to inherit.
func buildRuntimeRunner(root string) (*proc.Runner, string, error) {
	tmpDir, err := tmpdir.TempDir(root)
	if err != nil {
		return nil, "", err
	}
	runner := proc.New()
	runner.TmpDir = tmpDir
	return runner, tmpDir, nil
}

// assembleRuntime sets the wired pieces on the runtime literal: the shape
// buildRuntime returns, with candidates, policy and warnings from the loaded
// config and the store it was built over.
func assembleRuntime(a runtimeAssembly, st *store.Store, L config.Loaded, pol policy.Policy, configWarnings []string, gates runtimeGateHandles) relevo.Runtime {
	return relevo.Runtime{
		Git:            a.gitClient,
		Runner:         a.runner,
		Store:          st,
		TmpRoot:        a.tmpDir,
		Candidates:     L.Candidates,
		Accounts:       L.Accounts,
		OpencodeAuth:   relevo.OSOpencodeAuth(opencodeDBPath()),
		Gates:          gates.gates,
		GatesDB:        gates.gatesDB,
		History:        gates.history,
		Policy:         pol,
		Registry:       L.Registry,
		ConfigWarnings: configWarnings,
		Scope:          scopeFromPolicy(pol.ScopeFor(false)),
		Classify:       a.cls,
		Usage:          a.reader,
		Prices:         a.prices,
		Fetcher:        release.NewHTTPFetcher(release.Source(), 5*time.Second),
		Now:            time.Now,
		Hooks:          a.dispatcher,
		Remote:         a.remoteClient,
		// The transport depends only on the git client, not the servers
		// section, so a server added while the daemon runs needs no rebuild.
		Transport:          remote.NewBundleTransport(a.gitClient, a.tmpDir),
		Roles:              harness.OSRoleChecker(),
		Channels:           gates.claims,
		Waits:              gates.waits,
		ProcStart:          procStartUnix,
		MuseHost:           mastermind.MuseHostSeam(museCommOf, musePPidOf),
		OpencodeSession:    a.opencodeSession,
		OpencodeSessionDir: a.opencodeSessionDir,
		SessionReaper:      relevo.NewSessionReaper(binExec{}),
	}
}
