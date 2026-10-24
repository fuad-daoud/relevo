package serve

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// pickServedTier resolves role's candidate and tier for a create. It writes the
// failure itself and returns ok=false.
func pickServedTier(w http.ResponseWriter, rt relevo.Runtime, roleName, candidate, explicit string) (token, kind, tier string, ok bool) {
	token, kind, err := relevo.PickServedCandidateFor(rt, roleName, candidate)
	if err != nil {
		// The role refused the pick the tenant named: answer it, never serve someone else.
		writeErr(w, http.StatusUnprocessableEntity, remote.CodeInvalid, err.Error())
		return "", "", "", false
	}
	resolved, err := relevo.ResolveServedTierFor(rt, roleName, token, explicit)
	if err != nil {
		if errors.Is(err, relevo.ErrTierAboveMax) {
			offending := explicit
			format := "tier %s exceeds this server's max_tier %s; raise max_tier in the server's config policy"
			if offending == "" {
				format = "policy tier." + roleName + " %s exceeds max_tier %s"
				offending = string(resolved)
			}
			writeErr(w, http.StatusUnprocessableEntity, remote.CodeTierAboveMax,
				fmt.Sprintf(format, offending, rt.Policy.MaxTierOrDefault()))
			return "", "", "", false
		}
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
		return "", "", "", false
	}
	rendered, err := relevo.RenderServedTier(rt, roleName, token, kind, resolved)
	if err != nil {
		if errors.Is(err, harness.ErrTierUnsupported) || errors.Is(err, harness.ErrExtraArgsPermission) {
			writeErr(w, http.StatusUnprocessableEntity, remote.CodeInvalid, err.Error())
		} else {
			writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
		}
		return "", "", "", false
	}
	return token, kind, string(rendered), true
}

// servedShape resolves a create's role against this server's registry and
// refuses a gate on a reader: a reader round has no check. It writes the
// failure itself and returns ok=false.
func (s *Server) servedShape(w http.ResponseWriter, rt relevo.Runtime, reqRole, gate string) (string, bool) {
	if reqRole == "" {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "actor is required")
		return "", false
	}
	role := relevo.NormRole(reqRole)
	shape := store.ShapeWriter
	if role != "" {
		s, err := relevo.ActorShape(rt, role)
		if err != nil {
			writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
			return "", false
		}
		shape = s
	}
	if shape == store.ShapeReader && gate != "" {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "--gate: a reader round has no check")
		return "", false
	}
	return shape, true
}
