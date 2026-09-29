package serve

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/remote"
)

type contextKey string

const callerContextKey = contextKey("callerID")

func callerOf(r *http.Request) remote.ClientID {
	id, _ := r.Context().Value(callerContextKey).(remote.ClientID)
	return id
}

func target(r *http.Request) string {
	t := r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		t += "?" + r.URL.RawQuery
	}
	return t
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil {
			r.Body = http.NoBody
		}
		tmpDir := filepath.Join(s.cfg.Root, "tmp")
		_ = os.MkdirAll(tmpDir, 0o755)
		tmp, err := os.CreateTemp(tmpDir, "req-body-*")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "", err.Error())
			return
		}
		defer func() {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}()

		hasher := sha256.New()
		limited := io.LimitReader(r.Body, s.cfg.MaxBundleBytes+1)
		n, err := io.Copy(io.MultiWriter(tmp, hasher), limited)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "", err.Error())
			return
		}
		if n > s.cfg.MaxBundleBytes {
			writeErr(w, http.StatusRequestEntityTooLarge, remote.CodeTooLarge,
				fmt.Sprintf("request body exceeds %d bytes", s.cfg.MaxBundleBytes))
			return
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			writeErr(w, http.StatusInternalServerError, "", err.Error())
			return
		}
		r.Body = tmp

		sum := hasher.Sum(nil)
		id, err := remote.Verify(r.Header, r.Method, target(r), sum, s.cfg.Now(), s.clients.Lookup, s.nonces)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, remote.CodeOf(err), err.Error())
			return
		}

		ctx := context.WithValue(r.Context(), callerContextKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
