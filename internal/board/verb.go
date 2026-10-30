package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

// Listen binds the loopback listener a board serves on.
func Listen() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }

// Open opens the printed URL in a browser.
func Open(url string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	return exec.Command(opener, url).Start()
}

// RepoRoot is cwd's git top level, the root scenes are confined to.
func RepoRoot(cwd string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// GitConfig reads one repo-local git config key; an unset key is
// ("", nil), the same fact git reports with exit 1.
func GitConfig(cwd, key string) (string, error) {
	cmd := exec.Command("git", "config", "--get", key)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Options is what one run of the verb was asked for. scene and slug carry
// the resolved board twice over: scene is the path the server reads, and slug is
// the scene name the advertisement carries. They are separate because the scene
// name is the slug -- a board.html path's base name would be "board" for every
// board in both scopes -- and deriving it from the file name is what made every
// HTML board advertise the same scene.
type Options struct {
	Scene   string
	Theme   *Theme
	NoOpen  bool
	LiveDir string
	// Owner names the MasterMind a live board belongs to (its name, else its
	// id); empty for the repo scope. It only shapes the printed URL.
	Owner string
	// Slug is the resolved scene name, the one server.json advertises.
	Slug string
	// Scope tells the HTML server which scope it is serving.
	Scope Scope
}

// Advertise composes the advertisement a live board writes after its
// listener binds; a repo board -- liveDir empty -- writes none (S6). url is the
// printed board URL (host and per-run token), from which the port is read.
func Advertise(scene, url string, pid int, startedAt int64, liveDir string) *ServerInfo {
	if liveDir == "" {
		return nil
	}
	port := 0
	if u, err := neturl.Parse(url); err == nil {
		port, _ = strconv.Atoi(u.Port())
	}
	return &ServerInfo{Scene: scene, URL: url, Port: port, PID: pid, StartedAt: startedAt}
}

// URL prints the live board's URL for a shell copy when the statusline
// is not at hand (S9). It is read-only: no pointer write, no database write, no
// listener. The scene is boardName, else the pointer -- never the default and
// never written. Without a live server it is ErrNotAvailable, printing
// nothing on stdout and one line naming the scene and the MasterMind.
func URL(deps Deps, ref, boardName string) error {
	id, _, err := Mastermind(deps, ref)
	if err != nil {
		return err
	}
	liveRoot, err := deps.LiveRoot()
	if err != nil {
		return err
	}
	liveDir := filepath.Join(liveRoot, id)

	scene := boardName
	if scene != "" {
		if err := ValidSceneName(scene); err != nil {
			return err
		}
	} else {
		name, present, err := Pointer(liveDir)
		if err != nil {
			return err
		}
		if !present {
			return &notAvailableError{scene: scene, id: id}
		}
		scene = name
	}

	url, ok, err := LiveURL(liveDir, scene, deps.ProcStart)
	if err != nil {
		return err
	}
	if !ok {
		return &notAvailableError{scene: scene, id: id}
	}
	fmt.Println(url)
	return nil
}

// Run parses the verb, resolves the scene and scope, writes the pointer
// for a live board that was not read from the pointer, and then runs the
// foreground server. sub is the subverb name, or "" for the default flow;
// extra is the subverb's own arguments after it. A scene path is what cannot
// collide with one of them, and now it names a format rather than a scope: an
// explicit .excalidraw path keeps the Excalidraw flow and everything else is a
// single-file board, whose path must end in /board.html. So a bare slug is not
// a path in either format and the collision cannot arise.
func Run(deps Deps, cwd, ref, themeFlag, boardName, arg string, noOpen bool, shell fs.FS) error {
	// The theme is resolved before anything else so an unknown theme flag is a
	// usage refusal naming the stored palettes regardless of which format the path
	// selects. Only the Excalidraw flow consumes it: a single-file board brings
	// its own colours.
	theme, err := ResolveTheme(deps, cwd, themeFlag)
	if err != nil {
		return err
	}

	// An explicit Excalidraw path is the old flow, unchanged: the scene keeps
	// its own verbs and its own server.
	if strings.HasSuffix(arg, SceneExt()) {
		return runExcalidrawBoard(deps, cwd, ref, boardName, arg, noOpen, theme)
	}
	return runSingleFileBoard(deps, cwd, ref, boardName, arg, noOpen, shell)
}

// runExcalidrawBoard is the legacy flow: an explicit .excalidraw path resolved
// and served by the Excalidraw server, with the theme.
func runExcalidrawBoard(deps Deps, cwd, ref, boardName, arg string, noOpen bool, theme *Theme) error {
	res, err := ResolveScene(deps, cwd, ref, boardName, arg)
	if err != nil {
		return err
	}
	opts := Options{
		Scene:   res.Path,
		Slug:    res.Scene,
		Scope:   res.Scope,
		Theme:   theme,
		NoOpen:  noOpen,
		LiveDir: liveDirOf(res),
		Owner:   res.Owner,
	}
	return Serve(deps, opts)
}

// runSingleFileBoard is the default flow: a live <id>/<slug>/board.html, a repo
// <slug>/board.html under the git top level, or a new board that does not exist
// yet. The pointer is written for a live board that was not read from the pointer
// and never for a repo board, so the pointer always names the board the user is
// looking at.
func runSingleFileBoard(deps Deps, cwd, ref, boardName, arg string, noOpen bool, shell fs.FS) error {
	res, err := ResolveSceneHTML(deps, cwd, ref, boardName, arg)
	if err != nil {
		return err
	}
	if res.Scope == ScopeLive && !res.FromPointer {
		if err := WritePointer(res.LiveDir, res.Scene); err != nil {
			return err
		}
	}
	opts := Options{
		Scene:   res.Path,
		Slug:    res.Scene,
		Scope:   res.Scope,
		NoOpen:  noOpen,
		LiveDir: liveDirOf(res),
		Owner:   res.Owner,
	}
	return ServeHTML(deps, opts, shell)
}

// liveDirOf is the live directory a resolved board writes its advertisement and
// pointer into, empty for the repo scope.
func liveDirOf(res Resolved) string {
	if res.Scope != ScopeLive {
		return ""
	}
	return res.LiveDir
}

// agentPath resolves a subverb's scene path: the working directory, the
// repository root and the confined scene path. A path outside a repository is
// usage.
func agentPath(deps Deps, cwd, arg string) (string, string, error) {
	root, err := deps.RepoRoot(cwd)
	if err != nil {
		return "", "", usagef("not inside a git repository: %v", err)
	}
	path, err := Resolve(root, cwd, arg)
	if err != nil {
		return "", "", err
	}
	return cwd, path, nil
}

// Text lists a scene's text elements: one JSON document with asJSON,
// one tabwriter row each otherwise.
func Text(deps Deps, cwd, arg string, asJSON bool) error {
	_, path, err := agentPath(deps, cwd, arg)
	if err != nil {
		return err
	}
	elements, err := TextElements(path)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(elements)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, e := range elements {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.ID,
			strconv.FormatFloat(e.X, 'f', -1, 64),
			strconv.FormatFloat(e.Y, 'f', -1, 64),
			e.Text)
	}
	return tw.Flush()
}

// AnnotateRequest is what one `board annotate` asks for: the text to append,
// whether --text/--x/--y were given, and the coordinates. Every usage check
// runs before any repository, theme or file work.
type AnnotateRequest struct {
	Text       string
	HasText    bool
	HasX, HasY bool
	X, Y       float64
}

// AnnotateScene appends one text element to a scene and prints its id.
func AnnotateScene(deps Deps, cwd, arg string, req AnnotateRequest) error {
	if !req.HasText {
		return usagef("--text is required")
	}
	if req.Text == "" {
		return usagef("--text must not be empty")
	}
	if req.HasX != req.HasY {
		return usagef("--x and --y come together")
	}
	if req.HasX && (math.IsNaN(req.X) || math.IsInf(req.X, 0)) {
		return usagef("--x must be a finite number")
	}
	if req.HasY && (math.IsNaN(req.Y) || math.IsInf(req.Y, 0)) {
		return usagef("--y must be a finite number")
	}

	cwd, path, err := agentPath(deps, cwd, arg)
	if err != nil {
		return err
	}
	theme, err := ResolveTheme(deps, cwd, "")
	if err != nil {
		return err
	}
	el, err := Annotate(path, AnnotateOptions{
		Text: req.Text, X: req.X, Y: req.Y, HasX: req.HasX, HasY: req.HasY, Theme: theme,
	})
	if err != nil {
		return err
	}
	fmt.Printf("board: annotated %s (id %s)\n", path, el.ID)
	return nil
}

// FormatURL is the printed board URL: the loopback host, the owner segment when
// there is one (a live board), and the per-run token in the fragment, so the
// token never rides a request line. The server serves the page under that one
// owner segment.
func FormatURL(host, owner, token string) string {
	path := "/"
	if owner != "" {
		path = "/" + neturl.PathEscape(owner) + "/"
	}
	return "http://" + host + path + "#t=" + token
}

// Serve serves the Excalidraw scene on a loopback listener.
func Serve(deps Deps, opts Options) error {
	return serve(deps, opts, func(host, token string) http.Handler {
		srv := &Server{Token: token, ScenePath: opts.Scene, Theme: opts.Theme, Host: host}
		return srv.Handler()
	})
}

// serve is the lifecycle both formats share: bind, advertise, print, open
// the browser, then serve until a signal and drain. newHandler is called once
// the listener has bound, because the bound host and the per-run token are both
// part of what a handler needs; passing a ready handler instead would mean
// binding the listener twice. The scene name in server.json is the resolved
// slug, never a name derived from the file, which is what lets a live HTML board
// be found by the statusline and by `board url`.
func serve(deps Deps, opts Options, newHandler func(host, token string) http.Handler) error {
	token, err := Token()
	if err != nil {
		return fmt.Errorf("board token: %w", err)
	}
	ln, err := deps.Listen()
	if err != nil {
		return fmt.Errorf("board listen: %w", err)
	}
	defer func() { _ = ln.Close() }()

	host := ln.Addr().String()
	httpSrv := &http.Server{Handler: newHandler(host, token), ReadHeaderTimeout: 10 * time.Second}

	url := FormatURL(host, opts.Owner, token)

	// S6/S7: a live board writes server.json after the listener binds and
	// removes it on shutdown only when the file still carries our pid and start
	// time. A start-time measurement failure writes 0, which readers treat as
	// not live; the server still runs.
	if opts.LiveDir != "" {
		pid := os.Getpid()
		startedAt, perr := deps.ProcStart(pid)
		if perr != nil {
			startedAt = 0
		}
		info := Advertise(opts.Slug, url, pid, startedAt, opts.LiveDir)
		if err := WriteServerInfo(opts.LiveDir, *info); err != nil {
			return fmt.Errorf("board: write server.json: %w", err)
		}
		defer func() { _ = RemoveServerInfo(opts.LiveDir, *info) }()
	}

	fmt.Printf("board: %s  (Ctrl-C to stop)\n", url)
	if !opts.NoOpen {
		if err := deps.Open(url); err != nil {
			fmt.Fprintf(os.Stderr, "relevo: board: open %s: %v\n", url, err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
		return nil
	case serveErr := <-errCh:
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("board serve: %w", serveErr)
	}
}
