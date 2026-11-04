package serve

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

var errAlignOpen = errors.New("binding holds an open round")

// handleAlignRound moves a binding's round count up to n, the number the
// caller's next start will carry. A burst head numbers the rounds and sends
// only some of them here, so a fresh node binding counts from 1 while the head
// may be on round 5, and a start for that round would be refused. A count at
// or past n stays where it is, so a retried send changes nothing.
//
// A round closed before n is settled here: the caller has moved past it, and
// left unacked it would keep answering for the binding over the new round.
func (s *Server) handleAlignRound(w http.ResponseWriter, r *http.Request) {
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
	if !Allowed(caller, "align", b) {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "invalid round")
		return
	}

	unlock := lockOwnerSection(caller)
	defer unlock()
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		return alignRound(tx, name, n)
	})
	if errors.Is(err, errAlignOpen) {
		writeErr(w, http.StatusConflict, remote.CodeRoundOpen, fmt.Sprintf("cannot move to round %d: %v", n, err))
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	if reloaded, err := rt.Store.Load(name); err == nil {
		b = reloaded
	}
	entries, _ := rt.Store.ReadLog(name)
	writeJSON(w, http.StatusOK, s.servedView(rt, b, entries))
}

func alignRound(tx *store.Tx, name string, n int) error {
	b, err := tx.Load(name)
	if err != nil {
		return err
	}
	if n <= b.Round {
		return nil
	}
	entries, err := tx.ReadLog(name)
	if err != nil {
		return err
	}
	switch remote.RoundStateOf(b, entries) {
	case remote.RoundIdle, remote.RoundClosed:
	default:
		return errAlignOpen
	}
	b.Round = n
	settled := 0
	if b.Serve != nil && b.Serve.ClosedRound > b.Serve.AckedRound {
		settled = b.Serve.ClosedRound
		b.Serve.AckedRound = settled
	}
	if err := tx.Save(b); err != nil {
		return err
	}
	if settled == 0 {
		return nil
	}
	_, err = settleServed(tx, name, settled)
	return err
}
