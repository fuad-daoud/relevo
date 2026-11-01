package verb

import (
	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// The attempt verb hurries one just-made write to the other machines sooner than
// the export debounce would.

// attempt queues an attempt for a just-made write, bypassing only the export
// debounce; every other rule (sync off, breaker not due, the in-flight slot)
// still applies, so several of these cost the one rerun the slot provides.
// A runner with no trigger answers OK: the write is on disk and signalled, so
// the ordinary triggers send it.
func (v *Runner) attempt() *wire.SyncResult {
	if v.Trigger != nil {
		v.Trigger()
	}
	return &wire.SyncResult{OK: true}
}
