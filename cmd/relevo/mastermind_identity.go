package main

import (
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// mastermindIdentity resolves this session's mastermind and keeps the reason it
// could not. A caller that only needs to know whether one resolved reads
// mastermindFilter; a caller that has to tell "no mastermind anywhere" from
// "this session names one I cannot" -- the status scope, which refuses rather
// than guesses -- reads the error.
//
// Nothing here reads the process itself: the same inputs go to Resolve as they
// do for every other verb.
func mastermindIdentity(rt relevo.Runtime) (mastermind.Record, error) {
	if rt.MasterMinds == nil {
		return mastermind.Record{}, mastermind.ErrNoMasterMind
	}
	var now time.Time
	if rt.Now != nil {
		now = rt.Now()
	}
	cwd, _ := os.Getwd()
	rec, _, err := mastermind.Resolve(rt.MasterMinds, mastermind.ResolveInput{
		Env:             os.Getenv,
		PPID:            os.Getppid(),
		ProcStart:       rt.ProcStart,
		Now:             now,
		CWD:             cwd,
		OpencodeSession: rt.OpencodeSession,
	})
	if err != nil {
		return mastermind.Record{}, err
	}
	return rec, nil
}

// mastermindFilter resolves this session's mastermind for the commands that
// filter by it without requiring one: `relevo consent` and unbind's scope
// helper. A miss is not an error there -- the caller keeps its own fallback --
// and neither is a Runtime with no registry (tests).
func mastermindFilter(rt relevo.Runtime) (mastermind.Record, bool) {
	rec, err := mastermindIdentity(rt)
	if err != nil {
		return mastermind.Record{}, false
	}
	return rec, true
}
