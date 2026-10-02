package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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

	"github.com/fuad-daoud/relevo/internal/board"
)

// boardFlagValues holds the pointers board parses into.
type boardFlagValues struct {
	noOpen     *bool
	theme      *string
	board      *string
	mastermind *string
}

// boardFlagSet defines board's flags on fs and returns what it parses into, so
// the registry's parity test finds exactly one installer per verb.
func boardFlagSet(fs *flag.FlagSet) *boardFlagValues {
	v := &boardFlagValues{}
	v.noOpen = fs.Bool("no-open", false, "print the URL without opening a browser")
	v.theme = fs.String("theme", "", "the theme for new elements: cockpit or blueprint")
	v.board = fs.String("board", "", "the live scene name to open (default: the pointer, else board)")
	v.mastermind = fs.String("mastermind", "", "the MasterMind whose live board to open (id or name)")
	return v
}

// boardListen and boardOpen are the two effects no cmd test runs: the loopback
// listener and the browser opener, both replaceable seams.
var (
	boardListen = func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
	boardOpen   = func(url string) error {
		opener := "xdg-open"
		if runtime.GOOS == "darwin" {
			opener = "open"
		}
		return exec.Command(opener, url).Start()
	}
)

// boardRepoRootFn is the repository-root resolver, a seam so a test can refuse
// or accept without depending on where the test binary runs.
var boardRepoRootFn = boardRepoRoot

// boardRepoRoot is cwd's git top level, the root scenes are confined to.
func boardRepoRoot(cwd string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// boardGitConfig reads one repo-local git config key; an unset key is
// ("", nil), the same fact git reports with exit 1. A seam for the tests.
var boardGitConfig = func(cwd, key string) (string, error) {
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

// boardProcStart reads a process's start time in Unix seconds; a seam so the
// statusline and the server lifecycle can be tested without a real pid.
var boardProcStart = procStartUnix

// boardOptions is what one run of the verb was asked for.
type boardOptions struct {
	scene   string
	theme   *board.Theme
	noOpen  bool
	liveDir string
}

// boardServerInfo composes the advertisement a live board writes after its
// listener binds; a repo board -- liveDir empty -- writes none (S6). url is the
// printed board URL (host and per-run token), from which the port is read.
func boardServerInfo(scene, url string, pid int, startedAt int64, liveDir string) *board.ServerInfo {
	if liveDir == "" {
		return nil
	}
	port := 0
	if u, err := neturl.Parse(url); err == nil {
		port, _ = strconv.Atoi(u.Port())
	}
	return &board.ServerInfo{Scene: scene, URL: url, Port: port, PID: pid, StartedAt: startedAt}
}

// boardURLFlagValues holds the pointers `board url` parses into.
type boardURLFlagValues struct {
	board      *string
	mastermind *string
}

// boardURLFlagSet defines `board url`'s flags on fs and returns what it parses
// into.
func boardURLFlagSet(fs *flag.FlagSet) *boardURLFlagValues {
	v := &boardURLFlagValues{}
	v.board = fs.String("board", "", "the live scene name whose URL to print (default: the pointer)")
	v.mastermind = fs.String("mastermind", "", "the MasterMind whose live board URL to print (id or name)")
	return v
}

// cmdBoardURL prints the live board's URL for a shell copy when the statusline
// is not at hand (S9). It is read-only: no pointer write, no database write, no
// listener. The scene is --board, else the pointer -- never the default and
// never written. Without a live server it exits 1 not_available, printing
// nothing on stdout and one line naming the scene and the MasterMind.
func cmdBoardURL(args []string) error {
	fs := flag.NewFlagSet("relevo board url", flag.ContinueOnError)
	v := boardURLFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fail(codeUsage, "relevo board url takes no scene path, got %q", fs.Args()[0])
	}

	id, err := boardMasterMind(*v.mastermind)
	if err != nil {
		return err
	}
	liveRoot, err := boardLiveRoot()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	liveDir := filepath.Join(liveRoot, id)

	scene := *v.board
	if scene != "" {
		if err := board.ValidSceneName(scene); err != nil {
			return boardRefusal(err)
		}
	} else {
		name, present, err := board.Pointer(liveDir)
		if err != nil {
			return boardRefusal(err)
		}
		if !present {
			return boardNotAvailable(scene, id)
		}
		scene = name
	}

	url, ok, err := board.LiveURL(liveDir, scene, procStartUnix)
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	if !ok {
		return boardNotAvailable(scene, id)
	}
	fmt.Println(url)
	return nil
}

// boardNotAvailable is S9's refusal: exit 1 not_available, one line naming the
// scene and the MasterMind, nothing on stdout.
func boardNotAvailable(scene, id string) error {
	if scene == "" {
		scene = board.DefaultBoard
	}
	return fail(codeNotAvailable, "relevo board url: no live server for scene %s of MasterMind %s", scene, id)
}

// cmdBoard parses the verb, resolves the scene and scope, writes the pointer
// for a live board that was not read from the pointer, and then runs the
// foreground server. The subverbs dispatch first: a scene path always ends in
// .excalidraw, so it can never collide with a subverb name.
func cmdBoard(args []string) error {
	var sub string
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "url":
		return cmdBoardURL(args[1:])
	case "comments":
		return cmdBoardComments(args[1:])
	case "comment":
		return cmdBoardComment(args[1:])
	case "text":
		return cmdBoardText(args[1:])
	case "annotate":
		return cmdBoardAnnotate(args[1:])
	}
	fs := flag.NewFlagSet("relevo board", flag.ContinueOnError)
	v := boardFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) > 1 {
		return fail(codeUsage, "relevo board takes at most one scene path, got %d", len(fs.Args()))
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	theme, err := boardResolveTheme(cwd, *v.theme)
	if err != nil {
		return boardRefusal(err)
	}

	arg := ""
	if len(fs.Args()) == 1 {
		arg = fs.Args()[0]
	}
	res, err := resolveBoard(cwd, *v.mastermind, *v.board, arg)
	if err != nil {
		return err
	}
	// S4: the pointer is written when a live board is opened or selected and the
	// name differs; WritePointer is the "only when it differs" rule.
	if res.Scope == board.ScopeLive && !res.FromPointer {
		if err := board.WritePointer(res.LiveDir, res.Scene); err != nil {
			return fail(codeInternal, "%v", err)
		}
	}
	liveDir := ""
	if res.Scope == board.ScopeLive {
		liveDir = res.LiveDir
	}
	return runBoard(boardOptions{scene: res.Path, theme: theme, noOpen: *v.noOpen, liveDir: liveDir})
}

// boardResolveTheme applies the precedence: --theme, then the repo-local
// relevo.boardTheme, then cockpit.
func boardResolveTheme(cwd, flagTheme string) (*board.Theme, error) {
	name := flagTheme
	if name == "" {
		key, err := boardGitConfig(cwd, "relevo.boardTheme")
		if err != nil {
			return nil, fmt.Errorf("read relevo.boardTheme: %w", err)
		}
		name = key
	}
	if name == "" {
		name = board.Names()[0]
	}
	return board.Lookup(name)
}

// boardRefusal maps a board package refusal to the catalog code it earns: a
// usage error exits 2 with the usage hint, a missing or invalid scene is a
// refused error with no next command, and anything else is internal.
func boardRefusal(err error) error {
	switch {
	case errors.Is(err, board.ErrUsage):
		return fail(codeUsage, "%s", err)
	case errors.Is(err, board.ErrNotFound):
		return fail(codeRefused, "%s", err)
	case errors.Is(err, board.ErrInvalid):
		return fail(codeRefused, "%s", boardInvalidText(err))
	default:
		return fail(codeInternal, "%s", err)
	}
}

// boardInvalidText is the board.ErrInvalid message without its sentinel word,
// so the refusal names the scene's fault rather than the sentinel.
func boardInvalidText(err error) string {
	return strings.TrimPrefix(err.Error(), board.ErrInvalid.Error()+": ")
}

// boardTextFlagValues holds the pointers board text parses into.
type boardTextFlagValues struct {
	asJSON *bool
}

// boardTextFlagSet defines board text's flags on fs and returns what it parses
// into, so the registry's parity test finds exactly one installer per verb.
func boardTextFlagSet(fs *flag.FlagSet) *boardTextFlagValues {
	v := &boardTextFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the text elements as a JSON document")
	return v
}

// boardAnnotateFlagValues holds the pointers board annotate parses into.
type boardAnnotateFlagValues struct {
	text *string
	x    *float64
	y    *float64
}

// boardAnnotateFlagSet defines board annotate's flags on fs and returns what it
// parses into.
func boardAnnotateFlagSet(fs *flag.FlagSet) *boardAnnotateFlagValues {
	v := &boardAnnotateFlagValues{}
	v.text = fs.String("text", "", "the text to append")
	v.x = fs.Float64("x", 0, "the appended element's x coordinate")
	v.y = fs.Float64("y", 0, "the appended element's y coordinate")
	return v
}

// boardAgentPath resolves a subverb's scene path: the working directory, the
// repository root and the confined scene path. A path outside a repository is
// usage.
func boardAgentPath(arg string) (string, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", fail(codeInternal, "%v", err)
	}
	root, err := boardRepoRootFn(cwd)
	if err != nil {
		return "", "", fail(codeUsage, "not inside a git repository: %v", err)
	}
	path, err := board.Resolve(root, cwd, arg)
	if err != nil {
		return "", "", boardRefusal(err)
	}
	return cwd, path, nil
}

// cmdBoardText lists a scene's text elements: one JSON document under --json,
// one tabwriter row each otherwise.
func cmdBoardText(args []string) error {
	fs := flag.NewFlagSet("relevo board text", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := boardTextFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	positional := fs.Args()
	if len(positional) != 1 {
		return fail(codeUsage, "relevo board text needs one scene path")
	}
	_, path, err := boardAgentPath(positional[0])
	if err != nil {
		return err
	}
	elements, err := board.TextElements(path)
	if err != nil {
		return boardRefusal(err)
	}
	if *v.asJSON {
		return json.NewEncoder(os.Stdout).Encode(elements)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, e := range elements {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.ID,
			strconv.FormatFloat(e.X, 'f', -1, 64),
			strconv.FormatFloat(e.Y, 'f', -1, 64),
			e.Text)
	}
	return tw.Flush()
}

// cmdBoardAnnotate appends one text element to a scene and prints its id. Every
// usage check runs before any repository, theme or file work.
func cmdBoardAnnotate(args []string) error {
	fs := flag.NewFlagSet("relevo board annotate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := boardAnnotateFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	positional := fs.Args()
	if len(positional) != 1 {
		return fail(codeUsage, "relevo board annotate needs one scene path")
	}

	var textSet, xSet, ySet bool
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "text":
			textSet = true
		case "x":
			xSet = true
		case "y":
			ySet = true
		}
	})
	if !textSet {
		return fail(codeUsage, "--text is required")
	}
	if *v.text == "" {
		return fail(codeUsage, "--text must not be empty")
	}
	if xSet != ySet {
		return fail(codeUsage, "--x and --y come together")
	}
	if xSet && (math.IsNaN(*v.x) || math.IsInf(*v.x, 0)) {
		return fail(codeUsage, "--x must be a finite number")
	}
	if ySet && (math.IsNaN(*v.y) || math.IsInf(*v.y, 0)) {
		return fail(codeUsage, "--y must be a finite number")
	}

	cwd, path, err := boardAgentPath(positional[0])
	if err != nil {
		return err
	}
	theme, err := boardResolveTheme(cwd, "")
	if err != nil {
		return boardRefusal(err)
	}
	el, err := board.Annotate(path, board.AnnotateOptions{
		Text: *v.text, X: *v.x, Y: *v.y, HasX: xSet, HasY: ySet, Theme: theme,
	})
	if err != nil {
		return boardRefusal(err)
	}
	fmt.Printf("board: annotated %s (id %s)\n", path, el.ID)
	return nil
}

// runBoard serves the scene on a loopback listener until SIGINT or SIGTERM,
// then drains through Shutdown so an in-flight save finishes.
func runBoard(opts boardOptions) error {
	token, err := board.Token()
	if err != nil {
		return fail(codeInternal, "board token: %v", err)
	}
	ln, err := boardListen()
	if err != nil {
		return fail(codeInternal, "board listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	host := ln.Addr().String()
	srv := &board.Server{Token: token, ScenePath: opts.scene, Theme: opts.theme, Host: host}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	url := "http://" + host + "/#t=" + token

	// S6/S7: a live board writes server.json after the listener binds and
	// removes it on shutdown only when the file still carries our pid and start
	// time. A start-time measurement failure writes 0, which readers treat as
	// not live; the server still runs.
	if opts.liveDir != "" {
		sceneName := strings.TrimSuffix(filepath.Base(opts.scene), ".excalidraw")
		pid := os.Getpid()
		startedAt, perr := boardProcStart(pid)
		if perr != nil {
			startedAt = 0
		}
		info := boardServerInfo(sceneName, url, pid, startedAt, opts.liveDir)
		if err := board.WriteServerInfo(opts.liveDir, *info); err != nil {
			return fail(codeInternal, "board: write server.json: %v", err)
		}
		defer func() { _ = board.RemoveServerInfo(opts.liveDir, *info) }()
	}

	fmt.Printf("board: %s  (Ctrl-C to stop)\n", url)
	if !opts.noOpen {
		if err := boardOpen(url); err != nil {
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
		return fail(codeInternal, "board serve: %v", serveErr)
	}
}
