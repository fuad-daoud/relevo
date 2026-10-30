package doctor

import (
	"os"
	"os/user"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// sandboxMarkerName is the file under the state root that
// scripts/relevo-dev-user.sh writes for a dev sandbox. Its presence is what
// turns this account from a production install into a sandbox; without it the
// linger and port rows are absent, so an ordinary machine sees only the xdg
// row and gains nothing it has to learn about sandboxes.
const sandboxMarkerName = "sandbox"

// sandboxChecks appends the rows for a dev-sandbox account: the xdg row, which
// every user gets, and the linger and port rows, which only a marked sandbox
// does. It reads the marker off the state root, the serve pointer off d (nil
// when the machine database could not be opened, which simply leaves the port
// row with no listen address to read), and the running identity from the OS.
func sandboxChecks(env Env, d *db.DB, stateRoot, configRoot string, checks []Check) []Check {
	in := SandboxInput{StateRoot: stateRoot, UID: os.Getuid()}
	in.StateHome = os.Getenv("XDG_STATE_HOME")
	in.ConfigHome = os.Getenv("XDG_CONFIG_HOME")
	in.ConfigRoot = configRoot
	if u, err := user.Current(); err == nil {
		in.User = u.Username
	}

	checks = InsertGlobalCheck(checks, XDGCheck(env, in))

	marker, ok := sandboxMarker(stateRoot)
	if !ok {
		return checks
	}
	in.MarkerPort = marker.Port

	checks = InsertGlobalCheck(checks, LingerCheck(env, in))

	// The port row needs a serve pointer to have a port to read. A sandbox
	// that runs no `relevo serve` has none, so there is no row rather than a
	// row with nothing behind it.
	p, pok, err := db.ReadDaemonPointer(d)
	if err != nil || !pok {
		return checks
	}
	in.Listen = p.Listen
	return InsertGlobalCheck(checks, PortCheck(in))
}

// serveMachineDB opens the machine database rt.Store holds, or nil when it
// cannot be opened. Both the serve rows and the sandbox port row read that
// database; a store whose open failed leaves the rows off, as every other
// best-effort row does.
func serveMachineDB(rt relevo.Runtime) *db.DB {
	d, err := rt.Store.DB()
	if err != nil {
		return nil
	}
	return d
}

// sandboxMarker reads and parses the sandbox marker under stateRoot. A marker
// that does not exist, that cannot be read, or that names no sandbox reads as
// no sandbox at all, so a corrupt file turns the extra rows off rather than
// reporting on one it cannot name.
func sandboxMarker(stateRoot string) (SandboxMarker, bool) {
	if stateRoot == "" {
		return SandboxMarker{}, false
	}
	raw, err := os.ReadFile(stateRoot + "/" + sandboxMarkerName)
	if err != nil {
		return SandboxMarker{}, false
	}
	return ParseSandboxMarker(raw)
}
