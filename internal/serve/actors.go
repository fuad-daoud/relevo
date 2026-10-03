package serve

import (
	"fmt"
	"net/http"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
)

// handleGetActor answers what a create naming this actor would: the same pick,
// and the same refusal, so a client can ask before it creates anything. The
// actor is normalized exactly as buildServedBinding normalizes a create's role,
// because the probe must answer for the actor the create would resolve.
func (s *Server) handleGetActor(w http.ResponseWriter, r *http.Request) {
	caller := callerOf(r)
	rt, err := s.runtime(caller)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}

	role := relevo.NormRole(r.PathValue("actor"))
	roleName := orText(role, "builder")
	shape, err := relevo.ActorShape(rt, role)
	if err != nil {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, err.Error())
		return
	}

	view := remote.ActorView{
		Actor:      roleName,
		Shape:      shape,
		Candidates: actorCandidates(rt, roleName),
	}

	pin := r.URL.Query().Get("candidate")
	token, _, err := relevo.PickServedCandidateFor(rt, roleName, pin)
	switch {
	case err != nil:
		// The actor refused the token the caller named: report that refusal
		// as the create would, never another candidate's token.
		view.Reason = err.Error()
	case token == "":
		view.Reason = fmt.Sprintf("no candidate serves %s", roleName)
	default:
		view.Accepted = true
		view.Pick = token
	}
	// The ranked list marks the entry a create would serve, so a caller
	// reading the list it chooses from learns which one answers.
	for i := range view.Candidates {
		view.Candidates[i].Pick = view.Accepted && view.Candidates[i].Token == view.Pick
	}

	writeJSON(w, http.StatusOK, view)
}

// actorCandidates renders role's ranked candidates as the wire views the
// candidates route uses, in ranked order. Off entries stay listed: an explicit
// pin may name one, and the list is what the caller chooses from.
func actorCandidates(rt relevo.Runtime, role string) []remote.CandidateView {
	gated := make(map[string]bool)
	for _, g := range availability.Gates(relevo.AvailabilityDeps(rt)) {
		gated[g.Token] = true
	}

	views := []remote.CandidateView{}
	r, ok := rt.RoleRegistry().Role(role)
	if !ok {
		return views
	}
	for _, ranked := range r.Ranked {
		view := remote.CandidateView{Token: ranked.Token, Gated: gated[ranked.Token]}
		if ref, err := candidate.ParseRef(ranked.Token); err == nil {
			view.Kind = ref.Harness
			if rt.Candidates != nil {
				if c, err := rt.Candidates.Lookup(ref); err == nil {
					view.Name, view.Kind = c.Name, c.Harness
				}
			}
		}
		views = append(views, view)
	}
	return views
}
