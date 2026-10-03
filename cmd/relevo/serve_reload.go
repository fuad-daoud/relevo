package main

import (
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/serve"
)

// serveConfigReloader builds the reload source the running server ticks against:
// the machine database's own config store, labelled for imports and reading the
// same user config dir loadConfig imported at startup, exactly as cmdDaemonRun
// hands rt.Config to the daemon's watcher. So a config file dropped into that
// dir and a `relevo config` edit reach the next tick alike, and an edit stored
// only in the database needs no file at all.
//
// An empty configDir resolves the user config dir here, so the caller that
// already knows it can pass it rather than resolving it twice.
//
// A database whose schema is newer than this build gets no source, the same
// import loadConfig skipped for it; that server keeps the startup copy for its
// life rather than reloading from a database it does not understand.
func serveConfigReloader(d *db.DB, configDir string) (*serve.ConfigRefresher, error) {
	if d.Newer() {
		slog.Warn("relevo.db schema is newer; serve config reload disabled")
		return nil, nil
	}
	if configDir == "" {
		var err error
		if configDir, err = userConfigRoot(); err != nil {
			return nil, err
		}
	}
	dir := filepath.Join(configDir, "relevo")
	source := config.Open(d).As("import", "imported "+dir)
	return serve.NewConfigRefresher(source, dir, time.Now), nil
}
