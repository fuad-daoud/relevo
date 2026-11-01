package doctor

import (
	"context"
	"os"
	"os/exec"

	"github.com/fuad-daoud/relevo/internal/chatlabel"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// gatePluginRowsFor sets the plugin-row gate matching a harness kind.
func gatePluginRowsFor(kind string, in *MasterMindCheckInput) {
	switch kind {
	case "claude":
		in.Claude = true
	case "muse":
		in.Muse = true
	}
}

// museSessionGate opens the muse MasterMind row when MUSE_SESSION_ID is
// set. The id is read directly, duplicating mastermind.Detect's muse rule,
// which also requires a valid UUID. A stray id on a host with no muse
// candidate, record or binary reports no rows.
func museSessionGate(rt relevo.Runtime, in *MasterMindCheckInput, resolver chatlabel.Resolver) {
	if os.Getenv("MUSE_SESSION_ID") == "" {
		return
	}
	if !in.Muse && !museBinaryPresent() {
		return
	}
	in.MuseSession = true
	museMasterMindFacts(rt, in, resolver)
}

// museMasterMindFacts fills the session-row fields for a muse session as
// mastermindCheckInput's claude branch fills them.
func museMasterMindFacts(rt relevo.Runtime, in *MasterMindCheckInput, resolver chatlabel.Resolver) {
	in.MCPChild = HasMCPChild(hostChildProcesses(os.Getppid()))
	if rt.MasterMinds == nil {
		return
	}
	rec, _, err := mastermind.Resolve(rt.MasterMinds, mastermind.ResolveInput{
		Env:       os.Getenv,
		PPID:      os.Getppid(),
		ProcStart: rt.ProcStart,
		Now:       rt.Now(),
	})
	if err != nil {
		return
	}
	in.Resolved = &rec
	if lbl := resolver.Resolve(context.Background(), rec.HarnessKind, rec.SessionID); lbl != (chatlabel.Label{}) {
		in.Chat = lbl.String()
	}
	if rt.Channels != nil {
		if c, cerr := rt.Channels.Live(rec.ID, rt.Now()); cerr == nil && c != nil {
			in.ClaimLive = true
		}
	}
}

// museBinaryPresent is the direct exec.LookPath this package already makes;
// doctor's Env is not threaded into mastermindCheckInput. A muse with no
// PATH entry is not a muse session.
func museBinaryPresent() bool {
	_, err := exec.LookPath("muse")
	return err == nil
}
