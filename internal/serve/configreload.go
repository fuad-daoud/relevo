package serve

import (
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// ConfigRefresher reloads the config sections a serve restart does not govern:
// the candidates, the policy, the roles registry and the accounts pool. Every
// other section of a running server -- its listen address, certificate,
// audiences, state root, isolation mode and runner, the scope probe outcome,
// the tick interval and its installation identity -- is decided at startup and
// still needs a restart, as does pricing and the hook dispatcher, which keep
// the daemon watcher's "loaded once at start" behaviour.
//
// A refresher reads through a version counter, so an unchanged config costs one
// version query per tick and no load at all. A nil source -- a server with no
// machine database of its own -- makes every refresh a no-op.
type ConfigRefresher struct {
	source    relevo.ConfigSource // the machine config: a config.Store in production
	configDir string              // the dir ImportFiles imports from; "" imports nothing
	now       func() time.Time

	// version is the config version of the last successful load, loaded says
	// whether one has happened, lastErr is the last warning logged and loadedAt
	// is when it was: together they keep a failing reload quiet and say which
	// copy is still being served.
	version  int64
	loaded   bool
	lastErr  string
	loadedAt time.Time
	// logged is the set of config warning texts already logged this process, so
	// an unchanged warning is not repeated on every successful reload.
	logged map[string]bool

	// warn is slog.Warn in production; tests replace it.
	warn func(msg string, args ...any)
}

// NewConfigRefresher returns a refresher over source, importing any pending
// config file from configDir before each load. An empty configDir imports
// nothing, so a source backed only by stored sections -- every config edit
// applied through `relevo config` rather than a dropped file -- still reloads.
func NewConfigRefresher(source relevo.ConfigSource, configDir string, now func() time.Time) *ConfigRefresher {
	if now == nil {
		now = time.Now
	}
	return &ConfigRefresher{source: source, configDir: configDir, now: now, warn: slog.Warn}
}

// Refresh returns cfg with the candidates, policy, roles registry and accounts
// of the current config when its version differs from the version of the last
// successful load. An unchanged version returns cfg as given, with no load.
//
// Failure: an import error, a version error or a load error returns cfg
// untouched -- the last good copy keeps being served, and admissions are never
// held up or fed a half-loaded config -- and warns once per distinct error:
// "config reload failed; keeping the copy loaded at HH:MM:SS", the time of the
// last good load or "startup" when this refresher never loaded.
func (r *ConfigRefresher) Refresh(cfg Config) Config {
	if r == nil || r.source == nil {
		return cfg
	}
	if r.configDir != "" {
		if _, err := r.source.ImportFiles(r.configDir, r.now()); err != nil {
			return r.fail(cfg, err)
		}
	}

	v, err := r.source.Version()
	if err != nil {
		return r.fail(cfg, err)
	}
	if r.loaded && v == r.version {
		return cfg
	}

	L, err := r.source.Load()
	if err != nil {
		return r.fail(cfg, err)
	}

	cfg.Candidates = L.Candidates
	cfg.Policy = L.Policy
	cfg.Registry = L.Registry
	cfg.Accounts = L.Accounts
	r.logWarnings(L.Warnings)

	r.version = v
	r.loaded = true
	r.lastErr = ""
	r.loadedAt = r.now()
	return cfg
}

// fail warns once per distinct error and keeps serving cfg as it was.
func (r *ConfigRefresher) fail(cfg Config, err error) Config {
	msg := err.Error()
	if msg == r.lastErr {
		return cfg
	}
	when := "startup"
	if r.loaded {
		when = r.loadedAt.Local().Format("15:04:05")
	}
	if r.warn != nil {
		r.warn("config reload failed; keeping the copy loaded at "+when, "err", err)
	}
	r.lastErr = msg
	return cfg
}

// logWarnings logs each config warning once per distinct text for this process,
// so an unchanged warning is not repeated on every successful reload.
func (r *ConfigRefresher) logWarnings(warnings []string) {
	for _, msg := range warnings {
		if r.logged[msg] {
			continue
		}
		if r.logged == nil {
			r.logged = map[string]bool{}
		}
		r.logged[msg] = true
		if r.warn != nil {
			r.warn(msg)
		}
	}
}
