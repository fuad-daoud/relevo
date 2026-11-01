package serve

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// resumeSeam parks a resume between the worktree checkout and the
// re-activation save when non-nil; nil in production.
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
	if _, err := os.Lstat(b.Worktree); errors.Is(err, os.ErrNotExist) {
		if err := rt.Git.CheckoutWorktree(r.Context(), b.Serve.BareRepo, b.Worktree, b.Branch); err != nil {
			writeErr(w, http.StatusInternalServerError, "", err.Error())
			return
		}
	} else if isMatchingWorktree(r.Context(), rt, b.Worktree, b.Serve.BareRepo, b.Branch) {
	} else {
		msg := fmt.Sprintf("%s already exists but is not a worktree of %s on %s; move the directory aside or relevo unbind", b.Worktree, b.Serve.BareRepo, b.Branch)
		writeErr(w, http.StatusConflict, remote.CodeInvalid, msg)
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

// isMatchingWorktree reports whether dir is an existing worktree of bare
// on the given branch. Any probe error means dir cannot be safely adopted.
func isMatchingWorktree(ctx context.Context, rt relevo.Runtime, path, bare, branch string) bool {
	if rt.Git == nil {
		return false
	}
	prefix, err := rt.Git.ShowPrefix(ctx, path)
	if err != nil || prefix != "" {
		return false
	}
	_, commonDir, err := rt.Git.RepoFacts(ctx, path)
	if err != nil {
		return false
	}
	bareEval, err := filepath.EvalSymlinks(bare)
	if err != nil {
		bareEval = filepath.Clean(bare)
	}
	if commonDir != bareEval {
		return false
	}
	curr, err := rt.Git.CurrentBranch(ctx, path)
	if err != nil {
		return false
	}
	return curr == strings.TrimPrefix(branch, "refs/heads/")
}
