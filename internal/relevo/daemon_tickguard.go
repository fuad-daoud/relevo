package relevo

import (
	"github.com/fuad-daoud/relevo/internal/store"
)

// doneTickSkip decides whether this tick can do nothing at all for a done
// binding, and returns the revision a full pass should be pinned to when it
// cannot skip.
//
// The binding's revision covers every fact the done path reads -- the record
// JSON with its consults and owed-halt marker, the log's extent, the sealed
// files, the binding directory -- so a binding whose revision has not moved
// since its last full pass is left alone. The revision cannot see three
// things the done path can still owe, and each is a live check: a payload a
// push route admitted and never confirmed, whose read-back and horizon expiry
// patch the entry in place and no other route can take it; a round whose
// files still wait in the binding directory for a seal that can hinge on the
// stream going quiet or the artifact cap rising, neither a store write; and
// an open consult or owed halt, which the tick's own binding already names.
//
// Active and paused bindings never take the guard: their reconcile reads the
// builder process's liveness, clocks, the server's state and delivery
// confirmation, none of which a revision covers, however still it sits. The
// decision costs the one read that returns these facts, beside the binding
// the tick's list already returned.
func (d *Daemon) doneTickSkip(b store.Binding) (string, bool) {
	if b.State != store.StateDone || b.OwedHalt != nil {
		return "", false
	}
	for _, c := range b.Consults {
		if store.ConsultActive(c) {
			return "", false
		}
	}
	st, err := d.rt.Store.TickState(b.Name)
	if err != nil {
		// A revision that cannot be read is not a match: run the pass and
		// let the next tick read again.
		return "", false
	}
	if st.AdmitsOpen > 0 || st.RoundsOnDisk > 0 {
		return st.Rev, false
	}
	return st.Rev, d.tickPassed[b.Name] == st.Rev
}

// noteTickPassed pins the done binding name to the revision rev its full pass
// ran at, once that pass succeeded: a pass that failed keeps no memory, and
// the next tick runs it again. rev is empty for every binding the guard did
// not read, so only done bindings ever enter the map.
func (d *Daemon) noteTickPassed(name, rev string) {
	if rev != "" {
		d.tickPassed[name] = rev
	}
}

// pruneTickPassed drops the records of bindings that left the tick's list, so
// an unbind cannot leave a revision behind for a re-added name to match.
func (d *Daemon) pruneTickPassed(bindings []store.Binding) {
	if len(d.tickPassed) == 0 {
		return
	}
	keep := make(map[string]bool, len(bindings))
	for _, b := range bindings {
		keep[b.Name] = true
	}
	for name := range d.tickPassed {
		if !keep[name] {
			delete(d.tickPassed, name)
		}
	}
}
