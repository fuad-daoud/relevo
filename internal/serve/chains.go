package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// The bounds a chain create and resume enforce on their untrusted fields, each
// a named constant so a refusal and the plan agree.
const (
	maxChainPlans       = 64
	maxChainPlanBytes   = 1 << 20
	maxChainCorrections = 20
	maxChainRegate      = 10
	// chainMaxNameLen mirrors internal/relevo's unexported cap: a chain name
	// must leave room for its longest member suffix, "-plan".
	chainMaxNameLen = store.MaxAgentNameLen - len("-plan")
)

// parseCreateChainRequest validates every untrusted field of a chain create,
// returning the 400 message or "". It applies parseCreateRequest's rules to the
// fields the two share and adds the chain's own: the plan count, size and
// emptiness, the settings bounds and actor names, and the client binding ids'
// parts.
func parseCreateChainRequest(req remote.CreateChainRequest) string {
	if bad := parseChainCreateShared(req); bad != "" {
		return bad
	}
	if bad := validateChainPlans(req.Plans); bad != "" {
		return bad
	}
	if bad := validateChainSettings(req.Settings); bad != "" {
		return bad
	}
	return validateChainBindingIDs(req.ClientBindingIDs)
}

// parseChainCreateShared checks the fields a chain create and a binding create
// share: the name and its chain-length cap, the repo id, the base commit, the
// author and the two labels.
func parseChainCreateShared(req remote.CreateChainRequest) string {
	if err := store.ValidName(req.Name); err != nil {
		return err.Error()
	}
	if len(req.Name) > chainMaxNameLen {
		return fmt.Sprintf("chain name %q exceeds %d characters (the longest member suffix is -plan)", req.Name, chainMaxNameLen)
	}
	if len(req.RepoID) != 64 || !isHex(req.RepoID) || strings.ToLower(req.RepoID) != req.RepoID {
		return "repo_id must be 64 lowercase hex characters"
	}
	if len(req.BaseCommit) != 40 || !isHex(req.BaseCommit) {
		return "base_commit must be 40 hex characters"
	}
	if req.Author == nil {
		return "author is required"
	}
	if !validAuthor(*req.Author) {
		return "author: name and email must be 1-256 bytes with no newline, NUL, < or >"
	}
	if req.Feature != "" {
		if err := store.ValidFeature(req.Feature); err != nil {
			return err.Error()
		}
	}
	if req.Ticket != "" {
		if err := store.ValidTicket(req.Ticket); err != nil {
			return err.Error()
		}
	}
	return ""
}

// validateChainPlans checks the plan list: 1 to maxChainPlans entries, each
// non-empty after trim and at most maxChainPlanBytes.
func validateChainPlans(plans []string) string {
	if len(plans) < 1 || len(plans) > maxChainPlans {
		return fmt.Sprintf("plans: 1 to %d are required", maxChainPlans)
	}
	for i, plan := range plans {
		if strings.TrimSpace(plan) == "" {
			return fmt.Sprintf("plan %d is empty", i+1)
		}
		if len(plan) > maxChainPlanBytes {
			return fmt.Sprintf("plan %d exceeds %d bytes", i+1, maxChainPlanBytes)
		}
	}
	return ""
}

// validateChainSettings checks the chain's settings: the correction and regate
// bounds, and an actor name for every part a chain runs.
func validateChainSettings(s remote.ChainSettings) string {
	if s.MaxCorrections < 0 || s.MaxCorrections > maxChainCorrections {
		return fmt.Sprintf("max_corrections must be 0 to %d", maxChainCorrections)
	}
	if s.Regate < 0 || s.Regate > maxChainRegate {
		return fmt.Sprintf("regate must be 0 to %d", maxChainRegate)
	}
	if s.ReviewerActor == "" {
		return "settings.reviewer_actor is required"
	}
	if s.PlannerActor == "" {
		return "settings.planner_actor is required"
	}
	if s.Security && s.SecurityActor == "" {
		return "settings.security_actor is required when security is on"
	}
	return ""
}

// validateChainBindingIDs checks that every client link id names one of the
// four chain parts.
func validateChainBindingIDs(ids map[string]string) string {
	for part := range ids {
		switch part {
		case chain.MemberBuilder, chain.MemberReviewer, chain.MemberPlanner, chain.MemberSecurity:
		default:
			return fmt.Sprintf("client_binding_ids: unknown part %q", part)
		}
	}
	return ""
}

// handleCreateChain accepts a whole chain: a multipart POST /v1/chains whose
// "chain" field is the create JSON and whose "bundle" part is the base bundle.
// Validation and the preflight run before anything exists; the bundle is
// absorbed outside s.mu; a second preflight under the lock closes the race with
// a create that landed while the bundle was absorbed.
func (s *Server) handleCreateChain(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(s.cfg.MaxBundleBytes); err != nil {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "invalid multipart form: "+err.Error())
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	raw := r.FormValue("chain")
	if raw == "" {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "chain is required")
		return
	}
	var req remote.CreateChainRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "chain: "+err.Error())
		return
	}
	if bad := parseCreateChainRequest(req); bad != "" {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, bad)
		return
	}
	bundle, closeBundle := formBundlePart(r)
	defer closeBundle()

	caller := callerOf(r)
	repoRoot, err := s.repoRoot(caller)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}
	bare := filepath.Join(repoRoot, req.RepoID+".git")
	if !insideRoot(repoRoot, bare) {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "repo_id escapes the owner's repo root")
		return
	}
	outRef := "refs/relevo/" + req.Name + "/out"

	s.mu.Lock()
	rt, err := s.runtime(caller)
	if err != nil {
		s.mu.Unlock()
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}
	if s.chainPrepareCreate(w, rt, caller, bare, req) {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	// The absorb is the long git step and must not hold s.mu.
	if !s.chainInitAndAbsorb(w, r, bare, outRef, bundle) {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.chainFinishCreate(w, r, rt, caller, bare, outRef, req)
}

// chainPrepareCreate is the create's first locked pass: an existing chain of
// this name is a retry decision, and a fresh name goes through the read-only
// preflight. done is true when it has already written the response.
func (s *Server) chainPrepareCreate(w http.ResponseWriter, rt relevo.Runtime, caller remote.ClientID, bare string, req remote.CreateChainRequest) (done bool) {
	if _, err := rt.Store.Chain(req.Name); err == nil {
		s.writeChainRetry(w, rt, req)
		return true
	} else if !errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return true
	}
	if _, err := relevo.ServedChainPreflight(rt, servedChainRequestOf(caller, bare, req)); err != nil {
		// A chain created since the read above is an identical-retry decision.
		if _, cerr := rt.Store.Chain(req.Name); cerr == nil {
			s.writeChainRetry(w, rt, req)
			return true
		}
		writeChainPreflightError(w, err)
		return true
	}
	return false
}

// chainFinishCreate is the create's locked half, after the bundle was absorbed:
// the second preflight closes the create race, then the branch is cut at the
// base commit, the builder's worktree is checked out, the chain is created and
// the queue is admitted. Any failure past the absorb unwinds the out ref; a
// failure past the checkout also unwinds the worktree and the branch.
func (s *Server) chainFinishCreate(w http.ResponseWriter, r *http.Request, rt relevo.Runtime, caller remote.ClientID, bare, outRef string, req remote.CreateChainRequest) {
	ctx := r.Context()
	name := req.Name
	worktree := rt.Store.WorktreePath(name)
	branch := "relevo/" + name
	unwind := func(hasWorktree, hasBranch bool) {
		s.unwindChainCreate(ctx, bare, name, worktree, hasWorktree, hasBranch)
	}

	if _, err := rt.Store.Chain(name); err == nil {
		s.writeChainRetry(w, rt, req)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		unwind(false, false)
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	plan, err := relevo.ServedChainPreflight(rt, servedChainRequestOf(caller, bare, req))
	if err != nil {
		if _, cerr := rt.Store.Chain(name); cerr == nil {
			s.writeChainRetry(w, rt, req)
			return
		}
		unwind(false, false)
		writeChainPreflightError(w, err)
		return
	}

	if !s.cutChainWorktree(ctx, w, bare, name, outRef, branch, worktree, req.BaseCommit) {
		return
	}

	if _, err := relevo.ServedChainCreate(ctx, rt, plan, worktree); err != nil {
		unwind(true, true)
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	if err := s.admit(ctx); err != nil {
		slog.Warn("admit failed", "chain", name, "err", err)
	}
	view, err := s.servedChainView(rt, name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

// cutChainWorktree is the create's git half: it refuses a bundle whose out ref
// does not resolve to base, refuses an existing branch, cuts the branch at base
// and checks out the builder's worktree. It writes its own failure and returns
// false, unwinding what it made.
func (s *Server) cutChainWorktree(ctx context.Context, w http.ResponseWriter, bare, name, outRef, branch, worktree, base string) bool {
	unwind := func(hasWorktree, hasBranch bool) {
		s.unwindChainCreate(ctx, bare, name, worktree, hasWorktree, hasBranch)
	}
	got, found, err := s.cfg.Git.RefSHA(ctx, bare, outRef)
	if err != nil {
		unwind(false, false)
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return false
	}
	if !found || got != base {
		unwind(false, false)
		writeErr(w, http.StatusUnprocessableEntity, remote.CodeNotFastForward, "the bundle does not carry the base commit")
		return false
	}
	if _, exists, err := s.cfg.Git.RefSHA(ctx, bare, "refs/heads/"+branch); err != nil {
		unwind(false, false)
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return false
	} else if exists {
		unwind(false, false)
		writeErr(w, http.StatusConflict, remote.CodeInvalid, "branch "+branch+" already exists")
		return false
	}
	if err := s.cfg.Git.UpdateRef(ctx, bare, "refs/heads/"+branch, base, ""); err != nil {
		unwind(false, false)
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return false
	}
	if err := s.cfg.Git.CheckoutWorktree(ctx, bare, worktree, branch); err != nil {
		unwind(false, true)
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return false
	}
	return true
}

// chainInitAndAbsorb creates the owner's bare repo, then absorbs the shipped
// base bundle into refs/relevo/<name>/out. It reports the response itself and
// returns false when it wrote a failure.
func (s *Server) chainInitAndAbsorb(w http.ResponseWriter, r *http.Request, bare, outRef string, bundle io.Reader) bool {
	ctx := r.Context()
	if err := s.cfg.Git.InitBare(ctx, bare); err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return false
	}
	if bundle == nil {
		return true
	}
	_, err := s.transport.Absorb(ctx, bare, remote.ContentTypeGitBundle, bundle, []string{outRef})
	if err == nil {
		return true
	}
	if errors.Is(err, git.ErrNotFastForward) || errors.Is(err, git.ErrBadBundle) ||
		errors.Is(err, remote.ErrUnexpectedRef) || errors.Is(err, remote.ErrUnsupportedType) {
		writeErr(w, http.StatusUnprocessableEntity, remote.CodeNotFastForward, err.Error())
		return false
	}
	writeErr(w, http.StatusInternalServerError, "", err.Error())
	return false
}

// unwindChainCreate removes what a refused chain create made: the worktree and
// the branch once they exist, and always the out ref. The bare repo is left for
// pruneUnusedRepos.
func (s *Server) unwindChainCreate(ctx context.Context, bare, name, worktree string, hasWorktree, hasBranch bool) {
	if hasWorktree {
		if err := s.cfg.Git.RemoveWorktree(ctx, bare, worktree, true); err != nil {
			slog.Warn("chain unwind worktree", "chain", name, "err", err)
		}
	}
	if hasBranch {
		if err := s.cfg.Git.DeleteBranch(ctx, bare, "relevo/"+name); err != nil {
			slog.Warn("chain unwind branch", "chain", name, "err", err)
		}
	}
	if err := s.cfg.Git.DeleteRef(ctx, bare, "refs/relevo/"+name+"/out"); err != nil {
		slog.Warn("chain unwind out ref", "chain", name, "err", err)
	}
}

func (s *Server) handleGetChain(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rt, name, ok := s.chainRuntime(w, r)
	if !ok {
		return
	}
	view, err := s.servedChainView(rt, name)
	if err != nil {
		s.writeChainReadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleStopChain(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rt, name, ok := s.chainRuntime(w, r)
	if !ok {
		return
	}
	res, err := relevo.ChainStop(r.Context(), rt, name)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		case errors.Is(err, relevo.ErrNothingToStop):
			writeErr(w, http.StatusConflict, remote.CodeNothingToStop, err.Error())
		default:
			writeErr(w, http.StatusInternalServerError, "", err.Error())
		}
		return
	}
	if err := s.admit(r.Context()); err != nil {
		slog.Warn("admit failed", "chain", name, "err", err)
	}
	view, err := s.servedChainView(rt, name)
	if err != nil {
		s.writeChainReadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, remote.ChainStopResponse{Round: res.Round, Action: res.Action, Chain: view})
}

func (s *Server) handleResumeChain(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rt, name, ok := s.chainRuntime(w, r)
	if !ok {
		return
	}
	c, err := rt.Store.Chain(name)
	if err != nil {
		s.writeChainReadError(w, err)
		return
	}
	switch chain.Status(c.Status) {
	case chain.StatusRunning:
		writeErr(w, http.StatusConflict, remote.CodeChainRunning, "chain is running")
		return
	case chain.StatusDone:
		writeErr(w, http.StatusConflict, remote.CodeInvalid, "chain is done")
		return
	}

	var req remote.ChainResumeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
		return
	}
	opts, bad := resumeOptionsFromWire(name, req)
	if bad != "" {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, bad)
		return
	}
	if _, err := relevo.ChainResume(r.Context(), rt, opts); err != nil {
		writeChainResumeError(w, err)
		return
	}
	if err := s.admit(r.Context()); err != nil {
		slog.Warn("admit failed", "chain", name, "err", err)
	}
	view, err := s.servedChainView(rt, name)
	if err != nil {
		s.writeChainReadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// writeChainResumeError maps a resume failure; the status was checked before the
// call, so these arms are the backstop for a chain that changed under the lock.
func writeChainResumeError(w http.ResponseWriter, err error) {
	msg := err.Error()
	var open *relevo.RoundOpenError
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
	case errors.As(err, &open):
		// The target member's round is open and alive: the client rebuilds the
		// typed refusal so the CLI prints the conflict and its `relevo stop
		// <member>` next line, exactly as a local refusal does.
		writeErr(w, http.StatusConflict, remote.CodeRoundOpen, msg)
	case strings.Contains(msg, "is running"):
		writeErr(w, http.StatusConflict, remote.CodeChainRunning, msg)
	case strings.Contains(msg, "is done"):
		writeErr(w, http.StatusConflict, remote.CodeInvalid, msg)
	default:
		writeErr(w, http.StatusInternalServerError, "", msg)
	}
}

// resumeOptionsFromWire converts the resume body to relevo's options and
// enforces the same bounds a create does. A gate that is present and empty
// clears the check (NoGate); a present non-empty one sets it.
func resumeOptionsFromWire(name string, req remote.ChainResumeRequest) (relevo.ResumeOptions, string) {
	opts := relevo.ResumeOptions{Name: name}
	if req.MaxCorrections != nil {
		if *req.MaxCorrections < 0 || *req.MaxCorrections > maxChainCorrections {
			return opts, fmt.Sprintf("max_corrections must be 0 to %d", maxChainCorrections)
		}
		opts.MaxCorrections = req.MaxCorrections
	}
	if req.Regate != nil {
		if *req.Regate < 0 || *req.Regate > maxChainRegate {
			return opts, fmt.Sprintf("regate must be 0 to %d", maxChainRegate)
		}
		opts.Regate = req.Regate
	}
	opts.ReviewerActor = req.ReviewerActor
	opts.PlannerActor = req.PlannerActor
	opts.SecurityActor = req.SecurityActor
	opts.Security = req.Security
	if req.Gate != nil {
		if *req.Gate == "" {
			opts.NoGate = true
		} else {
			opts.Gate = *req.Gate
		}
	}
	return opts, ""
}

func (s *Server) handleDoneChain(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rt, name, ok := s.chainRuntime(w, r)
	if !ok {
		return
	}
	if _, err := relevo.ChainDone(r.Context(), rt, name); err != nil {
		var open *relevo.RoundOpenError
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		case errors.As(err, &open):
			writeErr(w, http.StatusConflict, remote.CodeRoundOpen, err.Error())
		case errors.Is(err, relevo.ErrChainRunning):
			writeErr(w, http.StatusConflict, remote.CodeChainRunning, err.Error())
		default:
			writeErr(w, http.StatusInternalServerError, "", err.Error())
		}
		return
	}

	// Settle every member's entries up to its closed round, the way handleDone
	// settles a lone binding's.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain(name)
		if err != nil {
			return err
		}
		for _, member := range []string{c.Builder, c.Reviewer, c.Planner, c.Security} {
			if member == "" {
				continue
			}
			b, err := tx.Load(member)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if b.Serve == nil {
				continue
			}
			if _, err := settleServed(tx, member, b.Serve.ClosedRound); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		slog.Warn("settle chain members", "chain", name, "err", err)
	}

	view, err := s.servedChainView(rt, name)
	if err != nil {
		s.writeChainReadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
