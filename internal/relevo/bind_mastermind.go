package relevo

import (
	"errors"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/store"
)

// resolveVerbMasterMind is how bind, add, fork and ask get their mastermind:
// flag > env > host > session, through the registry Runtime.MasterMinds names.
// ref is the caller's --mastermind value; "" resolves from the environment, the
// caller's parent process and its harness session, in that order.
//
// mastermind.ErrNoMasterMind becomes ErrNoMasterMindSession, and so does a Runtime with
// no registry configured: nothing derives a mastermind from a pane.
func resolveVerbMasterMind(rt Runtime, ref string) (mastermind.Record, bool, error) {
	return resolveVerbMasterMindHeal(rt, ref, true)
}

// resolveVerbMasterMindHeal is resolveVerbMasterMind with the gone-record heal
// switchable: a caller that may run in the daemon passes false, since the
// daemon's environment is not the calling session's.
func resolveVerbMasterMindHeal(rt Runtime, ref string, heal bool) (mastermind.Record, bool, error) {
	if rt.MasterMinds == nil {
		return mastermind.Record{}, false, ErrNoMasterMindSession
	}
	var now time.Time
	if rt.Now != nil {
		now = rt.Now()
	}
	cwd, _ := os.Getwd()
	rec, _, err := mastermind.Resolve(rt.MasterMinds, mastermind.ResolveInput{
		Flag:            ref,
		Env:             os.Getenv,
		PPID:            os.Getppid(),
		ProcStart:       rt.ProcStart,
		MuseHost:        rt.MuseHost,
		Now:             now,
		CWD:             cwd,
		OpencodeSession: rt.OpencodeSession,
		Heal:            heal,
	})
	switch {
	case err == nil:
		return rec, true, nil
	default:
		var unreg mastermind.ErrUnregisteredSession
		if errors.As(err, &unreg) && unreg.Kind == "opencode" {
			rec, _, initErr := mastermind.Init(rt.MasterMinds, mastermind.InitInput{
				Kind:      "opencode",
				SessionID: unreg.SessionID,
				CWD:       cwd,
				Now:       now,
			})
			if initErr != nil {
				return mastermind.Record{}, false, initErr
			}
			return rec, true, nil
		}
		if errors.Is(err, mastermind.ErrNoMasterMind) {
			return mastermind.Record{}, false, ErrNoMasterMindSession
		}
		return mastermind.Record{}, false, err
	}
}

// recordEndpoint is the mastermind endpoint a resolved record supplies:
// its kind and session. MasterMind.PaneID is written by nothing; it
// stays a field only so an older bind.json loads.
func recordEndpoint(rec mastermind.Record) store.Endpoint {
	return store.Endpoint{
		Kind:      rec.HarnessKind,
		SessionID: rec.SessionID,
	}
}
