package board

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// maxBody caps a PUT: 32 MiB.
const maxBody = 32 << 20

// Token returns a fresh per-run token: 32 random bytes in hex.
func Token() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// boardGuard is the request filter the board server wraps its mux in. The
// Host check, the security headers and no-store on /api/ live here once, so
// every route on the server carries the same policy.
type boardGuard struct {
	host string
	csp  string
}

// wrap applies the guard to a mux. The Host check defends against DNS
// rebinding: a rebound name still resolves to this loopback listener, so only
// the Host header separates a real visit from a rebound one.
func (g boardGuard) wrap(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != g.host {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Security-Policy", g.csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	})
}

// authorizedToken compares the request token against the run's in constant
// time.
func authorizedToken(r *http.Request, token string) bool {
	got := r.Header.Get("X-Relevo-Board-Token")
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func writeJSON(w http.ResponseWriter, doc any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}
