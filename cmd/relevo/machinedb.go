package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// errOwnerUnavailable marks a dial the owner never answered, after the whole
// budget was spent. The statusline turns it into silence; every other verb
// prints it, socket included.
var errOwnerUnavailable = errors.New("relevo owner unavailable")

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

// ownerStartWait is how long a verb waits for a daemon that is already
// starting: the drain window, the re-exec gap and the new image's config load,
// lock and migration add up to about ten seconds, and this leaves margin. It
// sits far below any service manager's start timeout, and it is measured from
// the first dial attempt, so a verb spends at most this much in total.
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

// routeForArgs maps a command line to its route, its dial budget and how long
// it may wait for a daemon that is still starting: the peek verbs and the
// daemon open directly, the statusline gets the short budget, a hook gets the
// verb budget with no start wait, and every other verb dials with the default
// budget and the start wait.
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
// start the owner: the daemon's --check/--preflight probes, bugreport, whose
// read-only runtime opens the machine database itself, db query, which reads
// the file directly or through an owner that is already up, and board, whose
// runtime reads and writes the repository itself. A peek verb also skips
// captureAgyEnv, which would open -- and can migrate -- the very database the
// verb promises not to touch. bugreport's own flags (--name/--round/--logs),
// db query's SQL, and board's ([path]/--board/--mastermind/--theme/--no-open)
// pick what the verb does and never change the route.
func isPeekArgs(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == "bugreport" || args[0] == "db" || args[0] == "board" {
		return true
	}
	return args[0] == "daemon" && hasArg(args[1:], "--check", "--preflight")
}

// isStatuslineArgs reports the statusline, whose budget is the short one.
func isStatuslineArgs(args []string) bool {
	return len(args) > 0 && args[0] == "status" && hasArg(args[1:], "--line")
}

// isHookArgs reports a hook invocation, which is every verb the claude plugin
// runs: a hook must answer in the verb budget and never waits for a daemon that
// is still starting, because the editor is blocked on it.
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
	root, err := store.DefaultRoot()
	if err != nil {
		return ""
	}
	return filepath.Join(root, "relevo.db")
}

// openDBRoute is the store's machine opener: it answers only for the machine
// path on the owner route, and only then does it dial. ok true means the store
// must take the result as-is -- the handle, or the refusal.
//
// The first attempt spends the route's own dial budget. When it fails and the
// route carries a start wait, and the evidence says a daemon is still starting,
// the opener waits inside one deadline measured from that first attempt.
func openDBRoute(path string) (*db.DB, bool, error) {
	if dbRouteMode != routeOwner || path != machineDBPath() {
		return nil, false, nil
	}
	root, err := store.DefaultRoot()
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

// awaitStartingOwner waits for a daemon that is still starting: it prints the
// one waiting line, then re-dials against the single deadline with a short
// jittered pause between attempts. The limit has run out when the deadline
// passes, and the refusal names the daemon as starting or unresponsive so a
// reader does not take it for a plain refusal.
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
