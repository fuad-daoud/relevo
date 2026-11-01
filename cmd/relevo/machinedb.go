package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
)

// errOwnerUnavailable marks a dial the owner never answered, after the whole
// budget was spent. The statusline turns it into silence; every other verb
// prints it, socket included.
var errOwnerUnavailable = errors.New("relevo owner unavailable")

// errOwnerSandboxDenied marks a dial the host's sandbox refused outright: the
// verb, the daemon and the socket are all fine, so the way out is an
// unsandboxed shell, not a bug report.
var errOwnerSandboxDenied = errors.New("relevo owner socket denied by the sandbox")

// dialSandboxDenied reports whether a dial failure is the sandbox refusing
// the socket call: a permission errno at any depth. A missing socket, refused
// connection or spent deadline keeps reading as an owner that never answered.
func dialSandboxDenied(err error) bool {
	return errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.EPERM) ||
		os.IsPermission(err)
}

// dbRoute is how a process reaches the machine database.
type dbRoute int

const (
	// routeNone leaves the route off: the machine database is opened directly,
	// which is what the daemon, the peek verbs and a test get.
	routeNone dbRoute = iota
	// routeDirect opens the file directly. It is not selectable by any verb:
	// installDBRoute falls back to it only on a platform with no owner socket,
	// where the direct open is the only route there is.
	routeDirect
	// routeOwner dials the owner, starting it when the socket is missing.
	routeOwner
)

// The dial budgets: a verb or hook must answer in two seconds, and the
// statusline, which may run once a second, gives up in a fraction of one.
const (
	verbDialBudget       = 2 * time.Second
	statuslineDialBudget = 500 * time.Millisecond
)

// ownerStartWait is how long a verb waits for a starting daemon: drain,
// re-exec gap, config load, lock and migration add up to about ten seconds,
// with margin, measured from the first dial attempt.
const ownerStartWait = 15 * time.Second

var (
	dbRouteMode      = routeNone
	dbRouteBudget    = verbDialBudget
	dbRouteStartWait time.Duration
)

// dbRouteFromArgs gates run()'s install. TestMain clears it, so the tests that
// call run() reach the machine database the way they always have -- directly --
// and a test that wants the switch installs it with installDBRoute.
var dbRouteFromArgs = true

// installDBRoute points this process at mode with the given dial budget and
// start wait. A platform with no owner socket keeps the direct open, so the
// cross-compiled build never has a route it cannot serve.
func installDBRoute(mode dbRoute, budget, startWait time.Duration) {
	if mode == routeOwner && !ownerRouteSupported() {
		mode = routeDirect
	}
	dbRouteMode = mode
	dbRouteBudget = budget
	dbRouteStartWait = startWait
	if mode != routeOwner {
		store.SetMachineOpener(nil)
		client.SetDialer(nil)
		return
	}
	store.SetMachineOpener(openDBRoute)
	client.SetDialer(func(ctx context.Context, sock string) (net.Conn, error) {
		return dialWithStart(ctx, sock)
	})
}

// installRouteForArgs installs the route args need and reports whether the peek
// verbs are in play, which captureAgyEnv must skip.
func installRouteForArgs(args []string) (peek bool) {
	peek = isPeekArgs(args)
	if !dbRouteFromArgs {
		return peek
	}
	mode, budget, startWait := routeForArgs(args)
	installDBRoute(mode, budget, startWait)
	return peek
}

// routeForArgs maps a command line to its route: the peek verbs and the
// daemon open directly, the statusline gets the short budget, a hook gets the
// verb budget with no start wait, and every other verb dials the owner.
func routeForArgs(args []string) (dbRoute, time.Duration, time.Duration) {
	if isPeekArgs(args) || (len(args) > 0 && args[0] == "daemon") {
		return routeNone, verbDialBudget, 0
	}
	if isStatuslineArgs(args) {
		return routeOwner, statuslineDialBudget, 0
	}
	if isHookArgs(args) {
		return routeOwner, verbDialBudget, 0
	}
	return routeOwner, verbDialBudget, ownerStartWait
}

// isPeekArgs reports the read-only verbs, which install no route and never
// start the owner, and skip captureAgyEnv, which would open -- and can
// migrate -- the database the verb promises not to touch.
func isPeekArgs(args []string) bool {
	if len(args) == 0 {
		return false
	}
	// db rename-repo and db relabel write, so they must reach the database
	// through the owner like any other writer; the direct open their db
	// siblings take is refused while the daemon holds the lock.
	if args[0] == "db" && len(args) > 1 && (args[1] == "rename-repo" || args[1] == "relabel") {
		return false
	}
	if args[0] == "bugreport" || args[0] == "db" || args[0] == "board" {
		return true
	}
	// The sync worker holds no handle on any of this installation's databases,
	// so it must not install a route that would open one, nor capture the agy
	// credentials that capture itself opens and can migrate.
	if args[0] == "sync-worker" {
		return true
	}
	return args[0] == "daemon" && hasArg(args[1:], "--check", "--preflight")
}

func isStatuslineArgs(args []string) bool {
	return len(args) > 0 && args[0] == "status" && hasArg(args[1:], "--line")
}

// isHookArgs reports a hook invocation: it must answer in the verb budget
// and never waits for a starting daemon, because the editor is blocked on it.
func isHookArgs(args []string) bool {
	return hasArg(args, "--hook")
}

func hasArg(args []string, names ...string) bool {
	for _, a := range args {
		for _, name := range names {
			if a == name {
				return true
			}
		}
	}
	return false
}

// machineDBPath is <state root>/relevo.db, the one path every client dials and
// every other path opens directly.
func machineDBPath() string {
	root, err := tmpdir.DefaultRoot()
	if err != nil {
		return ""
	}
	return filepath.Join(root, "relevo.db")
}

// openDBRoute is the store's machine opener for the machine path on the owner
// route only; ok true means the store takes the result as-is. A starting
// daemon is awaited inside one deadline measured from the first attempt.
func openDBRoute(path string) (*db.DB, bool, error) {
	if dbRouteMode != routeOwner || path != machineDBPath() {
		return nil, false, nil
	}
	root, err := tmpdir.DefaultRoot()
	if err != nil {
		return nil, true, err
	}
	sock, err := ownerSocket(root)
	if err != nil {
		return nil, true, err
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), dbRouteBudget)
	d, err := db.DialContext(ctx, sock)
	expired := attemptExpired(ctx, err)
	cancel()
	if err == nil {
		return d, true, nil
	}
	if errors.Is(err, errOwnerSandboxDenied) {
		// The refusal came from the shell's sandbox, not from the owner's
		// state: starting or waiting cannot change it, so it never reaches
		// the await-starting-owner path.
		return nil, true, err
	}
	if expired {
		// An owner that holds the listener but never answers must read as
		// unavailable, the same as one that never bound.
		err = fmt.Errorf("%w: %w", errOwnerUnavailable, err)
	}
	if dbRouteStartWait <= 0 || !ownerStarting(root, sock) {
		return nil, true, err
	}
	return awaitStartingOwner(sock, start.Add(dbRouteStartWait), dbRouteStartWait)
}

// attemptExpired reports whether the route's own budget, and not the owner,
// ended the attempt: the socket deadline the attempt's ctx sets fires as an
// i/o timeout, and the ctx itself may not have recorded it yet.
func attemptExpired(ctx context.Context, err error) bool {
	return errors.Is(err, os.ErrDeadlineExceeded) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// awaitStartingOwner waits for a starting daemon: one waiting line, then
// re-dials against the single deadline. The refusal names the daemon as
// starting or unresponsive so it does not read as a plain refusal.
func awaitStartingOwner(sock string, deadline time.Time, limit time.Duration) (*db.DB, bool, error) {
	fmt.Fprintf(os.Stderr, "relevo: the relevo daemon is starting; waiting up to %s (%s)\n", limit, sock)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	for {
		d, err := db.DialContext(ctx, sock)
		if err == nil {
			return d, true, nil
		}
		if ctx.Err() != nil {
			return nil, true, fmt.Errorf("%w: relevo daemon at %s is starting or unresponsive after %s", errOwnerUnavailable, sock, limit)
		}
		time.Sleep(waitStep())
	}
}

// waitStep is the pause between two wait attempts: short, and jittered so
// several clients waiting on the same daemon do not retry in lockstep.
func waitStep() time.Duration {
	return time.Duration(25+rand.Intn(76)) * time.Millisecond
}

// sandboxBannerVerb returns the sandbox name to print above a command's
// output, or "" with no sandbox, for a peek verb, or for a constantly-run
// read-only verb. A `config get` in a pipeline stays pipelineable.
func sandboxBannerVerb(args []string) string {
	name := os.Getenv("RELEVO_SANDBOX")
	if name == "" || len(args) == 0 || isPeekArgs(args) {
		return ""
	}
	switch args[0] {
	case "status", "show", "history", "doctor", "version", "help":
		return ""
	case "config":
		switch {
		case len(args) == 1:
			return ""
		case args[1] == "get", args[1] == "export", args[1] == "help":
			return ""
		}
	}
	return name
}
