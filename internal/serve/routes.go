package serve

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
)

type responseLogger struct {
	http.ResponseWriter
	status int
}

func (r *responseLogger) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code remote.Code, msg string) {
	writeJSON(w, status, remote.ErrorBody{
		Code:    code,
		Message: msg,
	})
}

// orText returns s, or fallback when s is empty.
func orText(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// ownerLabel is the request log's owner field: the caller's label, or "-" when
// the request never authenticated at all -- the zero ClientID an auth failure
// leaves behind.
func ownerLabel(clients *Clients, caller remote.ClientID) string {
	if caller == "" {
		return "-"
	}
	return clients.LabelOf(caller)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/whoami", s.handleWhoAmI)
	mux.HandleFunc("GET /v1/candidates", s.handleCandidates)
	mux.HandleFunc("GET /v1/actors/{actor}", s.handleGetActor)
	mux.HandleFunc("POST /v1/bindings", s.handleCreateBinding)
	mux.HandleFunc("GET /v1/bindings", s.handleListBindings)
	mux.HandleFunc("GET /v1/bindings/{name}", s.handleGetBinding)
	mux.HandleFunc("POST /v1/bindings/{name}/done", s.handleDone)
	mux.HandleFunc("POST /v1/bindings/{name}/unbind", s.handleUnbind)
	mux.HandleFunc("POST /v1/bindings/{name}/stop", s.handleStop)
	mux.HandleFunc("POST /v1/bindings/{name}/resume", s.handleResume)
	mux.HandleFunc("POST /v1/bindings/{name}/rounds", s.handleStartRound)
	mux.HandleFunc("GET /v1/bindings/{name}/rounds/{n}/files/{kind}", s.handleRoundFile)
	mux.HandleFunc("GET /v1/bindings/{name}/rounds/{n}/artifacts", s.handleRoundArtifacts)
	mux.HandleFunc("GET /v1/bindings/{name}/rounds/{n}/artifacts/{rel...}", s.handleRoundArtifact)
	mux.HandleFunc("GET /v1/bindings/{name}/rounds/{n}/bundle", s.handleRoundBundle)
	mux.HandleFunc("POST /v1/bindings/{name}/rounds/{n}/ack", s.handleAckRound)
	mux.HandleFunc("POST /v1/bindings/{name}/unavailable", s.handleUnavailable)
	mux.HandleFunc("POST /v1/unavailable", s.handleUnavailable)
	mux.HandleFunc("POST /v1/available", s.handleAvailable)

	mux.HandleFunc("/v1/", s.handleNotFound)

	authenticatedMux := s.authenticate(mux)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Before anything else: every response says which audience scheme this
		// server speaks, so a client can tell an old server's refusal from a
		// wrong-audience one.
		w.Header().Set(remote.HeaderAuthScheme, remote.AuthSchemeAudience)
		if !strings.HasPrefix(r.URL.Path, "/v1/") && r.URL.Path != "/v1" {
			writeErr(w, http.StatusUpgradeRequired, remote.CodeVersion, "this server speaks v1")
			return
		}

		rw := &responseLogger{ResponseWriter: w, status: http.StatusOK}
		authenticatedMux.ServeHTTP(rw, r)

		owner := ownerLabel(s.clients, callerOf(r))
		attrs := []any{"method", r.Method, "path", r.URL.Path, "owner", owner, "status", rw.status}
		// client_version is informational: an absent header adds no attribute.
		if v := r.Header.Get(remote.HeaderClientVersion); v != "" {
			attrs = append(attrs, "client_version", v)
		}
		slog.Info("http request", attrs...)
	})
}

func (s *Server) handleWhoAmI(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	caller := callerOf(r)
	label := s.clients.LabelOf(caller)
	who := remote.WhoAmI{
		ID:            caller,
		Label:         label,
		ServerVersion: remote.Version,
		Transports:    []string{"git-bundle"},
		// The server's own installation, not the caller's identity above.
		Installation:      s.cfg.Installation.ID,
		InstallationLabel: s.cfg.Installation.Label,
	}
	if rt, err := s.runtime(caller); err == nil {
		who.Features = []string{remote.FeatureTier, remote.FeatureQueue, remote.FeatureStop, remote.FeatureBuilder, remote.FeatureIdempotentSend, remote.FeatureAuthor, remote.FeatureRoles, remote.FeatureLabels, remote.FeatureReaders, remote.FeatureOrigin, remote.FeatureForce, remote.FeaturePlacement, remote.FeatureChainMember}
		who.BuilderTier = string(relevo.ServedBuilderTier(rt))
		who.MaxTier = string(rt.Policy.MaxTierOrDefault())
		c, _ := s.census()
		who.Builders = &remote.BuildersView{Running: c.Running, Queued: len(c.Queued), Cap: s.cap()}
		who.Builders.Scopes = s.cfg.Scope != nil
		if s.cfg.Scope != nil {
			who.Builders.Slice = s.cfg.Scope.Slice
			who.Builders.Quota = s.cfg.Scope.CPUQuota
		}
	}
	writeJSON(w, http.StatusOK, who)
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
}
