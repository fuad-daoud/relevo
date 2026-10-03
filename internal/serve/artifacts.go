package serve

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// resolveReaderRound resolves the binding and the closed reader round the
// artifact routes serve. It writes the refusal itself and ok is false then: the
// checks are handleRoundFile's, plus the shape test -- only a reader binding
// has an artifact directory.
func (s *Server) resolveReaderRound(w http.ResponseWriter, r *http.Request) (store.Binding, relevo.Runtime, int, bool) {
	caller := callerOf(r)
	name := r.PathValue("name")
	b, rt, err := s.loadBinding(caller, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
			return store.Binding{}, relevo.Runtime{}, 0, false
		}
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return store.Binding{}, relevo.Runtime{}, 0, false
	}
	if !Allowed(caller, "files", b) {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return store.Binding{}, relevo.Runtime{}, 0, false
	}
	if b.Shape != store.ShapeReader {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not a reader binding")
		return store.Binding{}, relevo.Runtime{}, 0, false
	}

	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 || n > b.Round {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return store.Binding{}, relevo.Runtime{}, 0, false
	}
	if b.Serve == nil || n > b.Serve.ClosedRound {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, fmt.Sprintf("round %d is not closed", n))
		return store.Binding{}, relevo.Runtime{}, 0, false
	}
	return b, rt, n, true
}

// artifactFiles is relevo's own listing as the wire type.
func artifactFiles(files []relevo.ArtifactFile) []remote.ArtifactFile {
	out := make([]remote.ArtifactFile, 0, len(files))
	for _, f := range files {
		out = append(out, remote.ArtifactFile{Rel: f.Rel, Size: f.Size, MTime: f.MTime})
	}
	return out
}

// handleRoundArtifacts lists a closed reader round's artifact files. The rels
// are PathValue-safe: they come from relevo.RoundArtifacts, which never lists a
// path outside the artifact directory.
func (s *Server) handleRoundArtifacts(w http.ResponseWriter, r *http.Request) {
	b, rt, n, ok := s.resolveReaderRound(w, r)
	if !ok {
		return
	}
	files, err := relevo.RoundArtifacts(rt, b.Name, n, relevo.BindingRole(b), relevo.OutputFile(rt, b))
	if err != nil {
		if errors.Is(err, relevo.ErrNoArtifact) {
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, "no such artifact")
			return
		}
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, remote.ArtifactList{
		Actor:  relevo.BindingRole(b),
		Output: relevo.OutputFile(rt, b),
		Files:  artifactFiles(files),
	})
}

// handleRoundArtifact downloads one listed artifact's bytes. Every rel the
// listing does not hold -- a ".." element, an absolute path, a backslash and an
// empty rel included -- is ErrNoArtifact, so nothing outside the artifact
// directory is ever read.
func (s *Server) handleRoundArtifact(w http.ResponseWriter, r *http.Request) {
	b, rt, n, ok := s.resolveReaderRound(w, r)
	if !ok {
		return
	}
	data, err := relevo.ReadArtifact(rt, b.Name, n, relevo.BindingRole(b), r.PathValue("rel"))
	if err != nil {
		if errors.Is(err, relevo.ErrNoArtifact) {
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, "no such artifact")
			return
		}
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
