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
// the machine database's own config store, reading the same user config dir
// loadConfig imported at startup. An empty configDir resolves the user config dir
// here, so a caller that already knows it passes it rather than resolving twice.
// A database newer than this build gets no source, the same import loadConfig
// skipped for it.
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
