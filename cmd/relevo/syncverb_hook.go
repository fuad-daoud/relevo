package main

import (
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
	"github.com/fuad-daoud/relevo/internal/relevo"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/syncpipe"
)

// installSyncVerbHook makes the owner's socket the way a client asks for a sync
// verb, and it is the whole of what removes the stop dance.
//
// The daemon already holds the shared file and the machine-local file beside it
// under the lock, opened once in this function's caller. The hook runs every
// verb against that pair, so a client asking for enable, push, pull or disable
// never opens the file and never competes for the lock this process is holding.
// A writer verb used to need the daemon stopped precisely because it wanted a
// second direct open; there is no second open left to need that.
//
// Nothing here re-decides what a verb means: the hook is a VerbRunner, which is
// the same value the cockpit's sync actions drive. Serializing it against the
// daemon's own seal and idle triggers needs the Daemon's guard, which is built
// after the owner is served, so installSyncVerbSerializing rebinds the same hook
// with that guard attached; until it does, a verb runs unserialized rather than
// not at all.
//
// A nil server, or a handle with no local file, leaves the hook uninstalled and
// the owner refuses the verb rather than answering as though sync had run.
func installSyncVerbHook(srv *owner.Server, d *db.DB) {
	if srv == nil || d == nil {
		return
	}
	runner := newVerbRunner(d)
	if runner == nil {
		return
	}
	srv.OnSyncVerb = runner.OwnerVerb
}

// newVerbRunner is the one constructor for a verb runner, so the hook the owner
// serves and the cockpit's actions run are the same value over the same handles.
// A handle with no local file yields nil: there is no row sync could own, and a
// runner built without one would have to be handed a nil seam.
//
// The runner is built holding the pipe client, so the daemon has one place a
// worker is constructed. The client starts no process until a call drives it,
// so the wiring installs it while the verbs still refuse.
func newVerbRunner(d *db.DB) *relevo.VerbRunner {
	if d == nil {
		return nil
	}
	local, err := relevosync.LocalHandle(d)
	if err != nil {
		// No machine-local file means no section and no token exist to read or
		// write. Leaving the hook off makes the owner refuse with its own
		// message rather than a verb answering from the shared file, which
		// carries neither and would report a configured machine as unconfigured.
		slog.Warn("relevo daemon: sync verbs are not served", "err", err)
		return nil
	}
	return &relevo.VerbRunner{
		Shared:     d,
		Local:      local,
		Path:       d.Path(),
		Runner:     syncpipe.NewSyncRunner(syncpipe.Config{}, local),
		ClientName: dbSyncHandleName,
	}
}

// installSyncVerbSerializing rebinds the owner's verb hook with the daemon's
// in-flight guard, so a verb and a tick cannot interleave: both record the same
// markers and both hold the same remote handle, and two of them at once would
// write each other's outcomes.
//
// It is called once the Daemon exists, which is after the owner is served, so
// that the guard a verb waits on is the guard the seal hook and the idle tick
// already take.
func installSyncVerbSerializing(d *relevo.Daemon, srv *owner.Server, handle *db.DB) {
	if srv == nil || handle == nil {
		return
	}
	runner := newVerbRunner(handle)
	if runner == nil {
		return
	}
	runner.Serialize = d.WaitSyncSlot
	d.SetSyncVerbs(runner)
	srv.OnSyncVerb = runner.OwnerVerb
}
