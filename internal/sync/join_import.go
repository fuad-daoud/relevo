package sync

import (
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// joinImport is the join up to and including its import: open the log, mark the
// join, clear the doc export state so the first export re-emits, and read every
// other origin. It writes nothing to the log, which lets an adoption stop here.
func (e *Enabler) joinImport(transport synclog.LogTransport, now time.Time) (synclog.ImportResult, error) {
	opened, err := transport.Stats()
	if err != nil {
		return synclog.ImportResult{}, fmt.Errorf("sync: enable: open the log: %w", err)
	}
	recordStats(e.Request.Local, &e.last, opened)
	e.recordRemoteKDF(transport)
	_, resumed, err := ReadJoin(e.Request.Local)
	if err != nil {
		return synclog.ImportResult{}, err
	}
	e.resumed = resumed
	if !resumed {
		if err := WriteJoin(e.Request.Local, now); err != nil {
			return synclog.ImportResult{}, err
		}
		// The new peer has seen none of the doc keys this machine exported to
		// another log, so the next export sends every one again; local stamps go
		// too, before the import, so shared keys apply over the seed.
		//
		// The secrets bookkeeping is kept over the clear: the pre-pass read it
		// off the fleet's entries, and an apply of a peer's entry is this
		// machine taking one, not writing one.
		kept, err := readDocSecretsBookkeeping(e.Request.Local)
		if err != nil {
			return synclog.ImportResult{}, err
		}
		kept = kept.fromPeers(e.Request.Shared.Origin())
		if err := e.Request.Local.ClearDocExportState(); err != nil {
			return synclog.ImportResult{}, err
		}
		if err := restoreDocSecretsBookkeeping(e.Request.Local, kept); err != nil {
			return synclog.ImportResult{}, err
		}
	}
	return e.drainImport(transport)
}
