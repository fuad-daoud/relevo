package db

import (
	"log/slog"
	"os"
)

// logSidecars records a WAL reset: every site that truncates the -wal or
// drops the -shm logs the file's path, the process and engine that did it,
// and both sidecars' sizes, so the next incident names its actor instead of
// leaving the field blank. Sizes are read beside the log, never assumed: -1
// marks a sidecar that is absent, which an empty one must not be confused
// with.
func logSidecars(log *slog.Logger, event, path string) {
	log.Info("wal sidecars",
		"event", event,
		"path", path,
		"pid", os.Getpid(),
		"engine", engineName,
		"wal_bytes", sidecarBytes(path+"-wal"),
		"shm_bytes", sidecarBytes(path+"-shm"),
	)
}

// sidecarBytes is the size of the sidecar at path, or -1 when it is absent.
func sidecarBytes(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return fi.Size()
}
