package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/syncworker"
)

// errNoSyncBackend is why every verb this build answers with a refusal. The
// pipe, its framing and the lifecycle all work; what is missing is the replica
// behind them, and a verb that would move a byte says so rather than answering
// with a log that is not there.
var errNoSyncBackend = errors.New("this build carries no sync backend: the worker's replica is not wired up")

// cmdSyncWorker serves the sync pipe on stdin and stdout until the daemon
// closes stdin. It is spawned by the daemon as a child of this same executable
// and named by nothing but the command line below.
//
// The verb is hidden from the usage text and the registry because no human runs
// it: the daemon is its only caller, and a command line that documents how to
// start the worker by hand is a second way to start a worker beside the daemon.
func cmdSyncWorker(args []string) error {
	fs := flag.NewFlagSet("sync-worker", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	// The help a human asked for is the whole usage text, which does not name
	// this verb: it takes no flags, so its own list would be the empty one.
	fs.Usage = func() { fmt.Fprint(fs.Output(), usage) }
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fail(codeUsage, "relevo sync-worker takes no arguments, got %q", fs.Arg(0))
	}

	// Nothing is opened and nothing is captured on the way in: the worker holds
	// no database handle of this installation's own, so it must not install the
	// route the other verbs read through. It is a peek verb for that reason.
	if err := syncworker.Serve(os.Stdin, os.Stdout, unbuiltBackend{}); err != nil {
		return err
	}
	return nil
}

// unbuiltBackend is the log this build has nothing behind. It refuses rather
// than answers, because the two things a caller can do with an answer -- clear
// outbox rows, advance an import mark -- are both wrong when the remote was
// never asked.
type unbuiltBackend struct{}

func (unbuiltBackend) Open(syncworker.Spec) error { return errNoSyncBackend }

func (unbuiltBackend) Append([]syncworker.Entry) ([]syncworker.Entry, error) {
	return nil, errNoSyncBackend
}

func (unbuiltBackend) Pull(map[string]int) ([]syncworker.Entry, error) {
	return nil, errNoSyncBackend
}

func (unbuiltBackend) Head(string, string, int) ([]syncworker.HeadRow, error) {
	return nil, errNoSyncBackend
}

func (unbuiltBackend) Stats() (syncworker.Stats, error) {
	return syncworker.Stats{}, errNoSyncBackend
}

func (unbuiltBackend) Close() error { return nil }
