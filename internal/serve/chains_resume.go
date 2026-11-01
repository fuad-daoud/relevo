package serve

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
)

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
		writeErr(w, http.StatusConflict, remote.CodeChainDone, msg)
	case errors.Is(err, resolve.ErrUnknownRole):
		// A --param naming an actor this server's config lacks is the caller's
		// to fix, not a crash.
		writeErr(w, http.StatusBadRequest, remote.CodeUnknownActor, msg)
	default:
		writeErr(w, http.StatusInternalServerError, "", msg)
	}
}

// resumeOptionsFromWire converts the resume body to relevo's options and
// enforces the same bounds a create does. A gate that is present and empty
// clears the check (NoGate); a present non-empty one sets it. Every field that
// was given is also mapped onto the workflow param it fills, so the engine a
// served chain runs reads the wire values through its own params.
func resumeOptionsFromWire(name string, req remote.ChainResumeRequest) (relevo.ResumeOptions, string) {
	opts := relevo.ResumeOptions{Name: name}
	params := map[string]string{}
	if req.MaxCorrections != nil {
		if *req.MaxCorrections < 0 || *req.MaxCorrections > maxChainCorrections {
			return opts, fmt.Sprintf("max_corrections must be 0 to %d", maxChainCorrections)
		}
		opts.MaxCorrections = req.MaxCorrections
		params["max_corrections"] = strconv.Itoa(*req.MaxCorrections)
	}
	if req.Regate != nil {
		if *req.Regate < 0 || *req.Regate > maxChainRegate {
			return opts, fmt.Sprintf("regate must be 0 to %d", maxChainRegate)
		}
		opts.Regate = req.Regate
		params["regate"] = strconv.Itoa(*req.Regate)
	}
	opts.ReviewerActor = req.ReviewerActor
	opts.PlannerActor = req.PlannerActor
	opts.SecurityActor = req.SecurityActor
	opts.Security = req.Security
	for param, value := range map[string]string{
		"reviewer": req.ReviewerActor, "planner": req.PlannerActor, "security": req.SecurityActor,
	} {
		if value != "" {
			params[param] = value
		}
	}
	if req.Security != nil {
		params["scan"] = strconv.FormatBool(*req.Security)
	}
	if req.Gate != nil {
		if *req.Gate == "" {
			opts.NoGate = true
		} else {
			opts.Gate = *req.Gate
		}
		params["gate"] = *req.Gate
	}
	maps.Copy(params, req.Params)
	// From reaches the engine's own resume, which enters that step.
	opts.From = req.From
	if len(params) > 0 {
		opts.Params = params
	}
	return opts, ""
}
