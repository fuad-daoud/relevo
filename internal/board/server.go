package board

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// maxBody caps a PUT: 32 MiB.
const maxBody = 32 << 20

// securityHeaders is the policy every response carries. The two relaxations
// have causes: Excalidraw styles inline (style-src 'unsafe-inline') and its
// font subsetter runs as wasm in a worker (script-src 'wasm-unsafe-eval',
// worker-src 'self' blob:). img-src and font-src allow data: and blob: for
// exported images and the in-page font blobs.
const securityHeaders = "default-src 'self'; img-src 'self' data: blob:; " +
	"font-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
	"worker-src 'self' blob:; script-src 'self' 'wasm-unsafe-eval'"

// Token returns a fresh per-run token: 32 random bytes in hex.
func Token() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Server is the board's HTTP surface. Host must be the bound 127.0.0.1:<port>
// and is checked on every request, which is what defends against DNS
// rebinding. Assets defaults to the embedded page tree.
type Server struct {
	Token     string
	ScenePath string
	Theme     *Theme
	Host      string
	Assets    fs.FS
}

// sceneDoc is GET /api/scene's body.
type sceneDoc struct {
	Scene json.RawMessage `json:"scene"`
	Etag  string          `json:"etag"`
	Theme *Theme          `json:"theme"`
	IsNew bool            `json:"isNew"`
}

// putDoc is PUT /api/scene's body.
type putDoc struct {
	Scene json.RawMessage `json:"scene"`
	SVG   string          `json:"svg"`
}

// etagDoc is PUT /api/scene's response.
type etagDoc struct {
	Etag string `json:"etag"`
}

// boardGuard is the request filter both servers wrap their mux in: the Host
// check, the security headers and no-store on /api/. They live here, once, so
// the HTML server cannot drift into a weaker policy than the Excalidraw one --
// csp is the one part that differs, because the two formats need different
// policies, and every other field is identical by construction.
type boardGuard struct {
	host string
	csp  string
}

// wrap applies the guard to a mux. The Host check is what defends against DNS
// rebinding: a rebound name still resolves to this loopback listener, so the
// only thing that separates a real visit from a rebound one is the Host header
// the browser sends.
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

// Handler returns the board's handler: the page at "/", its assets under
// "/assets/", and the one scene at "/api/scene".
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/scene", s.handleScene)
	mux.HandleFunc("/assets/", s.handleAsset)
	mux.HandleFunc("/", s.handleIndex)

	return boardGuard{host: s.Host, csp: securityHeaders}.wrap(mux)
}

// handleScene serves the one scene. Every method requires the token, compared
// in constant time.
func (s *Server) handleScene(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getScene(w)
	case http.MethodPut:
		s.putScene(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// authorized compares the request token against the run's in constant time. It
// is a free function rather than a method so the HTML server shares this one
// comparison instead of writing its own.
func authorizedToken(r *http.Request, token string) bool {
	got := r.Header.Get("X-Relevo-Board-Token")
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// authorized compares the request token against the run's in constant time.
func (s *Server) authorized(r *http.Request) bool {
	return authorizedToken(r, s.Token)
}

// getScene returns the scene, its etag, the theme and whether the file is new.
// A missing file is isNew with a null scene.
func (s *Server) getScene(w http.ResponseWriter) {
	data, etag, isNew, err := Load(s.ScenePath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	scene := json.RawMessage(data)
	if isNew {
		scene = json.RawMessage("null")
	}
	writeJSON(w, sceneDoc{Scene: scene, Etag: etag, Theme: s.Theme, IsNew: isNew})
}

// putScene validates the body, enforces If-Match, then saves the scene and its
// svg atomically.
func (s *Server) putScene(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, maxBody)
	data, err := io.ReadAll(body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	var in putDoc
	if err := json.Unmarshal(data, &in); err != nil {
		http.Error(w, "invalid body", http.StatusUnprocessableEntity)
		return
	}
	if err := ValidateScene(in.Scene); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if err := ValidateSVG([]byte(in.SVG)); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	etag, err := Save(s.ScenePath, in.Scene, []byte(in.SVG), r.Header.Get("If-Match"))
	if errors.Is(err, ErrConflict) {
		http.Error(w, "conflict", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, etagDoc{Etag: etag})
}

// handleIndex serves the page at "/" and under one owner segment
// ("/<owner>"), so a live board's printed URL names the MasterMind that owns
// it. The page itself only ever calls /api and /assets; anything deeper than
// one segment is refused.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if p := strings.Trim(r.URL.Path, "/"); strings.Contains(p, "/") {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(s.assets(), "index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

// handleAsset serves one embedded asset by name, with no token: the page
// cannot hold a token before it loads. Path syntax never reaches the FS.
func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/assets/")
	clean := strings.TrimPrefix(path.Clean("/"+name), "/")
	if clean == "" || clean == "." || strings.Contains(clean, "..") {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(s.assets(), clean)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, clean, time.Time{}, bytes.NewReader(data))
}

// assets is the embedded page tree, defaulting when the server carries none.
func (s *Server) assets() fs.FS {
	if s.Assets != nil {
		return s.Assets
	}
	return Assets()
}

// writeJSON writes one JSON document with its status.
func writeJSON(w http.ResponseWriter, doc any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}
