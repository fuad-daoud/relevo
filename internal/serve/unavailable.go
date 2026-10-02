package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// tokenForProvider is a configured candidate token whose provider is provider,
// so an account gate key can be recorded through the Unavailable path, which
// takes a resolvable token beside the keys. ok is false when no configured
// candidate serves provider, which is the server's own refusal.
func tokenForProvider(rt relevo.Runtime, provider string) (string, bool) {
	if rt.Candidates == nil {
		return "", false
	}
	for _, ref := range rt.Candidates.Refs() {
		r, err := candidate.ParseRef(ref)
		if err != nil {
			continue
		}
		if r.Provider == provider {
			return ref, true
		}
	}
	return "", false
}

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

	// A group@account token is an account gate key, not a candidate token: the
	// provider half names the group, and the whole key is what gets recorded,
	// so a server whose client forwards an account gate keeps the same
	// per-account ledger. A bare token records the provider exactly as before.
	token := req.Token
	var keys []string
	if group, name, ok := account.ParseGateKey(req.Token); ok && name != "" {
		byProvider, found := tokenForProvider(rt, group)
		if !found {
			writeErr(w, http.StatusBadRequest, remote.CodeInvalid, fmt.Sprintf("no configured candidate serves provider %q", group))
			return
		}
		token, keys = byProvider, []string{req.Token}
	}

	// The gate expires when the reason names its own reset, by the same parse
	// the daemon applies to builder output: a forwarded
	// "RESOURCE_EXHAUSTED 429: ... Resets in 51m30s" ends at that reset rather
	// than gating the provider until someone clears it by hand. A reason with
	// no parseable reset records the zero Until -- until cleared -- because
	// nothing in it states when the limit lifts.
	until := time.Time{}
	if reset, ok := availability.ResetFromReason(req.Reason, rt.Now()); ok {
		until = reset
	}

	if _, err := availability.Unavailable(relevo.AvailabilityDeps(rt), token, until, req.Reason, keys...); err != nil {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{})
}
