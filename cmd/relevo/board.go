package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
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

// boardOptions is what one run of the verb was asked for.
type boardOptions struct {
	scene  string
	theme  *board.Theme
	noOpen bool
}

// cmdBoard parses the verb, resolves the scene and scope, writes the pointer
// for a live board that was not read from the pointer, and then runs the
// foreground server.
func cmdBoard(args []string) error {
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
	return runBoard(boardOptions{scene: res.Path, theme: theme, noOpen: *v.noOpen})
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

// boardRefusal maps a board package refusal to the catalog code it earns.
func boardRefusal(err error) error {
	if errors.Is(err, board.ErrUsage) {
		return fail(codeUsage, "%s", err)
	}
	return fail(codeInternal, "%s", err)
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
