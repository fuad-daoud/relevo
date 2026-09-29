package relevo

import (
	"bytes"
	"errors"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/classify"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
)

// ConfigSource is the database-backed config the daemon reloads and imports
// from (docs/specs/2026-09-24-db-as-record-design.md §4.7). config.Store
// satisfies it; cmd/relevo passes the runtime's own store. Nothing in
// internal/relevo builds a config path itself (CLAUDE.md).
type ConfigSource interface {
	// ImportFiles imports any config file present in dir, the same one-time
	// migration every runtime construction runs.
	ImportFiles(dir string, now time.Time) (config.ImportResult, error)
	// Version is the change counter a reload compares against.
	Version() (int64, error)
	// Load reads every section and both secrets.
	Load() (config.Loaded, error)
}

// ConfigWatcher reloads the candidates, policy, roles and servers sections when
// the config store's version changes. Prices and hooks keep today's
// "loaded once at start" behaviour.
type ConfigWatcher struct {
	source    ConfigSource
	configDir string              // the dir ImportFiles imports from
	getenv    func(string) string // os.Getenv in production

	version  int64  // config version at the last successful load
	lastErr  string // last warning logged; "" after a good load
	loaded   bool   // false until the first successful load through Refresh
	loadedAt time.Time

	// servers and key are what the installed client was built from; both are
	// nil before the first install, and each load is compared against them to
	// decide whether the client has to be rebuilt.
	servers remote.Servers
	key     []byte

	// seams; production values set by NewConfigWatcher, tests replace them
	resolve   func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier
	remoteFor func(remote.Servers, []byte) (RemoteClient, error) // nil leaves rt.Remote untouched
	warn      func(msg string, args ...any)                      // slog.Warn

	// logged is the set of config warning texts already logged this process,
	// so the daemon logs each distinct warning once (#372 §4.4).
	logged map[string]bool
}

// sameServers reports whether two servers sections name the same servers with
// the same entries. Two absent sections and one empty section are the same
// set, so a machine with no servers never builds a client.
func sameServers(a, b remote.Servers) bool {
	if len(a) != len(b) {
		return false
	}
	for name, ea := range a {
		eb, ok := b[name]
		if !ok || eb != ea {
			return false
		}
	}
	return true
}

// NewConfigWatcher returns a watcher over source, importing from configDir.
// Its production seams are classify.Resolve (first return only), the
// NewRemoteClient rule for the servers section and the client key, and
// slog.Warn. It does not read anything yet.
func NewConfigWatcher(source ConfigSource, configDir string, getenv func(string) string) *ConfigWatcher {
	return &ConfigWatcher{
		source:    source,
		configDir: configDir,
		getenv:    getenv,
		resolve: func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
			cls, _ := classify.Resolve(cfg, key, getenv)
			return cls
		},
		remoteFor: NewRemoteClient,
		warn:      slog.Warn,
	}
}

// Refresh imports any pending config file, then returns rt with Candidates,
// Policy, Registry, Classify and ConfigWarnings replaced when the store's
// version differs from the version at the last successful load, or when no
// load has happened yet through this watcher. An unchanged version returns rt
// as given (no load).
//
// A load whose servers section or client key differs from the installed
// client's rebuilds rt.Remote through the construction seam: a successful
// build installs the new client, ErrNoClientKey clears it, and any other build
// error keeps the client already installed. Either way the version advances,
// so a failed build waits for the next change rather than retrying every tick.
//
// Failure: an import error, a version error or a load error keeps rt's current
// values and calls warn once per distinct error string: "config reload failed;
// keeping the copy loaded at HH:MM:SS" -- the time is rt.Now() of the last
// good load, or "startup" when this watcher never loaded.
func (w *ConfigWatcher) Refresh(rt Runtime) Runtime {
	if w.source == nil {
		return rt
	}

	if _, err := w.source.ImportFiles(w.configDir, w.now(rt)); err != nil {
		return w.fail(rt, err)
	}

	v, err := w.source.Version()
	if err != nil {
		return w.fail(rt, err)
	}
	if w.loaded && v == w.version {
		return rt
	}

	L, err := w.source.Load()
	if err != nil {
		return w.fail(rt, err)
	}

	rt.Candidates = L.Candidates
	rt.Policy = L.Policy
	rt.Registry = L.Registry
	rt.ConfigWarnings = L.Warnings
	w.logWarnings(rt.ConfigWarnings)
	if w.resolve != nil {
		rt.Classify = w.resolve(L.Policy.Classify, L.Typesafe, w.getenv)
	}

	if w.remoteFor != nil && (!sameServers(w.servers, L.Servers) || !bytes.Equal(w.key, L.ClientKey)) {
		client, cerr := w.remoteFor(L.Servers, L.ClientKey)
		switch {
		case cerr == nil:
			rt.Remote = client
			w.servers = L.Servers
			w.key = L.ClientKey
		case errors.Is(cerr, ErrNoClientKey):
			rt.Remote = nil
			w.servers = L.Servers
			w.key = L.ClientKey
			if w.warn != nil {
				w.warn(ErrNoClientKey.Error())
			}
		default:
			if w.warn != nil {
				w.warn("config reload: remote client not built; keeping the installed one", "err", cerr)
			}
		}
	}

	w.version = v
	w.loaded = true
	w.lastErr = ""
	w.loadedAt = w.now(rt)
	return rt
}

// now is the reload clock: the runtime's, so tests keep their own time.
func (w *ConfigWatcher) now(rt Runtime) time.Time {
	if rt.Now != nil {
		return rt.Now()
	}
	return time.Now()
}

// logWarnings logs each config warning once per distinct text for this
// process, so an unchanged warning is not repeated on every successful
// reload; a changed set logs the new texts (#372 §4.4).
func (w *ConfigWatcher) logWarnings(warnings []string) {
	for _, msg := range warnings {
		if w.logged[msg] {
			continue
		}
		if w.logged == nil {
			w.logged = map[string]bool{}
		}
		w.logged[msg] = true
		if w.warn != nil {
			w.warn(msg)
		}
	}
}

func (w *ConfigWatcher) fail(rt Runtime, err error) Runtime {
	msg := err.Error()
	if msg != w.lastErr {
		when := "startup"
		if w.loaded {
			when = w.loadedAt.Local().Format("15:04:05")
		}
		if w.warn != nil {
			w.warn("config reload failed; keeping the copy loaded at "+when, "err", err)
		}
		w.lastErr = msg
	}
	return rt
}
