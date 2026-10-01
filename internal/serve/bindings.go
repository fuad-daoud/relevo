package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/isolate"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// Allowed reports whether caller may perform verb on binding b. Exported for
// testing and a future grants lookup.
func Allowed(caller remote.ClientID, verb string, b store.Binding) bool {
	if verb == "create" {
		return true
	}
	return b.Owner == string(caller)
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func (s *Server) loadBinding(caller remote.ClientID, name string) (store.Binding, relevo.Runtime, error) {
	if caller == "" {
		return store.Binding{}, relevo.Runtime{}, store.ErrNotFound
	}
	// A name no binding can carry is answered as not-found: every handler maps
	// that to its own 404, and validating first would surface a 500 instead.
	if err := store.ValidName(name); err != nil {
		return store.Binding{}, relevo.Runtime{}, store.ErrNotFound
	}
	rt, err := s.runtime(caller)
	if err != nil {
		return store.Binding{}, relevo.Runtime{}, err
	}
	b, err := rt.Store.Load(name)
	if err != nil {
		return store.Binding{}, relevo.Runtime{}, err
	}
	return b, rt, nil
}

// validAuthor reports whether a wire author is storable: a non-empty name and
// email of at most 256 bytes each, with no newline, carriage return, NUL, < or
// >. The server puts both values in a builder's environment, so anything that
// could forge a line there is refused up front.
func validAuthor(a remote.GitIdentity) bool {
	return validAuthorPart(a.Name) && validAuthorPart(a.Email)
}

func validAuthorPart(s string) bool {
	if len(s) == 0 || len(s) > 256 {
		return false
	}
	return !strings.ContainsAny(s, "\n\r\x00<>")
}

// parseCreateRequest decodes and validates the wire create request; the returned
// message is the 400 to answer, empty when it is well formed.
func parseCreateRequest(r *http.Request) (remote.CreateBindingRequest, string) {
	var req remote.CreateBindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return req, err.Error()
	}
	if err := store.ValidName(req.Name); err != nil {
		return req, err.Error()
	}
	if len(req.RepoID) != 64 || !isHex(req.RepoID) || strings.ToLower(req.RepoID) != req.RepoID {
		return req, "repo_id must be 64 lowercase hex characters"
	}
	if len(req.BaseCommit) != 40 || !isHex(req.BaseCommit) {
		return req, "base_commit must be 40 hex characters"
	}
	if req.Author != nil && !validAuthor(*req.Author) {
		return req, "author: name and email must be 1-256 bytes with no newline, NUL, < or >"
	}
	if req.Feature != "" {
		if err := store.ValidFeature(req.Feature); err != nil {
			return req, err.Error()
		}
	}
	if req.Ticket != "" {
		if err := store.ValidTicket(req.Ticket); err != nil {
			return req, err.Error()
		}
	}
	return req, ""
}

func (s *Server) handleCreateBinding(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	req, bad := parseCreateRequest(r)
	if bad != "" {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, bad)
		return
	}

	caller := callerOf(r)
	rt, err := s.runtime(caller)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}
	if _, err := rt.Store.Load(req.Name); err == nil {
		writeErr(w, http.StatusConflict, remote.CodeInvalid, "binding exists")
		return
	}

	// A user-mode create needs a resolvable tenant before it makes anything: an
	// owner with no declared user, or one that is not on this host, is refused
	// here rather than by a round that halts later.
	if s.cfg.Isolation == isolate.ModeUser {
		if _, err := s.tenantFor(caller); err != nil {
			writeErr(w, http.StatusUnprocessableEntity, remote.CodeInvalid, err.Error())
			return
		}
	}

	b, ok := s.buildServedBinding(w, r.Context(), rt, caller, req)
	if !ok {
		return
	}
	if err := rt.Store.Save(b); err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	if reloaded, err := rt.Store.Load(req.Name); err == nil {
		b = reloaded
	}
	entries, _ := rt.Store.ReadLog(req.Name)
	writeJSON(w, http.StatusCreated, s.servedView(rt, b, entries))
}

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
	return token, kind, string(resolved), true
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

// buildServedBinding resolves the request's role against this server's own
// registry -- the client's roles.json never travels -- creates the bare repo and
// picks the role's candidate and tier. It writes the failure itself and returns
// ok=false, so a refused create leaves no bare repo behind.
func (s *Server) buildServedBinding(w http.ResponseWriter, ctx context.Context, rt relevo.Runtime, caller remote.ClientID, req remote.CreateBindingRequest) (store.Binding, bool) {
	role := relevo.NormRole(req.Role)
	shape, ok := s.servedShape(w, rt, req.Role, req.Gate)
	if !ok {
		return store.Binding{}, false
	}

	repoRoot, err := s.repoRoot(caller)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return store.Binding{}, false
	}
	bare := filepath.Join(repoRoot, req.RepoID+".git")
	if !insideRoot(repoRoot, bare) {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "repo_id escapes the owner's repo root")
		return store.Binding{}, false
	}
	roleName := orText(role, "builder")
	candidateToken, harnessKind, tier, ok := pickServedTier(w, rt, roleName, req.Candidate, req.Tier)
	if !ok {
		return store.Binding{}, false
	}

	if err := rt.Git.InitBare(ctx, bare); err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return store.Binding{}, false
	}

	authorName, authorEmail := "", ""
	if req.Author != nil {
		authorName, authorEmail = req.Author.Name, req.Author.Email
	}

	now, cwd := s.cfg.Now(), rt.Store.WorktreePath(req.Name)
	return store.Binding{
		Name:             req.Name,
		Owner:            string(caller),
		CWD:              cwd,
		Worktree:         cwd,
		Branch:           "relevo/" + req.Name,
		Base:             req.BaseCommit,
		Repo:             bare,
		Builder:          store.Endpoint{Kind: harnessKind, Mode: store.ModeHeadless, AgentName: req.Name},
		BuilderCandidate: candidateToken,
		Link:             servedLink(req),
		Tier:             string(tier),
		Role:             role,
		Shape:            shape,
		Round:            1,
		State:            store.StateActive,
		RoundCap:         req.RoundCap,
		RoundTimeoutMS:   req.RoundTimeoutMS,
		Feature:          req.Feature,
		Ticket:           req.Ticket,
		Gate:             req.Gate,
		Regate:           relevo.ResolveRegate(req.Regate, rt.Policy),
		Serve: &store.ServeFacts{
			RepoID:      req.RepoID,
			BareRepo:    bare,
			LastSeen:    now,
			AuthorName:  authorName,
			AuthorEmail: authorEmail,
		},
	}, true
}

func (s *Server) handleListBindings(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	caller := callerOf(r)
	rt, err := s.runtime(caller)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}

	bindings, err := rt.Store.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	views := make([]remote.BindingView, 0, len(bindings))
	for _, b := range bindings {
		if !Allowed(caller, "list", b) {
			continue
		}
		entries, _ := rt.Store.ReadLog(b.Name)
		views = append(views, s.servedView(rt, b, entries))
	}

	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleGetBinding(w http.ResponseWriter, r *http.Request) {
	caller := callerOf(r)
	name := r.PathValue("name")

	b, rt, view, ok := func() (store.Binding, relevo.Runtime, remote.BindingView, bool) {
		s.mu.Lock()
		defer s.mu.Unlock()

		b, rt, err := s.loadBinding(caller, name)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
				return store.Binding{}, relevo.Runtime{}, remote.BindingView{}, false
			}
			writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
			return store.Binding{}, relevo.Runtime{}, remote.BindingView{}, false
		}
		if !Allowed(caller, "get", b) {
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
			return store.Binding{}, relevo.Runtime{}, remote.BindingView{}, false
		}

		now := s.cfg.Now()
		if b.Serve == nil {
			b.Serve = &store.ServeFacts{}
		}
		b.Serve.LastSeen = now
		_ = rt.Store.Save(b)

		entries, _ := rt.Store.ReadLog(name)
		view := s.servedView(rt, b, entries)
		view.Queue = s.queuePositionView(b, view, caller)
		return b, rt, view, true
	}()
	if !ok {
		return
	}

	if view.RoundState == remote.RoundRunning {
		now := s.cfg.Now()
		cacheKey := string(caller) + "\x00" + name
		if cached, hit := s.liveCache.get(cacheKey, view.Round, now); hit {
			view.Live = cached
		} else {
			live, err := relevo.ServedLive(r.Context(), rt, b)
			if err != nil {
				slog.Warn("served live failed", "name", name, "round", view.Round, "err", err)
			} else {
				s.liveCache.put(cacheKey, view.Round, now, live)
				view.Live = live
			}
		}
	}

	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleDone(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

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
	if !Allowed(caller, "done", b) {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	entries, _ := rt.Store.ReadLog(name)
	switch relevo.RoundStateOf(b, entries) {
	case remote.RoundRunning:
		writeErr(w, http.StatusConflict, remote.CodeRoundOpen, "round is open")
		return
	case remote.RoundQueued:
		writeErr(w, http.StatusConflict, remote.CodeRoundOpen, fmt.Sprintf("round %d is queued; relevo stop to drop it from the queue, or unbind", b.Round))
		return
	}

	_, err = relevo.Done(r.Context(), rt, name)
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "round") && strings.Contains(errStr, "open") {
			writeErr(w, http.StatusConflict, remote.CodeRoundOpen, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		b2, err := tx.Load(name)
		if err != nil {
			return err
		}
		if b2.Serve != nil {
			_, err = settleServed(tx, name, b2.Serve.ClosedRound)
		}
		return err
	}); err != nil {
		slog.Warn("settle served reports failed", "binding", name, "err", err)
	}

	if reloaded, err := rt.Store.Load(name); err == nil {
		b = reloaded
	}
	entries, _ = rt.Store.ReadLog(name)
	writeJSON(w, http.StatusOK, s.servedView(rt, b, entries))
}

func (s *Server) handleUnbind(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

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
	if !Allowed(caller, "unbind", b) {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	res, err := relevo.Unbind(r.Context(), rt, name, true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	if res.WorktreeKept == "" {
		if err := releaseServedRefs(r.Context(), rt, b); err != nil {
			slog.Warn("release served refs", "owner", caller, "binding", b.Name, "err", err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleStop ends the binding's open round, leaving the binding in place: an
// idle or closed one is 409 nothing_to_stop, a halted one 409 round_halted.
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

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
	if !Allowed(caller, "stop", b) {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	entries, _ := rt.Store.ReadLog(name)
	switch relevo.RoundStateOf(b, entries) {
	case remote.RoundIdle, remote.RoundClosed:
		writeErr(w, http.StatusConflict, remote.CodeNothingToStop, relevo.ErrNothingToStop.Error())
		return
	case remote.RoundNeedsYou:
		writeErr(w, http.StatusConflict, remote.CodeRoundHalted, b.Halt)
		return
	}

	if _, err := relevo.Stop(r.Context(), rt, name, relevo.StopOptions{}); err != nil {
		if errors.Is(err, relevo.ErrNothingToStop) {
			writeErr(w, http.StatusConflict, remote.CodeNothingToStop, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	if reloaded, err := rt.Store.Load(name); err == nil {
		b = reloaded
	}
	entries, _ = rt.Store.ReadLog(name)
	writeJSON(w, http.StatusOK, s.servedView(rt, b, entries))
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

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

	if err := rt.Git.CheckoutWorktree(r.Context(), b.Serve.BareRepo, b.Worktree, b.Branch); err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	b.State = store.StateActive
	if err := rt.Store.Save(b); err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	entries, _ := rt.Store.ReadLog(name)
	writeJSON(w, http.StatusOK, s.servedView(rt, b, entries))
}

// handleAvailable lifts the server-wide ledger's rate-limit gate on a subject's
// provider. It is handleUnavailable minus the binding-scoped branch: the ledger
// is server-wide, so there is no /v1/bindings/{name}/available route.
// relevo.Available decides what the subject names.
func (s *Server) handleAvailable(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	caller := callerOf(r)
	rt, err := s.runtime(caller)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}

	var req remote.AvailableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
		return
	}
	if req.Subject == "" {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "subject is required")
		return
	}

	provider, removed, err := availability.Available(relevo.AvailabilityDeps(rt), req.Subject, availability.ClearedByMasterMind)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, remote.CodeInvalid, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, remote.AvailableResponse{Provider: provider, Removed: removed})
}
