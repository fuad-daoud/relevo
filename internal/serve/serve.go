// Package serve is the remote-builder server: the enrolled-client registry,
// the v1 wire routes over a git-bundle transport, the per-owner binding stores
// and the daemon that ticks them.
package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/installation"
	"github.com/fuad-daoud/relevo/internal/isolate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
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
	// Hooks dispatches lifecycle events; nil means none.
	Hooks hooks.Dispatcher
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
	// required in user mode; os/user.Lookup is the production value, and tests
	// inject a fake so setup stays pure.
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
	// lets a client show a name for the machine. The zero value serves
	// exactly as before, with no installation advertised.
	Installation installation.Installation
	// Audiences is the set of request audiences this server accepts: its
	// certificate fingerprint and every host:<h> a --public-host value names.
	// An empty set refuses every signed request, so production must fill it.
	Audiences []string
}

type Server struct {
	cfg          Config
	clients      *Clients
	nonces       *remote.NonceWindow  // ttl = remote.MaxClockSkew
	transport    remote.TreeTransport // remote.NewBundleTransport(cfg.Git, filepath.Join(cfg.Root, "tmp"))
	addr         net.Addr
	insecureHTTP bool
	// audiences is the accepted audience set, copied from cfg so a later
	// mutation of the caller's slice cannot widen it.
	audiences []string
	// mu guards only the two pieces of process-wide state below: the stores
	// map and the listen flags. It is never held across Store, git or
	// transport I/O -- per-owner exclusion is each owner's own Store lock
	// (see ownerStore), and the server-wide builder cap is admitMu -- so a
	// slow owner's reconcile cannot block another owner's poll.
	mu sync.Mutex
	// gates is a `serve.`-prefixed view of the machine database, so a server-wide
	// gate never collides with this machine's own rows.
	gates db.KV
	// stores is one Store per owner root, guarded by s.mu. The map only ever
	// grows; the *Store values are immutable after creation and are safe to
	// use once read out from under s.mu.
	stores map[string]*store.Store
	// admitMu serializes census-and-admit, so the server-wide builder cap is
	// decided against one running count even when a tick's admit and a
	// round start's admit overlap. collectSettled -> pruneUnusedRepos -> admit
	// still run in that order inside the section they share.
	admitMu sync.Mutex
	// liveCache caches running round LiveViews per caller/binding/round.
	liveCache *liveCache
	// tickFn, when non-nil, replaces Tick in Run; the drain tests set it.
	tickFn func(context.Context) error
}

// EnsureStateRoot creates the state root's bindings directory, the directory
// the doctor's serve/state check probes. MkdirAll also creates the root itself
// and repairs a bindings directory that was removed; it is idempotent and
// returns the raw error, as New does. The root is owner-only: it holds the
// database and every binding's files, and MkdirAll never chmods an existing one.
func EnsureStateRoot(root string) error {
	return os.MkdirAll(filepath.Join(root, "bindings"), store.StateRootMode)
}

// UserModeRoot is where a user-mode server keeps its state when --state is
// unset: a fixed, root-owned directory outside any tenant's home, so the daemon
// and its tenants agree on one location regardless of whose HOME the daemon was
// started with.
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
	// Normalize the unset isolation mode, so every server -- including one
	// whose config predates the key -- emits "isolation":"none".
	if cfg.Isolation == "" {
		cfg.Isolation = isolate.ModeNone
	}
	tmpDir := filepath.Join(cfg.Root, "tmp")
	if err := os.MkdirAll(tmpDir, store.StateRootMode); err != nil {
		return nil, err
	}
	// Before anything listens, drop the request temp files a previous process left
	// behind. A sweep error is a Warn and startup continues.
	if removed, err := sweepTmp(tmpDir, time.Hour, cfg.Now()); err != nil {
		slog.Warn("temp sweep failed", "dir", tmpDir, "removed", removed, "err", err)
	}
	clients, err := LoadClients(cfg.DB)
	if err != nil {
		return nil, err
	}
	nonces := remote.NewNonceWindow(remote.MaxClockSkew)
	transport := remote.NewBundleTransport(cfg.Git, tmpDir)

	return &Server{
		cfg:       cfg,
		clients:   clients,
		nonces:    nonces,
		transport: transport,
		audiences: append([]string(nil), cfg.Audiences...),
		gates:     db.PrefixKV{KV: cfg.DB, Prefix: "serve."},
		stores:    map[string]*store.Store{},
		liveCache: newLiveCache(),
	}, nil
}

func (s *Server) DB() *db.DB { return s.cfg.DB }

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

// OwnerRuntime resolves owner's runtime for the ui's server source. It holds no
// lock of its own beyond ownerStore's map lookup.
func (s *Server) OwnerRuntime(owner remote.ClientID) (relevo.Runtime, error) {
	root, err := s.ownerRoot(owner)
	if err != nil {
		return relevo.Runtime{}, err
	}
	return s.runtimeAt(root), nil
}

// runtime resolves the Runtime for one client id.
func (s *Server) runtime(id remote.ClientID) (relevo.Runtime, error) {
	root, err := s.ownerRoot(id)
	if err != nil {
		return relevo.Runtime{}, err
	}
	return s.runtimeAt(root), nil
}

func (s *Server) runtimeAt(root string) relevo.Runtime {
	st := s.ownerStore(root)
	rt := relevo.Runtime{
		Git:        s.cfg.Git,
		Runner:     s.cfg.Runner,
		Store:      st,
		Transport:  s.transport,
		Candidates: s.cfg.Candidates,
		Accounts:   s.cfg.Accounts,
		Policy:     s.cfg.Policy,
		Gates:      s.gates, // server-wide, not the owner's own
		Usage:      s.cfg.Usage,
		Prices:     s.cfg.Prices,
		Now:        s.cfg.Now,
		StartedAt:  s.cfg.StartedAt,
		Roles:      s.cfg.Roles,
		Registry:   s.cfg.Registry,
		Hooks:      s.cfg.Hooks,
		Scope:      s.cfg.Scope,
		HeldCPUs:   func(tx *store.Tx, self string) ([]int, error) { return s.heldCPUs(root, tx, self) },
		// The reaper is server-wide: deleting an owner's abandoned session
		// runs the harness binary, which is the same for every owner.
		SessionReaper: s.cfg.SessionReaper,
	}
	switch s.cfg.Isolation {
	case isolate.ModeUser:
		s.applyTenant(root, st, &rt)
	case isolate.ModeContainer:
		s.applyContainer(root, &rt)
	}
	return rt
}

// applyContainer rewires rt for one container-mode owner: its runner starts
// every process as a rootless podman container bound to the owner's repo, so a
// round, gate, consult or owner-path git runs confined by the container rather
// than as the serve uid. There is no tenant chown and no tenant root: the
// container runs as the serve uid on the host via keep-id. A runner that is not
// a Boundary is wrapped to refuse every Start, so container mode never falls
// back to the serve uid.
func (s *Server) applyContainer(root string, rt *relevo.Runtime) {
	repo, err := s.repoRoot(ownerIDOf(root))
	if err != nil {
		rt.Runner = isolate.Refuse(s.cfg.Runner, err)
		return
	}
	if b, ok := s.cfg.Runner.(isolate.Boundary); ok {
		rt.Runner = b.ForContainer(isolate.ContainerSpec{Image: s.cfg.IsolationImage, RepoRoot: repo})
		return
	}
	rt.Runner = isolate.Refuse(s.cfg.Runner, fmt.Errorf("serve.isolation=container needs a container boundary runner, got %T", s.cfg.Runner))
}

// applyTenant rewires rt for one user-mode owner: the boundary refuses when the
// tenant could not be resolved, and otherwise every process the owner starts --
// rounds, gates, consults, owner-path git and the session reaper -- runs as the
// tenant, with the tenant's own environment and a per-owner bundle transport.
// A runner that is not a Boundary is wrapped to refuse every Start, so user
// mode never falls back to the serve uid. ensureTenantRoots runs on every
// resolution because the daemon's prunes remove empty tenant directories.
func (s *Server) applyTenant(root string, st *store.Store, rt *relevo.Runtime) {
	owner := ownerIDOf(root)
	t, err := s.tenantFor(owner)
	if b, ok := s.cfg.Runner.(isolate.Boundary); ok {
		rt.Runner = b.ForTenant(t, err)
	} else {
		// User mode runs every builder under a tenant boundary. Production
		// wraps the runner in resolveIsolation, so a runner that is not one is
		// a wiring fault; it fails closed with a boundary-setup error, so a
		// round halts NEEDS YOU rather than running as the serve uid.
		rt.Runner = isolate.Refuse(s.cfg.Runner, fmt.Errorf("serve.isolation=user needs a tenant boundary runner, got %T", s.cfg.Runner))
	}
	if t == nil {
		st.SetTenantChown(nil)
		return
	}
	gc := s.cfg.Git.WithCredential(t.UID, t.GID, tenantEnv(*t))
	rt.Git = gc
	if s.cfg.ReaperFor != nil {
		rt.SessionReaper = s.cfg.ReaperFor(*t)
	}
	rt.Transport = s.ownerTransport(root, *t, gc)
	st.SetTenantChown(func(path string) error {
		return os.Lchown(path, int(t.UID), int(t.GID))
	})
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
			slog.Warn("unexpected entry in bindings dir", "entry", entry.Name())
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
			held = append(held, relevo.HeldIn(bindings, self)...)
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
		held = append(held, relevo.HeldIn(bindings, "")...)
	}

	return held, firstErr
}
