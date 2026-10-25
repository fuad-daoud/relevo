package main

import (
	"context"
	"os"
	"os/exec"

	"github.com/fuad-daoud/relevo/internal/chatlabel"
	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// gatePluginRowsFor sets the plugin-row gate matching a harness kind, from a
// candidate kind or a mastermind record's HarnessKind: claude gates the
// Claude rows, muse the muse rows.
func gatePluginRowsFor(kind string, in *doctor.MasterMindCheckInput) {
	switch kind {
	case "claude":
		in.Claude = true
	case "muse":
		in.Muse = true
	}
}

// museSessionGate opens the muse MasterMind row when MUSE_SESSION_ID is set.
// The id is read directly because mastermind.Detect in
// internal/mastermind/ident.go answers "muse" only once the parallel change
// lands there; Detect owns session detection and this gate is the duplicate
// to converge on it. A stray id on a host with no muse candidate, record or
// binary reports no rows, the same shape as the Claude gating.
func museSessionGate(rt relevo.Runtime, in *doctor.MasterMindCheckInput) {
	if os.Getenv("MUSE_SESSION_ID") == "" {
		return
	}
	if !in.Muse && !museBinaryPresent() {
		return
	}
	in.MuseSession = true
	museMasterMindFacts(rt, in)
}

// museMasterMindFacts fills the session-row fields for a muse session exactly
// as mastermindCheckInput's claude branch fills them. It resolves through
// mastermind.Resolve, which reads MUSE_SESSION_ID only once Detect in
// internal/mastermind/ident.go knows the muse kind; until then the row reads
// FAIL (no record resolved), never guesses an identity.
func museMasterMindFacts(rt relevo.Runtime, in *doctor.MasterMindCheckInput) {
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
	if lbl := chatResolver().Resolve(context.Background(), rec.HarnessKind, rec.SessionID); lbl != (chatlabel.Label{}) {
		in.Chat = lbl.String()
	}
	if rt.Channels != nil {
		if c, cerr := rt.Channels.Live(rec.ID, rt.Now()); cerr == nil && c != nil {
			in.ClaimLive = true
		}
	}
}

// museBinaryPresent is the direct exec.LookPath this package already makes
// of the host's process table; doctor's Env is not threaded into
// mastermindCheckInput. A muse with no PATH entry is not a muse session.
func museBinaryPresent() bool {
	_, err := exec.LookPath("muse")
	return err == nil
}
