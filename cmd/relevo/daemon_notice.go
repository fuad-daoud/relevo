package main

import "github.com/fuad-daoud/relevo/internal/store"

// daemonNotice is the line `relevo status` prints above the rows when the
// running daemon's recorded state needs a human. Pure: every input is an
// argument, so it is table-tested with no store and no daemon.
//
// It returns "" unless the daemon is running and one of two conditions holds:
// the daemon predates version tracking (no daemon.json), or it refused the
// binary it was asked to load. A plain version difference prints nothing,
// because it is transient during a re-exec.
func daemonNotice(cli string, info store.DaemonInfo, ok, running bool) string {
	if !running {
		return ""
	}
	if !ok {
		return "relevo daemon predates version tracking -- restart it once (relevo doctor)"
	}
	if info.ReexecFailed != nil {
		return "relevo daemon refused the new binary -- relevo doctor"
	}
	return ""
}
