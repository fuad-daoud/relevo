package doctor

import (
	"os"
	"os/user"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// sandboxMarkerName is the file under the state root that
// scripts/relevo-dev-user.sh writes for a dev sandbox. Its presence turns
// this account into a sandbox; without it the linger and port rows are
// absent.
const sandboxMarkerName = "sandbox"

// sandboxChecks appends the rows for a dev-sandbox account: the xdg row
// for every user and the linger and port rows only for a marked sandbox. A
// nil d leaves the port row with no listen address to read.
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
// cannot be opened. A store whose open failed leaves the serve and sandbox
// port rows off.
func serveMachineDB(rt relevo.Runtime) *db.DB {
	d, err := rt.Store.DB()
	if err != nil {
		return nil
	}
	return d
}

// sandboxMarker reads and parses the sandbox marker under stateRoot. A
// marker that cannot be read or names no sandbox reads as no sandbox at
// all.
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
