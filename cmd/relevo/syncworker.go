package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/syncworker"
)

// workerReplicaName is the replica file the worker owns beside relevo.db. It is
// named here as well as in the daemon's wiring so a worker started without the
// flag still lands in the same state directory rather than the caller's cwd.
const workerReplicaName = "relevo-sync.db"

// cmdSyncWorker serves the sync pipe on stdin and stdout until the daemon
// closes stdin. It is spawned by the daemon as a child of this same executable
// and named by nothing but the command line below.
//
// The worker owns the replica and nothing else: it opens no handle on
// relevo.db, and the replica is reached only through the sync constructor the
// driver wraps, which is what keeps a driver abort inside this process from
// touching the record.
//
// The verb is hidden from the usage text and the registry because no human runs
// it: the daemon is its only caller, and a command line that documents how to
// start the worker by hand is a second way to start a worker beside the daemon.
func cmdSyncWorker(args []string) error {
	fs := flag.NewFlagSet("sync-worker", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	// The help a human asked for is the whole usage text, which does not name
	// this verb. The replica flag is documented on the one line below because
	// the daemon passes it and a reader of a process list should know what the
	// path is.
	fs.Usage = func() { fmt.Fprint(fs.Output(), usage) }
	replica := fs.String("replica", "", "the replica file this worker owns")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fail(codeUsage, "relevo sync-worker takes no arguments, got %q", fs.Arg(0))
	}

	path := *replica
	if path == "" {
		shared := machineDBPath()
		if shared == "" {
			return fail(codeConfigInvalid, "relevo sync-worker: no state directory to place %s in", workerReplicaName)
		}
		path = filepath.Join(filepath.Dir(shared), workerReplicaName)
	}

	// Nothing is opened and nothing is captured on the way in: the worker holds
	// no database handle of this installation's own, so it must not install the
	// route the other verbs read through. It is a peek verb for that reason.
	if err := syncworker.Serve(os.Stdin, os.Stdout, syncworker.NewTursoBackendForPath(path)); err != nil {
		return err
	}
	return nil
}
