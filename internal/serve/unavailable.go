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
	"github.com/fuad-daoud/relevo/internal/sanitize"
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
	// per-account ledger. A bare token records the provider.
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

	// The expiry is the wire's when the sender named one; else the same
	// manual-gate rule the CLIs apply to a hand-recorded reason, so an old
	// head's zero-Until forward of an unparseable limit still bounds at this
	// node, and a forwarded credit reason still holds until cleared. The
	// receive is idempotent: a live entry already holding this gate makes a
	// re-heard forward (a resend after a head restart) record nothing, while
	// a re-gate with a new reason still writes.
	until := req.Until
	if until.IsZero() {
		until = availability.ManualGateUntil(req.Reason, time.Time{}, rt.Now(), rt.Policy.LimitGateDefault())
	}

	deps := relevo.AvailabilityDeps(rt)
	if gateHeard(deps, token, keys, until, req.Reason, rt.Now()) {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	if _, err := availability.Unavailable(deps, token, until, req.Reason, keys...); err != nil {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
		return
	}

	// The ledger is server-wide, so the gate reaches every verified live
	// node the way the head holds it; the node resolves it against its
	// own candidates. The fan-out is detached: the answer never waits for
	// a node and a node failure only logs.
	s.fanoutGateUnavailableAsync(req.Token, req.Reason, until)

	writeJSON(w, http.StatusOK, map[string]any{})
}

// gateHeard reports whether the ledger already holds a live rate-limit gate
// for the same subjects, expiry and note: the same gate re-heard, which
// records nothing. Subjects resolve the way the write resolves them, so the
// check sees the provider the token records as. A re-gate with a new reason
// or expiry still writes.
func gateHeard(deps availability.Deps, subject string, keys []string, until time.Time, reason string, now time.Time) bool {
	l, err := availability.LoadLedger(deps.Gates)
	if err != nil {
		return false
	}
	l = l.Prune(now)
	_, subjects, err := availability.GateProvider(deps.Candidates, l, subject, keys)
	if err != nil {
		return false
	}
	in := make(map[string]bool, len(subjects))
	for _, s := range subjects {
		in[s] = true
	}
	note := sanitize.Text(reason)
	for _, e := range l.Entries {
		if e.Kind != availability.RateLimited || !in[e.Subject] {
			continue
		}
		if e.Until.Equal(until) && e.Note == note {
			return true
		}
	}
	return false
}
