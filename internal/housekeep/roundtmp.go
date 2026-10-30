package housekeep

import (
	"log/slog"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/store"
)

// RoundTmpRel is one round's private temp directory under a tmp root, named
// rounds/<short>. The short id is deterministic in the binding name and
// round, so a start, a relaunch and every removal route agree on the name
// without anyone carrying it between them -- which is what makes removing
// the directory on a kill safe when the round resumes: the next start
// recreates it under the same name.
func RoundTmpRel(name string, round int) string {
	return filepath.Join(SubRounds, ShortRoundID(name, round))
}

// RemoveRoundTmp takes a round's private temp directory away. Every failure
// is logged and swallowed: cleanup never fails the close, the stop, the done
// or the unbind it runs from. A Deps with no tmp root gave its processes no
// directory, so it has nothing to remove; a directory that is already gone is
// success -- the route that just killed the process removed it, and the close
// runs the same hook afterwards.
func RemoveRoundTmp(deps Deps, b store.Binding, round int) {
	if deps.TmpRoot == "" {
		return
	}
	if err := deps.Store.RemoveTmpEntry(deps.TmpRoot, RoundTmpRel(b.Name, round)); err != nil {
		slog.Warn("round tmp dir not removed", "binding", b.Name, "round", round, "err", err)
	}
}
