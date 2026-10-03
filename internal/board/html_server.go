package board

import (
	"io/fs"
	"net/http"
	"strings"
)

// htmlSecurityHeaders is the HTML board's policy. It is stricter than the
// Excalidraw one in every direction that matters and it is what enforces the
// offline promise: default-src 'none' means nothing loads unless it is named,
// and no name here is a remote origin. Two entries exist for the shell itself:
// script-src and style-src carry 'unsafe-inline' because the shell page is
// hand-written with inline CSS and an inline bootstrap, and frame-src blob: is
// how the board document is rendered. A blob document inherits its creator's
// policy, so the board's own scripts run under exactly this policy, with no
// http and no https reachable from either document.
const htmlSecurityHeaders = "default-src 'none'; " +
	"script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; " +
	"img-src data: blob:; font-src data:; connect-src 'self'; frame-src blob:; " +
	"base-uri 'none'; form-action 'none'"

// HTMLServer is the single-file board's HTTP surface: a shell page that needs
// no token, one token-guarded GET that returns the board's bytes, and the shell
// script. Host must be the bound 127.0.0.1:<port> and is checked on every
// request, exactly as on the Excalidraw server.
type HTMLServer struct {
	Token     string
	BoardPath string
	Scope     Scope
	Name      string
	Host      string
	Shell     fs.FS

	// annotations is the write window for POST /api/annotations. It is unexported
	// because it is not configuration: two servers over one board get two
	// mutexes, which is the same limitation a lock file would not have had.
	annotations annotationLock
}

// shellScripts is the allowlist of shell scripts the server will serve. Each
// entry names a file this package embeds, so the server reads nothing the
// allowlist did not name.
var shellScripts = map[string]bool{
	"shell.js":       true,
	"comments.js":    true,
	"commentlist.js": true,
	"overlay.js":     true,
}

// boardDoc is GET /api/board's body. Html is a string rather than raw JSON
// because the payload is HTML, and null is the honest "no board yet" value.
// Annotations is [] rather than null even when there are none, so the shell
// never has to tell "no notes" from "notes not loaded yet".
type boardDoc struct {
	Html             string       `json:"html"`
	Etag             string       `json:"etag"`
	Annotations      []Annotation `json:"annotations"`
	AnnotationsEtag  string       `json:"annotationsEtag"`
	AnnotationsError string       `json:"annotationsError"`
	IsNew            bool         `json:"isNew"`
	Scope            Scope        `json:"scope"`
	Name             string       `json:"name"`
	External         []string     `json:"external"`
}

// Handler returns the board's handler: the shell at "/" and under one owner
// segment ("/<owner>"), the shell script at "/shell.js", and the one board at
// "/api/board".
func (s *HTMLServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/board", s.handleBoard)
	mux.HandleFunc("/api/annotations", s.handleAnnotations)
	mux.HandleFunc("/shell.js", s.handleShellScript)
	mux.HandleFunc("/comments.js", s.handleShellScript)
	mux.HandleFunc("/commentlist.js", s.handleShellScript)
	mux.HandleFunc("/overlay.js", s.handleShellScript)
	mux.HandleFunc("/", s.handleIndex)

	return boardGuard{host: s.Host, csp: htmlSecurityHeaders}.wrap(mux)
}

// handleBoard serves the one board, GET only. The token is required and
// compared in constant time, and the response is never stored: it carries the
// board's bytes, and a cached copy would outlive the run that produced it.
func (s *HTMLServer) handleBoard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !authorizedToken(r, s.Token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	data, etag, isNew, err := LoadHTML(s.BoardPath)
	if err != nil {
		// An oversized board lands here. It is a 500 naming the cap rather than
		// a partial read, so the shell never renders half a board.
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	annotations, annEtag, annErr := boardAnnotations(s.BoardPath)

	// The shell polls with the etags it already holds. Both matching is the only
	// way to get 204: a new board re-renders the frame and a new annotations
	// file redraws the pins, and each of those needs the whole document.
	if annotationsUnchanged(r, etag, annEtag) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	doc := boardDoc{
		Etag:             etag,
		Annotations:      annotations,
		AnnotationsEtag:  annEtag,
		AnnotationsError: annErr,
		IsNew:            isNew,
		Scope:            s.Scope,
		Name:             s.Name,
		External:         ExternalRefs(data),
	}
	if !isNew {
		doc.Html = string(data)
	}
	writeJSON(w, doc)
}

// handleIndex serves the shell at "/" and under one owner segment, so a live
// board's printed URL names the MasterMind that owns it. The shell needs no
// token: it cannot read the board until the fragment carries one.
func (s *HTMLServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if p := strings.Trim(r.URL.Path, "/"); strings.Contains(p, "/") {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(s.shell(), "index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

// handleShellScript serves the shell's scripts, with no token for the same
// reason the page needs none: they run before the fragment is read. The set is
// a fixed allowlist rather than the request's own path, so a name this server
// does not ship cannot be probed for through it.
func (s *HTMLServer) handleShellScript(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if !shellScripts[name] {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(s.shell(), name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write(data)
}

// shell is the embedded shell page tree, defaulting when the server carries
// none, so a test can substitute its own without a second code path.
func (s *HTMLServer) shell() fs.FS {
	if s.Shell != nil {
		return s.Shell
	}
	return Shell()
}
