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
// mastermindCheckInput's claude branch fills them. The record resolves
// first: a doctor under `sh -c` must check the resolved host's children,
// not the short-lived shell's.
func museMasterMindFacts(rt relevo.Runtime, in *MasterMindCheckInput, resolver chatlabel.Resolver) {
	ppid := os.Getppid()
	var museHost int
	var museOK bool
	if rt.MuseHost != nil {
		museHost, museOK = rt.MuseHost(ppid)
	}
	if rt.MasterMinds != nil {
		rec, _, err := mastermind.Resolve(rt.MasterMinds, mastermind.ResolveInput{
			Env:       os.Getenv,
			PPID:      ppid,
			ProcStart: rt.ProcStart,
			MuseHost:  rt.MuseHost,
			Now:       rt.Now(),
			// Re-point a gone record at this live session, so the row
			// below reads the live host instead of the stale one.
			Heal: true,
		})
		if err == nil {
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
	}
	// A nil ProcStart leaves liveness unknowable: the record reads as not
	// gone, so it keeps its order below.
	resolvedGone := false
	if in.Resolved != nil && rt.ProcStart != nil {
		resolvedGone = mastermind.RecordState(*in.Resolved, rt.ProcStart) == mastermind.StateGone
	}
	in.MCPChild = HasMCPChild(hostChildProcesses(museMCPHostPID(in.Resolved, resolvedGone, museHost, museOK, ppid)))
}

// museMCPHostPID picks whose children prove the session's tools are up: the
// resolved record's host, unless the record is gone and the muse walk
// verified a live ancestor, in which case that ancestor, else the direct
// parent. A gone record names a dead host, so its children prove nothing;
// the walk host is already verified by the caller. Pure, so the
// shell-sibling order is testable without /proc.
func museMCPHostPID(resolved *mastermind.Record, resolvedGone bool, museHost int, ok bool, ppid int) int {
	if resolvedGone {
		if ok && museHost > 0 {
			return museHost
		}
		return ppid
	}
	if resolved != nil && resolved.HostPID > 0 {
		return resolved.HostPID
	}
	if ok && museHost > 0 {
		return museHost
	}
	return ppid
}

// museBinaryPresent is the direct exec.LookPath this package already makes;
// doctor's Env is not threaded into mastermindCheckInput. A muse with no
// PATH entry is not a muse session.
func museBinaryPresent() bool {
	_, err := exec.LookPath("muse")
	return err == nil
}
