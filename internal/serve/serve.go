// Package serve is the remote-builder server: the client registry, the v1
// wire routes and the daemon that ticks them.
package serve

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/installation"
	"github.com/fuad-daoud/relevo/internal/isolate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
	"github.com/fuad-daoud/relevo/internal/usage"
)

type Config struct {
	Root string // <state>/serve
	// DB is the machine database, which the caller opens and closes; the server
	// opens none of its own under Root.
	DB         *db.DB
	Candidates *candidate.Set
	// Accounts is the server's own login pool, from its own config. Empty
	// means every serve path is byte-identical to a host with no accounts.
	Accounts       account.Set
	Policy         policy.Policy
	Runner         spawn.Runner
	Git            *git.Client // concrete: the transport needs it too
	Now            func() time.Time
	Interval       time.Duration // daemon tick, floored by relevo.NewDaemon
	MaxBundleBytes int64         // default 512 << 20
	Usage          usage.Reader  // nil = the server records "no reader"
	Prices         usage.Prices  // zero value = embedded defaults via usage.Fold's rules
	// StartedAt is when this process started; zero disables the restart check.
	StartedAt time.Time
	// Roles checks candidate harness role-file coverage; nil means no check.
	Roles harness.RoleChecker
	// Registry is relevo's roles registry; nil derives it from Candidates and Policy.
	Registry *roles.Registry
	// MaxBuilders caps headless builders at once across all owners; 0 means the policy default.
	MaxBuilders int
	// Reloader refreshes the config sections a serve restart does not govern --
	// the candidates, the policy, the roles registry and the accounts pool --
	// once per tick, so a config edit takes effect on the next tick or
	// admission. Nil keeps the copy loaded at startup for the process's life.
	Reloader *ConfigRefresher
	// Hooks dispatches lifecycle events; nil means none.
	Hooks hooks.Dispatcher
	// DiskFree reads free bytes under a path for the disk preflight; nil means
	// the real filesystem.
	DiskFree func(string) (uint64, error)
	// Scope is the systemd scope template served rounds launch under; nil means none.
	Scope *spawn.ScopeSpec
	// Isolation is the tenant-isolation mode this server runs under. The zero
	// value is normalized to isolate.ModeNone by New, so every server emits
	// "isolation":"none". A configured user or container never reaches New:
	// cmdServeRun refuses it first.
	Isolation isolate.Mode
	// IsolationImage names the container image, set only in container mode.
	IsolationImage string
	// SessionReaper deletes harness sessions a served round abandoned; nil
	// means the deletes are skipped and the entries stay on the binding.
	SessionReaper relevo.SessionDeleter
	// LookupUser resolves an owner's declared unix_user to a tenant. It is
	// required in user mode; os/user.Lookup is the production value.
	LookupUser func(string) (isolate.Tenant, error)
	// SharedLogins keeps the server's account-home entries in a user-mode
	// round's environment instead of stripping them, from
	// serve.isolation_shared_logins.
	SharedLogins bool
	// ReaperFor builds the session reaper for one tenant, so a user-mode
	// server's deletes run as the tenant. Nil keeps SessionReaper for every
	// owner.
	ReaperFor func(isolate.Tenant) relevo.SessionDeleter
	// Installation is this server's own identity: its id is the origin of
	// every row the server writes and the id WhoAmI advertises, and its label
	// names the machine to clients. The zero value advertises no installation.
	Installation installation.Installation
	// Audiences is the set of request audiences this server accepts: its
	// certificate fingerprint and every host:<h> a --public-host value names.
	// An empty set refuses every signed request, so production must fill it.
	Audiences []string
	// ClientKey is this head's own client private key, the one
	// `relevo config server key` creates. Everything the head sends to a
	// burst node is signed as a serve client, so an empty key keeps
	// bursting off even when a command is configured.
	ClientKey []byte
	// Burst is the serve.burst policy block; nil means no provider to run.
	Burst *policy.BurstPolicy
	// Provider is the head's burst report exchange; nil means built from
	// Burst's command when bursting is on.
	Provider burst.Provider
}

type Server struct {
	cfg Config
	// reloaded is cfg as of the last successful tick refresh, guarded by s.mu.
	// It differs from cfg only in the four sections a config edit may replace --
	// candidates, policy, roles registry and accounts -- and every reader copies
	// the whole struct out under the lock, so a request or a tick step sees one
	// consistent config for all of its work and never a mix of two reloads.
	reloaded     Config
	clients      *Clients
	nonces       *remote.NonceWindow // ttl = remote.MaxClockSkew
	transport    remote.TreeTransport
	addr         net.Addr
	insecureHTTP bool
	// audiences is copied from cfg so a later mutation of the caller's
	// slice cannot widen it.
	audiences []string
	// mu guards the stores map, the listen flags and the reloaded config. It
	// is never held across Store, git or transport I/O -- per-owner exclusion
	// is each owner's own Store lock (see ownerStore) and the server-wide
	// builder cap is admitMu -- so a slow owner's reconcile cannot block
	// another owner's poll.
	mu sync.RWMutex
	// gates is a `serve.`-prefixed view of the machine database, so a server-wide
	// gate never collides with this machine's own rows.
	gates db.KV
	// gatesLocal is that machine's own ledger surface -- the unprefixed row
	// `relevo gate <token>` records on this host, which the synced gates document
	// applies. Served runtimes read the union of the two (relevo.GatesLocal,
	// gateunion.go), so a host gate steers served picks and switches; served
	// writes still land in the prefixed row alone.
	gatesLocal db.KV
	// stores is one Store per owner root, guarded by s.mu. The map only ever
	// grows; the *Store values are immutable after creation and are safe to
	// use once read out from under s.mu.
	stores map[string]*store.Store
	// admitMu serializes census-and-admit, so the server-wide builder cap is
	// decided against one running count even when a tick's admit and a
	// round start's admit overlap. collectSettled -> pruneUnusedRepos -> admit
	// still run in that order inside the section they share. The prune takes
	// the bare-repo pin mutex inside that hold, and the bind paths take that
	// mutex alone, so the order is admitMu -> pin mutex and never the reverse.
	admitMu sync.Mutex
	// tmpSweptAt maps an owner root to its last tmp sweep. The Daemon that
	// sweeps lives for one tick, so the pace has to outlive it here.
	tmpSweptAt sync.Map
	liveCache  *liveCache
	// nodeLive is the cached node live view observe fills for ServedLive.
	nodeLive *liveCache
	// burstProvider is the head's resolved report provider: nil when
	// bursting is off, which is either no configured command or a command
	// refused for want of a client key. With it nil every later burst step
	// is a no-op.
	burstProvider burst.Provider
	// burstOffErr is why a configured burst command was refused at
	// startup, naming the command that fixes it; "" when bursting came up
	// or was never configured. The status block reports it as last_err.
	burstOffErr string
	// burstTable is the node table loaded from the machine database at
	// startup; nil when bursting is off. Its rows keep the provider's
	// response order, which placement's tie-break reads.
	burstTable  *burst.NodeTable
	burstHealth *burstHealth
	// tickFn and mirrorEvery, when set, replace Tick and ownerMirrorInterval.
	tickFn      func(context.Context) error
	mirrorEvery time.Duration
	// fanout tracks in-flight burst fan-outs (node stops, unbinds and
	// gates), so the drain can wait for them inside the shutdown budget
	// instead of returning while one is still in flight.
	fanout sync.WaitGroup
	// stopFanoutHook, when non-nil, runs when one node-stop fan-out lands
	// its answer, carrying the attempt's error (nil when the stop landed or
	// the row took no new traffic). Tests set it to join the detached
	// goroutine without ever sleeping.
	stopFanoutHook func(err error)
	// unbindFanoutHook joins node-unbind fan-outs the way stopFanoutHook
	// joins stops: it runs once per target node when that node's attempt
	// lands.
	unbindFanoutHook func(err error)
	// gateFanoutHook joins gate fan-outs the way unbindFanoutHook joins
	// unbinds: it runs once per dialled node when that node's attempt
	// lands, carrying nil when the gate landed.
	gateFanoutHook func(err error)
	// heard remembers which live head-ledger gates each node already holds,
	// by node id and gate subject, so the resync pass sends only what the
	// node has not heard. It is in-memory and pruned to the table members
	// every pass: a restart forgets it and resends live gates, which the
	// nodes' idempotent receive turns into a no-op. It is not a NodeRow
	// field, because merges rebuild rows per provider response and the
	// table's persistence would carry stale heard state across restarts.
	heard map[string]map[string]time.Time
}

// EnsureStateRoot creates the state root's bindings directory, the directory
// the doctor's serve/state check probes. It is idempotent and returns the raw
// error, as New does. The root is owner-only: it holds the database and every
// binding's files, and MkdirAll never chmods an existing one.
func EnsureStateRoot(root string) error {
	return os.MkdirAll(filepath.Join(root, "bindings"), tmpdir.StateRootMode)
}

// UserModeRoot is where a user-mode server keeps its state when --state is
// unset: a fixed, root-owned directory outside any tenant's home, so the
// daemon and its tenants agree on one location.
const UserModeRoot = "/var/lib/relevo"

// EnsureUserModeRoot creates the user-mode serve root and its bindings/, repos/
// and tmp/ children with the 0711 modes a tenant needs to traverse them. It is
// idempotent, and refuses a symlink or a non-directory where one of them should
// be.
func EnsureUserModeRoot(root string) error {
	for _, d := range []string{
		root,
		filepath.Join(root, "bindings"),
		filepath.Join(root, "repos"),
		filepath.Join(root, "tmp"),
	} {
		if err := ensureRootDir(d, serveRootMode); err != nil {
			return err
		}
	}
	return nil
}

func New(cfg Config) (*Server, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxBundleBytes <= 0 {
		cfg.MaxBundleBytes = 512 << 20
	}
	if cfg.Git == nil {
		cfg.Git = git.NewClient("git", 0, 0)
	}
	// Normalize the unset isolation mode, so every server emits
	// "isolation":"none".
	if cfg.Isolation == "" {
		cfg.Isolation = isolate.ModeNone
	}
	tmpDir := filepath.Join(cfg.Root, "tmp")
	if err := os.MkdirAll(tmpDir, tmpdir.StateRootMode); err != nil {
		return nil, err
	}
	// Before anything listens, drop the request temp files a previous process left
	// behind. A sweep error is a Warn and startup continues.
	if removed, err := sweepTmp(tmpDir, time.Hour, cfg.Now()); err != nil {
		slog.Warn("temp sweep failed", "dir", tmpDir, "removed", removed, "err", err)
	}
	local := cfg.DB.LocalOrSelf()
	clients, err := LoadClients(local)
	if err != nil {
		return nil, err
	}
	// A configured burst command without a client key is a
	// misconfiguration: every request the head sends to a node is signed
	// as a serve client, and there is no key to sign with. It is one
	// startup error, not a refused boot -- the head keeps serving local
	// rounds -- and bursting stays off: no provider resolved, no table,
	// and the reason is kept for the status block.
	var burstProvider burst.Provider
	var burstOffErr string
	if cfg.Burst != nil && len(cfg.Burst.Command) > 0 {
		if len(cfg.ClientKey) == 0 {
			burstOffErr = "bursting is off: serve.burst.command is set but this head has no client key; run `relevo config server key` to create and enroll one"
			slog.Error(burstOffErr)
		} else {
			burstProvider = cfg.Provider
			if burstProvider == nil {
				burstProvider = &burst.CommandProvider{Argv: cfg.Burst.Command}
			}
		}
	}
	var burstTable *burst.NodeTable
	if burstProvider != nil {
		burstTable, err = loadBurstTable(local)
		if err != nil {
			return nil, err
		}
	}
	nonces := remote.NewNonceWindow(remote.MaxClockSkew)
	transport := remote.NewBundleTransport(cfg.Git, tmpDir)

	return &Server{
		cfg:        cfg,
		reloaded:   cfg,
		clients:    clients,
		nonces:     nonces,
		transport:  transport,
		audiences:  append([]string(nil), cfg.Audiences...),
		gates:      db.PrefixKV{KV: local, Prefix: "serve."},
		gatesLocal: local,
		stores:     map[string]*store.Store{},
		liveCache:  newLiveCache(),
		nodeLive:   newLiveCache(),

		burstProvider: burstProvider,
		burstOffErr:   burstOffErr,
		burstTable:    burstTable,
		burstHealth:   newBurstHealth(),
	}, nil
}

func (s *Server) DB() *db.DB { return s.cfg.DB }

// live returns the config as of the last successful refresh: one consistent
// copy of every section, taken under s.mu and used without it. Every reader
// of a reloadable section goes through this, never through s.cfg directly:
// cfg is the startup copy and would pin a server to the config it launched
// with. The sections no reload can change are identical in both.
func (s *Server) live() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reloaded
}

// refreshConfig advances the reloadable sections from the machine config. It
// replaces the whole copy in one store, so a tick's reconcile, its census and
// its admissions each see one config and never a partial reload; a nil
// reloader, an unchanged version and a failed load all keep the last good copy
// (see ConfigRefresher.Refresh). The reload itself runs off the lock, so a
// slow database read never blocks a request that only wants the current copy.
func (s *Server) refreshConfig() {
	if s.cfg.Reloader == nil {
		return
	}
	next := s.cfg.Reloader.Refresh(s.live())
	s.mu.Lock()
	s.reloaded = next
	s.mu.Unlock()
}

func ownerIDOf(root string) remote.ClientID {
	id, _ := remote.IDFromDir(filepath.Base(root))
	return id
}

// ownerStore returns the one Store for an owner root, creating it on first use;
// every record call goes through the machine database. It takes s.mu itself
// for the map lookup only: the returned Store is then used without any lock, and
// its own Store.WithLock is what serialises one owner's writes. Nothing that
// holds s.mu may take this path, so the map lock is never nested inside another.
func (s *Server) ownerStore(root string) *store.Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.stores[root]; ok {
		return st
	}
	st := store.NewShared(root, string(ownerIDOf(root)), s.cfg.DB)
	s.stores[root] = st
	return st
}

// sweepTmp removes the stale req-body-* and plan-* files in dir older than
// olderThan, and no other name. A missing dir is empty, not an error.
func sweepTmp(dir string, olderThan time.Duration, now time.Time) (removed int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	var firstErr error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "req-body-") && !strings.HasPrefix(name, "plan-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if now.Sub(info.ModTime()) < olderThan {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}

func (s *Server) ownerRoot(owner remote.ClientID) (string, error) {
	dir, ok := owner.Dir()
	if !ok {
		return "", errors.New("malformed client id")
	}
	return filepath.Join(s.cfg.Root, "bindings", dir), nil
}

func (s *Server) repoRoot(owner remote.ClientID) (string, error) {
	dir, ok := owner.Dir()
	if !ok {
		return "", errors.New("malformed client id")
	}
	return filepath.Join(s.cfg.Root, "repos", dir), nil
}

// insideRoot reports whether p is root itself or under it. It is the create
// path's defence in depth after the join: filepath.Rel is used so a root that
// is a string prefix of a sibling ("/r/a" against "/r/ab/x") is outside, and a
// parent element ("/r/a" against "/r") is refused.
func insideRoot(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// OwnerRuntime resolves owner's runtime for the ui's server source; it holds
// no lock of its own beyond ownerStore's map lookup.
func (s *Server) OwnerRuntime(owner remote.ClientID) (relevo.Runtime, error) {
	root, err := s.ownerRoot(owner)
	if err != nil {
		return relevo.Runtime{}, err
	}
	return s.runtimeAt(root), nil
}

// heldCPUs is the server's cross-owner core census, injected as
// Runtime.HeldCPUs so internal/relevo stays unaware of owners. The caller holds
// this owner's own store lock, and each other owner's List is a lock-free
// database read, so no owner's lock is nested inside another's here. A per-owner
// List error is logged and skipped.
func (s *Server) heldCPUs(root string, tx *store.Tx, self string) ([]int, error) {
	var held []int
	var firstErr error

	bindingsDir := filepath.Join(s.cfg.Root, "bindings")
	entries, err := os.ReadDir(bindingsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	for _, entry := range entries {
		id, ok := remote.IDFromDir(entry.Name())
		if !entry.IsDir() || !ok {
			warnStrayBindingsEntry(entry.Name())
			continue
		}
		ownerPath := filepath.Join(bindingsDir, entry.Name())
		if ownerPath == root {
			bindings, err := tx.List()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			held = append(held, housekeep.HeldIn(bindings, self)...)
			continue
		}
		bindings, err := s.ownerStore(ownerPath).List()
		if err != nil {
			slog.Warn("cpu census: list owner bindings failed", "owner", id, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		held = append(held, housekeep.HeldIn(bindings, "")...)
	}

	return held, firstErr
}
