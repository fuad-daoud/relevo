// Package serve is the remote-builder server: the enrolled-client registry,
// the v1 wire routes over a git-bundle transport, the per-owner binding stores
// and the daemon that ticks them.
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

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/hooks"
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
	DB             *db.DB
	Candidates     *candidate.Set
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
	// SessionReaper deletes harness sessions a served round abandoned; nil
	// means the deletes are skipped and the entries stay on the binding.
	SessionReaper relevo.SessionDeleter
}

type Server struct {
	cfg          Config
	clients      *Clients
	nonces       *remote.NonceWindow  // ttl = remote.MaxClockSkew
	transport    remote.TreeTransport // remote.NewBundleTransport(cfg.Git, filepath.Join(cfg.Root, "tmp"))
	addr         net.Addr
	insecureHTTP bool
	mu           sync.Mutex // every store/ledger mutation and every tick
	// gates is a `serve.`-prefixed view of the machine database, so a server-wide
	// gate never collides with this machine's own rows.
	gates db.KV
	// stores is one Store per owner root, guarded by s.mu.
	stores map[string]*store.Store
	// liveCache caches running round LiveViews per caller/binding/round.
	liveCache *liveCache
	// tickFn, when non-nil, replaces Tick in Run; the drain tests set it.
	tickFn func(context.Context) error
}

// EnsureStateRoot creates the state root's bindings directory, the directory
// the doctor's serve/state check probes. MkdirAll also creates the root itself
// and repairs a bindings directory that was removed; it is idempotent and
// returns the raw error, as New does.
func EnsureStateRoot(root string) error {
	return os.MkdirAll(filepath.Join(root, "bindings"), 0o755)
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
	tmpDir := filepath.Join(cfg.Root, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
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
// every record call goes through the machine database. The caller holds s.mu.
func (s *Server) ownerStore(root string) *store.Store {
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

// OwnerRuntime resolves owner's runtime for the ui's server source; it takes
// s.mu itself. A caller holding s.mu calls runtimeAt instead.
func (s *Server) OwnerRuntime(owner remote.ClientID) (relevo.Runtime, error) {
	root, err := s.ownerRoot(owner)
	if err != nil {
		return relevo.Runtime{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtimeAt(root), nil
}

// runtime is OwnerRuntime for a caller that already holds s.mu.
func (s *Server) runtime(id remote.ClientID) (relevo.Runtime, error) {
	root, err := s.ownerRoot(id)
	if err != nil {
		return relevo.Runtime{}, err
	}
	return s.runtimeAt(root), nil
}

func (s *Server) runtimeAt(root string) relevo.Runtime {
	st := s.ownerStore(root)
	return relevo.Runtime{
		Git:        s.cfg.Git,
		Runner:     s.cfg.Runner,
		Store:      st,
		Candidates: s.cfg.Candidates,
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
}

// heldCPUs is the server's cross-owner core census, injected as
// Runtime.HeldCPUs so internal/relevo stays unaware of owners. The caller holds
// s.mu, so taking another owner's store lock is safe: the admin CLI takes one
// owner lock at a time. A per-owner List error is logged and skipped.
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
