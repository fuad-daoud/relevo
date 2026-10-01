package serve

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// formBundlePart returns the create's bundle file part, or nil when the request
// carries none. It mirrors the round form's reader: a zero-length part is
// treated as absent.
func formBundlePart(r *http.Request) (io.Reader, func()) {
	file, _, err := r.FormFile("bundle")
	if err != nil {
		return nil, func() {}
	}
	size, seekErr := file.Seek(0, io.SeekEnd)
	if seekErr == nil && size > 0 {
		if _, err := file.Seek(0, io.SeekStart); err == nil {
			return file, func() { _ = file.Close() }
		}
	}
	_ = file.Close()
	return nil, func() {}
}

// servedChainRequestOf builds the relevo request from the wire create and the
// server's own facts. BuilderActor is left "": this slice's wire carries no
// builder actor, so every chain fixes its builder to the builder actor.
func servedChainRequestOf(caller remote.ClientID, bare string, req remote.CreateChainRequest) relevo.ServedChainRequest {
	out := relevo.ServedChainRequest{
		Name: req.Name, Plans: req.Plans, Settings: req.Settings,
		Feature: req.Feature, Ticket: req.Ticket,
		Owner: string(caller), Bare: bare, RepoID: req.RepoID, Base: req.BaseCommit,
		ClientInstallation: req.ClientInstallation, ClientBindingIDs: req.ClientBindingIDs,
	}
	if req.Author != nil {
		out.AuthorName, out.AuthorEmail = req.Author.Name, req.Author.Email
	}
	return out
}

// writeChainRetry answers a create whose name already names a chain: an
// identical request returns 200 with the chain's current view and creates
// nothing; any difference is 409.
func (s *Server) writeChainRetry(w http.ResponseWriter, rt relevo.Runtime, req remote.CreateChainRequest) {
	view, err := s.servedChainView(rt, req.Name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	if chainRequestMatches(rt, view, req) {
		writeJSON(w, http.StatusOK, view)
		return
	}
	writeErr(w, http.StatusConflict, remote.CodeInvalid, "chain exists")
}

// chainRequestMatches reports whether an existing chain is byte-for-byte the
// request: its base, settings, labels and plan copies. The name and owner are
// already the same, and the repo id is not part of the comparison.
func chainRequestMatches(rt relevo.Runtime, view remote.ChainView, req remote.CreateChainRequest) bool {
	if view.Base != req.BaseCommit || view.Feature != req.Feature || view.Ticket != req.Ticket {
		return false
	}
	if view.Settings != req.Settings {
		return false
	}
	if view.Plans != len(req.Plans) {
		return false
	}
	for i, plan := range req.Plans {
		body, err := rt.Store.ReadFile(rt.Store.ChainPlanPath(req.Name, i+1))
		if err != nil || string(body) != plan {
			return false
		}
	}
	return true
}

// writeChainPreflightError maps a refused chain preflight to its status: a tier
// above max is 422 tier_above_max, an unknown actor or wrong shape is 400, a
// name already taken is 409, and any other pick refusal is 422 invalid.
func writeChainPreflightError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, relevo.ErrTierAboveMax):
		writeErr(w, http.StatusUnprocessableEntity, remote.CodeTierAboveMax, err.Error())
	case errors.Is(err, relevo.ErrUnknownRole):
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
	case strings.Contains(err.Error(), "already exists"):
		writeErr(w, http.StatusConflict, remote.CodeInvalid, err.Error())
	case strings.Contains(err.Error(), "must be a"):
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
	default:
		writeErr(w, http.StatusUnprocessableEntity, remote.CodeInvalid, err.Error())
	}
}

// servedChainView is relevo.ServedChainView with the server's own facts: each
// member's server binding_record id and this server's installation id.
func (s *Server) servedChainView(rt relevo.Runtime, name string) (remote.ChainView, error) {
	recordID := func(member string) string {
		id, err := rt.Store.RecordID(member)
		if err != nil {
			slog.Warn("served chain view record id", "member", member, "err", err)
		}
		return id
	}
	return relevo.ServedChainView(rt, name, recordID, s.cfg.Installation.ID)
}

// chainRuntime resolves the caller's runtime and the path's chain name for a
// chain route; the caller holds s.mu. A name no chain can carry is answered as
// not-found, exactly as loadBinding does for a binding.
func (s *Server) chainRuntime(w http.ResponseWriter, r *http.Request) (relevo.Runtime, string, bool) {
	rt, err := s.runtime(callerOf(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return relevo.Runtime{}, "", false
	}
	name := r.PathValue("name")
	if err := store.ValidName(name); err != nil {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return relevo.Runtime{}, "", false
	}
	return rt, name, true
}

// writeChainReadError maps a chain read or verb failure: a missing chain is
// 404, anything else is 500.
func (s *Server) writeChainReadError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}
	writeErr(w, http.StatusInternalServerError, "", err.Error())
}
