package serve

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// maxCheckBodyBytes bounds a check or gate request body. Both are small JSON
// documents carrying a command and an id, so a body past this is not one the
// caller meant to send.
const maxCheckBodyBytes = 8 << 10

// handleCreateCheck starts one check run on a served binding. It answers 201
// when this call started the run and 200 when the id named a run the binding
// already held, so a client that repeated a request learns from the status that
// nothing was started twice.
func (s *Server) handleCreateCheck(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, rt, name, ok := s.servedRoute(w, r, "check")
	if !ok {
		return
	}

	var req remote.CreateCheckRequest
	if err := decodeBoundedJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
		return
	}

	view, created, err := relevo.ServedCheckStart(r.Context(), rt, name, req)
	if err != nil {
		s.writeCheckErr(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, view)
}

// handleGetCheck reads the run a client started on a served binding. The run
// stays on the binding once it settles, so a client that polls late still reads
// the result of the run it asked for.
func (s *Server) handleGetCheck(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, rt, name, ok := s.servedRoute(w, r, "check")
	if !ok {
		return
	}

	view, err := relevo.ServedCheckGet(rt, name, r.PathValue("id"))
	if err != nil {
		s.writeCheckErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleSetGate writes the acceptance command and repair budget onto a served
// binding. It answers the binding's own view, so the client sees what the
// server stored rather than having to fetch it back.
func (s *Server) handleSetGate(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, rt, name, ok := s.servedRoute(w, r, "gate")
	if !ok {
		return
	}

	var req remote.SetGateRequest
	if err := decodeBoundedJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
		return
	}

	if err := relevo.ServedSetGate(r.Context(), rt, name, req); err != nil {
		s.writeCheckErr(w, err)
		return
	}

	if reloaded, err := rt.Store.Load(name); err == nil {
		b = reloaded
	}
	entries, _ := rt.Store.ReadLog(name)
	writeJSON(w, http.StatusOK, s.servedView(rt, b, entries))
}

// servedRoute loads the binding a check or gate route names, under the same
// tenant guard every other binding route applies, and writes the refusal itself
// when there is none. ok is false once a refusal has been written, so a caller
// never acts on a binding it was not given.
func (s *Server) servedRoute(w http.ResponseWriter, r *http.Request, verb string) (store.Binding, relevo.Runtime, string, bool) {
	caller := callerOf(r)
	name := r.PathValue("name")

	b, rt, err := s.loadBinding(caller, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		} else {
			writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		}
		return store.Binding{}, relevo.Runtime{}, "", false
	}
	if !Allowed(caller, verb, b) {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return store.Binding{}, relevo.Runtime{}, "", false
	}
	return b, rt, name, true
}

// decodeBoundedJSON reads at most maxCheckBodyBytes of r's body into v. The
// bound is on the bytes taken, not on a field, so a body past it is refused
// whole and no part of an oversized request is acted on.
func decodeBoundedJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxCheckBodyBytes)
	return json.NewDecoder(r.Body).Decode(v)
}

// writeCheckErr answers a refused check or gate request. Each refusal gets its
// own code, because the caller acts on it: a binding that is not there, or is
// another tenant's, is a 404 that names nothing; a check already in flight is a
// 409 check_running, so a client waits instead of retrying blind; and
// everything the caller could fix -- a reader, a missing worktree, a request
// that cannot be stored -- is a 400 invalid carrying the reason.
func (s *Server) writeCheckErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, relevo.ErrNoCheck):
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, err.Error())
	case errors.Is(err, relevo.ErrCheckRunning):
		writeErr(w, http.StatusConflict, remote.CodeCheckRunning, err.Error())
	case errors.Is(err, relevo.ErrReaderHasNoCheck),
		errors.Is(err, relevo.ErrNoWorktree),
		errors.Is(err, relevo.ErrInvalidCheck):
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "", err.Error())
	}
}
