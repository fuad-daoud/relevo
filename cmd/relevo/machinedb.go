package main

import (
	"context"
	"errors"
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
	// routeDirect opens the file directly even with an owner listening, which
	// is what the hidden RELEVO_DB_DIRECT escape hatch selects.
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

var (
	dbRouteMode   = routeNone
	dbRouteBudget = verbDialBudget
)

// dbRouteFromArgs gates run()'s install. TestMain clears it, so the tests that
// call run() reach the machine database the way they always have -- directly --
// and a test that wants the switch installs it with installDBRoute.
var dbRouteFromArgs = true

// installDBRoute points this process at mode with the given dial budget. A
// platform with no owner socket keeps the direct open, so the cross-compiled
// build never has a route it cannot serve.
func installDBRoute(mode dbRoute, budget time.Duration) {
	if mode == routeOwner && !ownerRouteSupported() {
		mode = routeDirect
	}
	dbRouteMode = mode
	dbRouteBudget = budget
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
	mode, budget := routeForArgs(args)
	installDBRoute(mode, budget)
	return peek
}

// routeForArgs maps a command line to its route and budget: the peek verbs and
// the daemon open directly, RELEVO_DB_DIRECT keeps the direct open, the
// statusline gets the short budget, and every other verb dials with the default.
func routeForArgs(args []string) (dbRoute, time.Duration) {
	if isPeekArgs(args) || (len(args) > 0 && args[0] == "daemon") {
		return routeNone, verbDialBudget
	}
	if os.Getenv("RELEVO_DB_DIRECT") != "" {
		return routeDirect, verbDialBudget
	}
	if isStatuslineArgs(args) {
		return routeOwner, statuslineDialBudget
	}
	return routeOwner, verbDialBudget
}

// isPeekArgs reports the read-only verbs, which never dial and never start the
// owner: the daemon's --check/--preflight probes, and bugreport, whose read-only
// runtime opens the machine database itself and must leave the route alone. A
// peek verb also skips captureAgyEnv, which would open -- and can migrate -- the
// very database the verb promises not to touch. bugreport's own flags
// (--name/--round/--logs) pick what it reads and never change the route.
func isPeekArgs(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == "bugreport" {
		return true
	}
	return args[0] == "daemon" && hasArg(args[1:], "--check", "--preflight")
}

// isStatuslineArgs reports the statusline, whose budget is the short one.
func isStatuslineArgs(args []string) bool {
	return len(args) > 0 && args[0] == "status" && hasArg(args[1:], "--line")
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
	d, err := db.Dial(sock)
	if err != nil {
		return nil, true, err
	}
	return d, true, nil
}
