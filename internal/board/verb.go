package board

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
	"time"
)

func Listen() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }

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

// Options is what one run of the verb was asked for. scene and slug carry
// the resolved board twice over: scene is the path the server reads, and slug
// is the scene name the advertisement carries. They are separate because a
// board.html path's base name would be "board" for every board in both scopes.
type Options struct {
	Scene   string
	NoOpen  bool
	LiveDir string
	// Owner names the MasterMind a live board belongs to (its name, else its
	// id); empty for the repo scope. It only shapes the printed URL.
	Owner string
	// Slug is the resolved scene name, the one server.json advertises.
	Slug  string
	Scope Scope
}

// Advertise composes the advertisement a live board writes after its
// listener binds; a repo board -- liveDir empty -- writes none. The port
// is read off the printed board URL.
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

// URL prints the live board's URL for a shell copy when the statusline is
// not at hand. It is read-only: no pointer write, no database write, no
// listener. The scene is boardName, else the pointer -- never the default and
// never written. Without a live server it is ErrNotAvailable.
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

// Run parses the verb, resolves the board and scope, writes the pointer
// for a live board that was not read from the pointer, and then runs the
// foreground server. A board path names a single-file board ending in
// /board.html, so a bare slug is not a path and cannot collide with a
// subverb name.
func Run(deps Deps, cwd, ref, boardName, arg string, noOpen bool, shell fs.FS) error {
	return runSingleFileBoard(deps, cwd, ref, boardName, arg, noOpen, shell)
}

// runSingleFileBoard is the default flow: a live or repo board.html, or a
// new board that does not exist yet. The pointer is written for a live board
// that was not read from the pointer and never for a repo board, so the
// pointer always names the board the user is looking at.
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

// FormatURL is the printed board URL: the loopback host, the owner segment
// when there is one, and the per-run token in the fragment, so the token
// never rides a request line.
func FormatURL(host, owner, token string) string {
	path := "/"
	if owner != "" {
		path = "/" + neturl.PathEscape(owner) + "/"
	}
	return "http://" + host + path + "#t=" + token
}

// serve is the lifecycle the board shares: bind, advertise, print, open
// the browser, then serve until a signal and drain. newHandler runs once the
// listener has bound, because the bound host and the per-run token are both
// part of what a handler needs. The scene name in server.json is the resolved
// slug, never a name derived from the file, which is what lets a live HTML
// board be found by the statusline and by `board url`.
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

	// A live board writes server.json after the listener binds and removes it
	// on shutdown only when the file still carries our pid and start time. A
	// start-time measurement failure writes 0, which readers treat as not
	// live; the server still runs.
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
