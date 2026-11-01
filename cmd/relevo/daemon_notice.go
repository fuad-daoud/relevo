package main

import "github.com/fuad-daoud/relevo/internal/store"

// daemonNotice is the line `relevo status` prints when the running daemon
// needs a human. It returns "" unless the daemon is running and predates
// version tracking -- there is no daemon.json -- or refused the new binary;
// a plain version difference prints nothing, transient during a re-exec.
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
