package main

import (
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// dropChangeCaptureTable removes the dead change-capture table an earlier
// engine left behind, once, after a backup of each file that holds it; the
// VACUUM inside the pass is what returns its pages to the disk. A refusal for
// lack of space or a failure never stops the daemon, and the next start retries.
func dropChangeCaptureTable(rt relevo.Runtime) {
	if rt.DB == nil {
		return
	}
	backupPath, ran, err := db.DropTursoCdcOnce(rt.DB, filepath.Dir(rt.Store.DBPath()), time.Now())
	if err != nil {
		slog.Warn("relevo daemon: change-capture table drop skipped", "err", err)
	} else if ran {
		slog.Info("relevo daemon: change-capture table drop", "backup_path", backupPath)
	}
}
