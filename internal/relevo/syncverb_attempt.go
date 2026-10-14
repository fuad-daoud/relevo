package relevo

import (
	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// The attempt verb is what a one-shot reaches the running daemon through when
// it has just written something worth the other machines seeing in seconds: a
// rate limit a person recorded with `relevo gate` is stamped and signalled in
// its own process, and this is how that write leaves sooner than the export
// debounce would have it.

// attempt queues an attempt for the write a client has just made, bypassing
// only the export debounce. Every other rule still applies inside the daemon's
// own trigger -- sync off, a breaker that is not due, and the in-flight slot --
// so a client sending several of these costs the one rerun the slot provides
// rather than an attempt each.
//
// A runner with no trigger installed answers OK rather than refusing: the write
// the client made is on disk and signalled, so the ordinary triggers will send
// it, and a refusal here would read as a failure of a gate that did land.
func (v *VerbRunner) attempt() *wire.SyncResult {
	if v.Trigger != nil {
		v.Trigger()
	}
	return &wire.SyncResult{OK: true}
}
