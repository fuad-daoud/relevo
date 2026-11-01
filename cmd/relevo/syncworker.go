package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/syncworker"
)

// workerReplicaName is the replica file the worker owns beside relevo.db, named here
// as well as in the daemon's wiring so a worker started without the flag still
// lands in the same state directory.
const workerReplicaName = "relevo-sync.db"

// cmdSyncWorker serves the sync pipe on stdin and stdout until the daemon closes
// stdin. The worker owns the replica and nothing else: it opens no handle on
// relevo.db. The verb is hidden from the usage text and the registry because no
// human runs it; documenting how to start one by hand would be a second way to
// start a worker beside the daemon.
func cmdSyncWorker(args []string) error {
	fs := flag.NewFlagSet("sync-worker", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	// The replica flag is documented below because the daemon passes it and a
	// reader of a process list should know what the path is.
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

	syncworker.OwnerOnlyFiles()

	// Nothing is opened on the way in: the worker holds no database handle of its
	// own, so it must not install the route the other verbs read through.
	if err := syncworker.Serve(os.Stdin, os.Stdout, syncworker.NewTursoBackendForPath(path)); err != nil {
		return err
	}
	return nil
}
