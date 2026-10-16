package serve

import (
	"errors"
	"net/http"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// resumeSeam parks a resume between the worktree checkout and the
// re-activation save when non-nil. Nil in production; the prune-race tests set
// it to run the tick's prune inside the pin window deterministically.
var resumeSeam func()

// handleResume checks a released binding's worktree back out and re-activates
// its row. The checkout is pinned: a concurrent unbind or GC can archive the
// row while it runs, which drops the bare repo from the prune's live set, so
// the pin spares the repo until the save below has re-activated the row.
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	caller := callerOf(r)
	name := r.PathValue("name")

	b, rt, err := s.loadBinding(caller, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}
	if !Allowed(caller, "resume", b) {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	if b.State != store.StateDone {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "binding is not done")
		return
	}
	if b.Serve == nil || b.Serve.BareRepo == "" {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "missing bare repo facts")
		return
	}

	unpin := pinBareRepo(b.Serve.BareRepo)
	defer unpin()
	if err := rt.Git.CheckoutWorktree(r.Context(), b.Serve.BareRepo, b.Worktree, b.Branch); err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	if resumeSeam != nil {
		resumeSeam()
	}
	b.State = store.StateActive
	if err := rt.Store.Save(b); err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	entries, _ := rt.Store.ReadLog(name)
	writeJSON(w, http.StatusOK, s.servedView(rt, b, entries))
}
