package doctor

import (
	"context"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/chatlabel"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// mastermindConsent fills the repository consent fields: whether the working
// directory names a repository and what it answers. Each read is best-effort
// like the rest of the row: a store or git read that fails leaves the row off.
func mastermindConsent(rt relevo.Runtime, in *MasterMindCheckInput, deps Deps) {
	if rt.Git == nil || in.Repo == "" {
		return
	}
	ref := deps.RepoOf(context.Background(), rt, in.Repo)
	in.RepoKnown = deps.RepoKnown(ref)
	if rt.Store == nil {
		return
	}
	d, err := rt.Store.DB()
	if err != nil {
		return
	}
	in.ConsentKnown = true
	if !in.RepoKnown {
		return
	}
	if c, err := d.RepoConsent(ref); err == nil {
		in.Consent = c
	}
}

// mastermindRecords lists the known masterminds, or nothing when there is no
// registry or it cannot be read.
func mastermindRecords(rt relevo.Runtime) []mastermind.Record {
	if rt.MasterMinds == nil {
		return nil
	}
	recs, err := rt.MasterMinds.List()
	if err != nil {
		return nil
	}
	return recs
}

// liveMasterMindIDs names the masterminds with a live binding: a stale record
// on one of these is still in use, so it is never named stale.
func liveMasterMindIDs(rt relevo.Runtime) map[string]bool {
	live := map[string]bool{}
	if rt.Store == nil {
		return live
	}
	bindings, err := rt.Store.List()
	if err != nil {
		return live
	}
	for _, b := range bindings {
		if b.State != store.StateDone && b.MasterMindID != "" {
			live[b.MasterMindID] = true
		}
	}
	return live
}

// isStaleMasterMind reports whether a record is old and has no live binding.
func isStaleMasterMind(rec mastermind.Record, live map[string]bool, cutoff time.Time) bool {
	return rec.SeenAt.Before(cutoff) && !live[rec.ID]
}

// mastermindClaudeSession fills the session fields when this process runs
// inside Claude Code: the detection flag, the push-route child check and the
// resolved mastermind with its chat label and live claim.
func mastermindClaudeSession(rt relevo.Runtime, in *MasterMindCheckInput, deps Deps) {
	ident, ok := mastermind.Detect(os.Getenv, os.Getppid())
	if !ok || ident.Kind != "claude" {
		return
	}
	in.Detected = true
	in.MCPChild = HasMCPChild(hostChildProcesses(ident.HostPID))
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
	// The mastermind's chat label, read here so the row can
	// name the mastermind as the harness does. An empty label
	// leaves the detail byte-identical.
	if lbl := deps.ChatResolver.Resolve(context.Background(), rec.HarnessKind, rec.SessionID); lbl != (chatlabel.Label{}) {
		in.Chat = lbl.String()
	}
	if rt.Channels == nil {
		return
	}
	if c, cerr := rt.Channels.Live(rec.ID, rt.Now()); cerr == nil && c != nil {
		in.ClaimLive = true
	}
}
