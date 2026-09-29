package serve

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// sameSavedPlan reports whether planText is byte-identical to the plan saved for
// round; an unreadable plan is not the same plan.
func sameSavedPlan(rt relevo.Runtime, name string, round int, planText string) bool {
	saved, err := os.ReadFile(rt.Store.PromptPath(name, round))
	if err != nil {
		return false
	}
	return sha256.Sum256([]byte(planText)) == sha256.Sum256(saved)
}

type roundRequest struct {
	Round     int
	Plan      string
	Tier      string
	Candidate string
	Bundle    io.Reader
}

// parseRoundRequest reads the round's form fields; bad is the 400 to answer and
// close releases the bundle part.
func parseRoundRequest(r *http.Request) (req roundRequest, close func(), bad string) {
	close = func() {}
	roundStr := r.FormValue("round")
	if roundStr == "" {
		return req, close, "round is required"
	}
	round, err := strconv.Atoi(roundStr)
	if err != nil || round < 1 {
		return req, close, "invalid round"
	}
	req.Round = round

	req.Plan = r.FormValue("plan")
	if req.Plan == "" {
		return req, close, "plan is required"
	}
	if tier := r.FormValue("tier"); tier != "" {
		if _, err := harness.ParseTier(tier); err != nil {
			return req, close, err.Error()
		}
		req.Tier = tier
	}
	req.Candidate = r.FormValue("candidate")

	file, _, fileErr := r.FormFile("bundle")
	if fileErr != nil {
		return req, close, ""
	}
	close = func() { _ = file.Close() }
	size, seekErr := file.Seek(0, io.SeekEnd)
	if seekErr == nil && size > 0 {
		if _, err := file.Seek(0, io.SeekStart); err == nil {
			req.Bundle = file
		}
	}
	return req, close, ""
}

// roundStartDecision is what the open-round checks say about a start request.
type roundStartDecision int

const (
	startProceed roundStartDecision = iota
	startRetry                      // identical retry: answer 200 with the view
	startOpen                       // the round is running with a different plan
	startStarted                    // another round's plan differs
)

// write answers a non-proceed round start: a retry is the 200 view, an open or
// started round a named 409.
func (d roundStartDecision) write(w http.ResponseWriter, view remote.BindingView, msg string) {
	switch d {
	case startRetry:
		writeJSON(w, http.StatusOK, view)
	case startOpen:
		writeErr(w, http.StatusConflict, remote.CodeRoundOpen, msg)
	case startStarted:
		writeErr(w, http.StatusConflict, remote.CodeRoundStarted, msg)
	}
}

// roundStartDecisionOf classifies a start request against the binding's log: an
// identical retry is a no-op, a different plan for the open round is
// 409 round_open, and a different round's plan is 409 round_started.
func roundStartDecisionOf(rt relevo.Runtime, b store.Binding, entries []store.LogEntry, reqRound int, planText string) (roundStartDecision, string) {
	st := relevo.RoundStateOf(b, entries)
	if (st == remote.RoundRunning || st == remote.RoundQueued) && reqRound == b.Round && sameSavedPlan(rt, b.Name, b.Round, planText) {
		return startRetry, ""
	}
	if st == remote.RoundRunning {
		return startOpen, "round is running"
	}
	if reqRound != b.Round {
		if reqRound == b.Round-1 && sameSavedPlan(rt, b.Name, b.Round-1, planText) {
			return startRetry, ""
		}
		return startStarted, fmt.Sprintf("round %d already started with a different plan", reqRound)
	}
	return startProceed, ""
}

func (s *Server) handleStartRound(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(s.cfg.MaxBundleBytes); err != nil {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "invalid multipart form: "+err.Error())
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	req, closeBundle, bad := parseRoundRequest(r)
	if bad != "" {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, bad)
		return
	}
	defer closeBundle()

	s.mu.Lock()

	caller := callerOf(r)
	name := r.PathValue("name")
	b, rt, err := s.loadBinding(caller, name)
	if err != nil {
		s.mu.Unlock()
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}
	if !Allowed(caller, "rounds", b) {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	entries, _ := rt.Store.ReadLog(name)
	if dec, msg := roundStartDecisionOf(rt, b, entries, req.Round, req.Plan); dec != startProceed {
		s.mu.Unlock()
		dec.write(w, s.servedView(rt, b, entries), msg)
		return
	}

	if b.Serve == nil || b.Serve.BareRepo == "" {
		s.mu.Unlock()
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "missing bare repo facts")
		return
	}

	// A requested builder change is validated before anything moves: a bad
	// token refuses with nothing absorbed and no ref touched. Send re-resolves
	// it under its own lock as a backstop.
	if req.Candidate != "" {
		if _, err := relevo.ResolveSendBuilderFor(rt, relevo.BindingRole(b), b.BuilderCandidate, req.Candidate); err != nil {
			s.mu.Unlock()
			writeErr(w, http.StatusUnprocessableEntity, remote.CodeInvalid, err.Error())
			return
		}
	}

	bare := b.Serve.BareRepo
	outRef := "refs/relevo/" + name + "/out"

	// Absorb OUTSIDE s.mu.
	s.mu.Unlock()

	if !s.absorbRoundBundle(w, r, bare, outRef, req.Bundle) {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishRoundStart(w, r, rt, caller, name, bare, outRef, b, req)
}

func (s *Server) absorbRoundBundle(w http.ResponseWriter, r *http.Request, bare, outRef string, bundle io.Reader) bool {
	if bundle == nil {
		return true
	}
	_, err := s.transport.Absorb(r.Context(), bare, remote.ContentTypeGitBundle, bundle, []string{outRef})
	if err == nil {
		return true
	}
	if errors.Is(err, git.ErrNotFastForward) ||
		errors.Is(err, git.ErrBadBundle) ||
		errors.Is(err, remote.ErrUnexpectedRef) ||
		errors.Is(err, remote.ErrUnsupportedType) {
		writeErr(w, http.StatusUnprocessableEntity, remote.CodeNotFastForward, err.Error())
		return false
	}
	writeErr(w, http.StatusInternalServerError, "", err.Error())
	return false
}

// applyRoundTags sets the client's shipped tags whose commit is already in bare;
// an unrelated tag is skipped. old = "" is unconditional, so a tag the client
// moved moves here too.
func (s *Server) applyRoundTags(ctx context.Context, w http.ResponseWriter, bare, rawTags string) bool {
	if rawTags == "" {
		return true
	}
	var tags []remote.TagRef
	if err := json.Unmarshal([]byte(rawTags), &tags); err != nil {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "tags: "+err.Error())
		return false
	}
	for _, tag := range tags {
		if err := validTagRef(tag); err != nil {
			slog.Debug("tag skipped", "tag", tag.Name, "err", err)
			continue
		}
		if err := s.cfg.Git.UpdateRef(ctx, bare, "refs/tags/"+tag.Name, tag.SHA, ""); err != nil {
			slog.Debug("tag not set", "tag", tag.Name, "err", err)
		}
	}
	return true
}

func (s *Server) syncRoundWorktree(ctx context.Context, w http.ResponseWriter, bare, outRef string, b store.Binding, outSHA string) bool {
	if _, statErr := os.Stat(b.Worktree); os.IsNotExist(statErr) {
		if err := s.cfg.Git.UpdateRef(ctx, bare, "refs/heads/"+b.Branch, outSHA, ""); err != nil {
			writeErr(w, http.StatusInternalServerError, "", err.Error())
			return false
		}
		if err := s.cfg.Git.CheckoutWorktree(ctx, bare, b.Worktree, b.Branch); err != nil {
			writeErr(w, http.StatusInternalServerError, "", err.Error())
			return false
		}
		return true
	}

	err := s.cfg.Git.MergeFF(ctx, b.Worktree, outRef)
	switch {
	case err == nil:
		return true
	case errors.Is(err, git.ErrNotFastForward):
		writeErr(w, http.StatusUnprocessableEntity, remote.CodeNotFastForward, "relevo/"+b.Name+" on the server has moved past your copy")
	case errors.Is(err, git.ErrMergeConflict):
		writeErr(w, http.StatusUnprocessableEntity, remote.CodeNotFastForward, "uncommitted work in the server worktree conflicts with your update")
	default:
		writeErr(w, http.StatusInternalServerError, "", err.Error())
	}
	return false
}

func writePlanTemp(root, planText string) (string, error) {
	f, err := os.CreateTemp(filepath.Join(root, "tmp"), "plan-*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err := f.WriteString(planText); err != nil {
		_ = f.Close()
		return "", err
	}
	_ = f.Close()
	return path, nil
}

// writeSendError maps a Send failure to its response: a halted binding gets 409
// round_halted, anything unrecognised a 500.
func writeSendError(w http.ResponseWriter, rt relevo.Runtime, name string, b store.Binding, sendErr error) {
	switch {
	case errors.Is(sendErr, spawn.ErrRunnerUnavailable):
		writeErr(w, http.StatusServiceUnavailable, remote.CodeNoRunner, sendErr.Error())
	case errors.Is(sendErr, relevo.ErrBuilderBusy), errors.Is(sendErr, relevo.ErrReportPending):
		writeErr(w, http.StatusConflict, remote.CodeRoundOpen, sendErr.Error())
	case errors.Is(sendErr, relevo.ErrTierAboveMax):
		writeErr(w, http.StatusUnprocessableEntity, remote.CodeTierAboveMax, sendErr.Error())
	case errors.Is(sendErr, relevo.ErrBadBuilder):
		writeErr(w, http.StatusUnprocessableEntity, remote.CodeInvalid, sendErr.Error())
	default:
		if reloaded, loadErr := rt.Store.Load(name); loadErr == nil {
			b = reloaded
		}
		if b.State == store.StateNeedsYou || b.Halt != "" {
			slog.Warn("round start failed", "binding", name, "round", b.Round, "err", sendErr, "halt", b.Halt)
			writeErr(w, http.StatusConflict, remote.CodeRoundHalted, orText(b.Halt, sendErr.Error()))
			return
		}
		writeErr(w, http.StatusInternalServerError, "", sendErr.Error())
	}
}

// recordRoundAccepted writes the accept-time census entry and queued hook:
// Send's deferred branch does not know the census.
func (s *Server) recordRoundAccepted(r *http.Request, rt relevo.Runtime, name string, b store.Binding) {
	acceptCensus, censusErr := s.census()
	if censusErr != nil {
		slog.Warn("census failed at round accept", "binding", name, "err", censusErr)
	}
	if err := rt.Store.AppendLog(name, store.LogEntry{
		TS: rt.Now().UTC(), Round: b.Round, Direction: store.DirToMasterMind, Kind: store.KindQueue, Confirmed: true,
		Note: fmt.Sprintf("queued (%d/%d builders busy)", acceptCensus.Running, s.cap()),
	}); err != nil {
		slog.Warn("append queue entry failed", "binding", name, "err", err)
	}
	if s.cfg.Hooks != nil {
		s.cfg.Hooks.Dispatch(r.Context(), hooks.Event{
			Type:      hooks.EventRoundQueued,
			BindingID: name,
			State:     string(b.State),
			Round:     b.Round,
			Timestamp: rt.Now().UTC(),
		})
	}
}

// finishRoundStart runs the locked half of a round start -- ref, worktree,
// send, accept census, admit -- writing the 201 view or the failure itself.
func (s *Server) finishRoundStart(w http.ResponseWriter, r *http.Request, rt relevo.Runtime, caller remote.ClientID, name, bare, outRef string, b store.Binding, req roundRequest) {
	if reloaded, loadErr := rt.Store.Load(name); loadErr == nil {
		b = reloaded
	}

	outSHA, ok, refErr := s.cfg.Git.RefSHA(r.Context(), bare, outRef)
	if refErr != nil {
		writeErr(w, http.StatusInternalServerError, "", refErr.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusUnprocessableEntity, remote.CodeNotFastForward, "no outbound ref; send a bundle first")
		return
	}
	if !s.applyRoundTags(r.Context(), w, bare, r.FormValue("tags")) {
		return
	}
	if !s.syncRoundWorktree(r.Context(), w, bare, outRef, b, outSHA) {
		return
	}

	tmpFilePath, err := writePlanTemp(s.cfg.Root, req.Plan)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	defer func() { _ = os.Remove(tmpFilePath) }()

	if _, sendErr := relevo.Send(r.Context(), rt, name, tmpFilePath, relevo.SendOptions{Tier: req.Tier, Builder: req.Candidate, Defer: true}); sendErr != nil {
		writeSendError(w, rt, name, b, sendErr)
		return
	}

	if reloaded, loadErr := rt.Store.Load(name); loadErr == nil {
		b = reloaded
	}
	s.recordRoundAccepted(r, rt, name, b)
	if err := s.admit(r.Context()); err != nil {
		slog.Warn("admit failed", "binding", name, "err", err)
	}

	if reloaded, loadErr := rt.Store.Load(name); loadErr == nil {
		b = reloaded
	}
	entries, _ := rt.Store.ReadLog(name)
	view := s.servedView(rt, b, entries)
	view.Queue = s.queuePositionView(b, view, caller)
	writeJSON(w, http.StatusCreated, view)
}

// validTagRef reports whether a shipped tag is well formed enough to set as a
// ref. Git tag names may legally contain "/", so this follows git's ref-name
// rules loosely; a failure is skipped by the caller, never a 400.
func validTagRef(tag remote.TagRef) error {
	name := tag.Name
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, " \t\n\r") {
		return fmt.Errorf("invalid tag name %q", name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid tag name %q", name)
		}
	}
	if strings.Contains(name, "@{") || strings.ContainsAny(name, `\~^:?*[`) {
		return fmt.Errorf("invalid tag name %q", name)
	}
	if strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") {
		return fmt.Errorf("invalid tag name %q", name)
	}
	if strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, ".") {
		return fmt.Errorf("invalid tag name %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") {
			return fmt.Errorf("invalid tag name %q", name)
		}
	}
	if len(tag.SHA) != 40 || strings.Trim(tag.SHA, "0123456789abcdefABCDEF") != "" {
		return fmt.Errorf("invalid tag sha %q", tag.SHA)
	}
	return nil
}
