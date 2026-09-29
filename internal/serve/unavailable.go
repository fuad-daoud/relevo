package serve

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

func (s *Server) handleUnavailable(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	caller := callerOf(r)
	rt, err := s.runtime(caller)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}

	name := r.PathValue("name")
	if name != "" {
		b, _, err := s.loadBinding(caller, name)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
				return
			}
			writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
			return
		}
		if !Allowed(caller, "unavailable", b) {
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
			return
		}
	}

	var req remote.UnavailableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
		return
	}
	if req.Token == "" {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "token is required")
		return
	}
	if len(req.Reason) > 512 {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "reason is longer than 512 bytes")
		return
	}

	if _, err := availability.Unavailable(relevo.AvailabilityDeps(rt), req.Token, time.Time{}, req.Reason); err != nil {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{})
}
