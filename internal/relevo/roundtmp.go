package relevo

import (
	"log/slog"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/store"
)

// roundTmpRel is one round's private temp directory under a runtime's tmp
// root, named rounds/<short>. The short id is deterministic in the binding
// name and round, so a start, a relaunch and every removal route agree on
// the name without anyone carrying it between them -- which is what makes
// removing the directory on a kill safe when the round resumes: the next
// start recreates it under the same name.
func roundTmpRel(name string, round int) string {
	return filepath.Join(tmpSubRounds, shortRoundID(name, round))
}

// removeRoundTmp takes a round's private temp directory away. Every failure
// is logged and swallowed: cleanup never fails the close, the stop, the done
// or the unbind it runs from. A runtime with no tmp root gave its processes no
// directory, so it has nothing to remove; a directory that is already gone is
// success -- the route that just killed the process removed it, and the close
// runs the same hook afterwards.
func removeRoundTmp(rt Runtime, b store.Binding, round int) {
	if rt.TmpRoot == "" {
		return
	}
	if err := rt.Store.RemoveTmpEntry(rt.TmpRoot, roundTmpRel(b.Name, round)); err != nil {
		slog.Warn("round tmp dir not removed", "binding", b.Name, "round", round, "err", err)
	}
}
