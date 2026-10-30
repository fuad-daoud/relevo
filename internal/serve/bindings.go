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
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/isolate"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/text"
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

// validAuthorPart reports whether one half of a wire author is storable.
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
	return req, validSubdir(req.Subdir)
}

// handleCreateBinding takes no server-wide lock: its own owner's store lock
// and the git work below are the only exclusion it needs, and holding s.mu
// across either would block every other owner's poll. The bare repo the create
// makes is pinned from before InitBare until this handler returns, so the
// tick's prune cannot delete it in the window between the init and the row
// save (the pin's release is deferred, which covers the error paths too).
func (s *Server) handleCreateBinding(w http.ResponseWriter, r *http.Request) {
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
	// A cheap unlocked precheck, so the common duplicate create is refused
	// before any git work. The authoritative check-then-save is the one under
	// the owner's store lock below; that pair is what has to be atomic now that
	// s.mu no longer spans it.
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

	b, unpin, ok := s.buildServedBinding(w, r.Context(), rt, caller, req)
	if !ok {
		return
	}
	if bindCreateSeam != nil {
		bindCreateSeam()
	}
	// The pin releases when this handler returns: after the save below on the
	// success path, and on every error path. The prune must see the pin or the
	// saved row, never neither.
	defer unpin()
	// Check and save under the owner's own store lock, so two creates of one
	// name cannot both pass: one 409s on the row the other just wrote. The lock
	// is per-owner, so this never blocks a different owner's request.
	exists := false
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if _, err := tx.Load(req.Name); err == nil {
			exists = true
			return nil
		}
		return tx.Save(b)
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	if exists {
		writeErr(w, http.StatusConflict, remote.CodeInvalid, "binding exists")
		return
	}
	if reloaded, err := rt.Store.Load(req.Name); err == nil {
		b = reloaded
	}
	entries, _ := rt.Store.ReadLog(req.Name)
	writeJSON(w, http.StatusCreated, s.servedView(rt, b, entries))
}

// bindCreateSeam parks a plain bind between InitBare and the row save when
// non-nil. Nil in production; the prune-race tests set it to run the tick's
// prune inside the pin-to-row-save window deterministically.
var bindCreateSeam func()

// buildServedBinding resolves the request's role against this server's own
// registry -- the client's roles.json never travels -- creates the bare repo
// and picks the role's candidate and tier. It writes the failure itself and
// returns ok=false, so a refused create leaves no bare repo behind. The bare
// repo comes back pinned: the caller releases the pin once the row that
// references it is saved, because between InitBare and that save no row names
// the repo and the tick's prune would otherwise delete it.
func (s *Server) buildServedBinding(w http.ResponseWriter, ctx context.Context, rt relevo.Runtime, caller remote.ClientID, req remote.CreateBindingRequest) (store.Binding, func(), bool) {
	role := resolve.NormRole(req.Role)
	shape, ok := s.servedShape(w, rt, req.Role, req.Gate)
	if !ok {
		return store.Binding{}, nil, false
	}

	repoRoot, err := s.repoRoot(caller)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return store.Binding{}, nil, false
	}
	bare := filepath.Join(repoRoot, req.RepoID+".git")
	if !insideRoot(repoRoot, bare) {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "repo_id escapes the owner's repo root")
		return store.Binding{}, nil, false
	}
	roleName := text.OrText(role, "builder")
	candidateToken, harnessKind, tier, ok := pickServedTier(w, rt, roleName, req.Candidate, req.Tier)
	if !ok {
		return store.Binding{}, nil, false
	}

	// The pin goes on before the bare repo exists: between here and the row
	// save in handleCreateBinding no row references it, and the pin is what
	// keeps the tick's prune from deleting it inside that window.
	unpin := pinBareRepo(bare)
	if err := rt.Git.InitBare(ctx, bare); err != nil {
		unpin()
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return store.Binding{}, nil, false
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
		Subdir:           req.Subdir,
		Worktree:         cwd,
		Branch:           "relevo/" + req.Name,
		Base:             req.BaseCommit,
		Repo:             bare,
		Builder:          store.Endpoint{Kind: harnessKind, Mode: store.ModeHeadless, AgentName: req.Name},
		BuilderCandidate: candidateToken,
		BuilderPinned:    req.Candidate != "",
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
			Trimpath:    req.Trimpath,
		},
	}, unpin, true
}

func (s *Server) handleListBindings(w http.ResponseWriter, r *http.Request) {
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

// lastSeenEvery is how stale a binding's recorded contact may get before a poll
// records it again. A client polls each of its bindings every daemon tick, and
// every save is a binding_record row that sync exports whole and every other
// machine pulls: recording each poll ran the shared database's traffic to
// several gigabytes a day. Its readers -- the admin's last contact and the
// abandoned-binding sweep -- measure in hours and days, so an hour is the
// coarsest a contact may read without misleading either.
const lastSeenEvery = time.Hour

func (s *Server) handleGetBinding(w http.ResponseWriter, r *http.Request) {
	caller := callerOf(r)
	name := r.PathValue("name")

	b, rt, view, ok := func() (store.Binding, relevo.Runtime, remote.BindingView, bool) {
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

		// The LastSeen bump is a read-modify-write, so it runs under this
		// owner's own store lock: a concurrent poll or a tick reconcile of the
		// same owner cannot land a write between the read and the save. The lock
		// is per-owner, so another owner's poll is never blocked by it.
		now := s.cfg.Now()
		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			fresh, err := tx.Load(name)
			if err != nil {
				return err
			}
			if fresh.Serve == nil {
				fresh.Serve = &store.ServeFacts{}
			}
			if now.Sub(fresh.Serve.LastSeen) >= lastSeenEvery {
				fresh.Serve.LastSeen = now
				if err := tx.Save(fresh); err != nil {
					return err
				}
			}
			b = fresh
			return nil
		}); err != nil {
			// The bump is best-effort bookkeeping: the poll still answers from
			// the binding it loaded rather than failing a client's read.
			slog.Warn("record last seen failed", "binding", name, "err", err)
		}

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

	res, err := relevo.Done(r.Context(), rt, name)
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
	view := s.servedView(rt, b, entries)
	view.WorktreeKept = res.WorktreeKept
	view.KeptReason = res.KeptReason
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleUnbind(w http.ResponseWriter, r *http.Request) {
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

	res, err := relevo.UnbindWith(r.Context(), rt, name, true, r.URL.Query().Get("force") != "")
	if errors.Is(err, relevo.ErrWorktreeKept) {
		writeErr(w, http.StatusConflict, remote.CodeWorktreeKept, err.Error())
		return
	}
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
// idle or closed one is 409 nothing_to_stop.
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
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

// handleAvailable lifts the server-wide ledger's rate-limit gate on a subject's
// provider. It is handleUnavailable minus the binding-scoped branch: the ledger
// is server-wide, so there is no /v1/bindings/{name}/available route.
// relevo.Available decides what the subject names.
func (s *Server) handleAvailable(w http.ResponseWriter, r *http.Request) {
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
