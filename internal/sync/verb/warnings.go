package verb

import (
	"strings"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// enableWarning is what a successful enable still has to say: an unchecked join
// and the import's leftovers.
func enableWarning(res relevosync.EnableResult) string {
	var out []string
	if res.Guarded && !res.Estimate.Known {
		out = append(out, "no machine has published its log size, so the join was not checked against the Turso quota")
	}
	if res.Locked {
		out = append(out, "this machine's log is signed and it holds no key, so its own history was not sent; run relevo db sync unlock to send it")
	}
	return strings.Join(append(out, importWarnings(res.Imported)...), "; ")
}

// importWarnings is what the join's import left undone. The enable still succeeds,
// but the owner hears both: a stalled origin is reread by steady ticks, a dropped
// row is gone for good.
func importWarnings(r synclog.ImportResult) []string {
	var out []string
	for _, s := range r.Stalled {
		out = append(out, s.String())
	}
	for _, d := range r.Dropped {
		out = append(out, d.String())
	}
	return out
}
