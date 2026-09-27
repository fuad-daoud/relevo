package serve

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

func roundFilePath(rt relevo.Runtime, name string, n int, kind string) (string, bool) {
	switch kind {
	case "report":
		return rt.Store.ReportPath(name, n), true
	case "diff":
		return rt.Store.DiffPath(name, n), true
	case "log":
		return rt.Store.BuilderLogPath(name, n), true
	case "stream":
		return rt.Store.StreamPath(name, n), true
	case "plan":
		return rt.Store.PlanPath(name, n), true
	case "drift":
		return rt.Store.DriftPath(name, n), true
	}
	return "", false
}

// parseLogFrom reads the log's ?from offset, writing the 400 itself on a bad
// value; ok is false then.
func parseLogFrom(r *http.Request, kind string, w http.ResponseWriter) (fromOffset int64, hasFrom, ok bool) {
	if kind != "log" {
		return 0, false, true
	}
	q := r.URL.Query()
	if q.Get("from") == "" && !q.Has("from") {
		return 0, false, true
	}
	parsed, err := strconv.ParseInt(q.Get("from"), 10, 64)
	if err != nil || parsed < 0 {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "invalid from")
		return 0, false, false
	}
	return parsed, true, true
}

// readRoundBytes reads a round file: the log is its NNN-builder.log when one
// exists, otherwise its rendered stream; every other kind comes from disk or the
// sealed row.
func readRoundBytes(rt relevo.Runtime, name string, n int, kind, path string, b store.Binding) ([]byte, bool) {
	if kind != "log" {
		data, err := rt.Store.ReadFile(path)
		return data, err == nil
	}
	read := func(p string) ([]byte, bool, error) {
		d, err := rt.Store.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, false, nil
			}
			return nil, false, err
		}
		return d, true, nil
	}
	text, _, found, err := relevo.RoundTranscript(rt.Store, name, n, b.Builder, read)
	if err != nil || !found {
		return nil, false
	}
	return text, true
}

func writeLogHeaders(w http.ResponseWriter, data []byte, fromOffset int64, hasFrom bool) {
	size := int64(len(data))
	w.Header().Set(remote.HeaderFileSize, strconv.FormatInt(size, 10))
	w.Header().Set(remote.HeaderFileFrom, strconv.FormatInt(fromOffset, 10))
	if hasFrom {
		if fromOffset <= size {
			data = data[fromOffset:]
		} else {
			data = nil
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) handleRoundFile(w http.ResponseWriter, r *http.Request) {
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
	if !Allowed(caller, "files", b) {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 || n > b.Round {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	kind := r.PathValue("kind")
	path, known := roundFilePath(rt, name, n, kind)
	if !known {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "unknown file kind")
		return
	}

	fromOffset, hasFrom, ok := parseLogFrom(r, kind, w)
	if !ok {
		return
	}

	if kind != "log" && kind != "drift" {
		if b.Serve == nil || n > b.Serve.ClosedRound {
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, fmt.Sprintf("round %d is not closed", n))
			return
		}
	}

	data, found := readRoundBytes(rt, name, n, kind, path, b)
	if !found {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "file not found")
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if kind == "log" {
		writeLogHeaders(w, data, fromOffset, hasFrom)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) handleRoundBundle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()

	caller := callerOf(r)
	name := r.PathValue("name")
	b, _, err := s.loadBinding(caller, name)
	if err != nil {
		s.mu.Unlock()
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}
	if !Allowed(caller, "bundle", b) {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || b.Serve == nil || n != b.Serve.ClosedRound {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "round not closed")
		return
	}

	bare := b.Serve.BareRepo
	refs := []string{"refs/heads/" + b.Branch}
	if b.Serve.DirtyCommit != "" {
		refs = append(refs, fmt.Sprintf("refs/relevo/%s/round-%d", name, n))
	}
	since := r.URL.Query().Get("since")

	s.mu.Unlock()

	snap, err := s.transport.Snapshot(r.Context(), bare, refs, since)
	if err != nil {
		if errors.Is(err, remote.ErrSinceUnknown) {
			writeErr(w, http.StatusUnprocessableEntity, remote.CodeNotFastForward, "since is not an ancestor of the result")
			return
		}
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	if snap.Empty {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	defer func() { _ = snap.Body.Close() }()

	w.Header().Set("Content-Type", snap.ContentType)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, snap.Body)
}

func (s *Server) handleAckRound(w http.ResponseWriter, r *http.Request) {
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
	if !Allowed(caller, "ack", b) {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || b.Serve == nil || n > b.Serve.ClosedRound {
		writeErr(w, http.StatusConflict, remote.CodeRoundOpen, fmt.Sprintf("round %d is not closed", n))
		return
	}

	err = rt.Store.WithLock(func(tx *store.Tx) error {
		b2, err := tx.Load(name)
		if err != nil {
			return err
		}
		if b2.Serve != nil && n > b2.Serve.AckedRound {
			b2.Serve.AckedRound = n
		}
		if err := tx.Save(b2); err != nil {
			return err
		}
		_, err = settleServed(tx, name, n)
		return err
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	if reloaded, err := rt.Store.Load(name); err == nil {
		b = reloaded
	}
	entries, _ := rt.Store.ReadLog(name)
	writeJSON(w, http.StatusOK, relevo.ServedView(b, entries))
}
