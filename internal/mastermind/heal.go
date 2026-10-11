package mastermind

import "errors"

// healGone re-points a gone record at the calling session's live host, the
// way init's reattach would, so a resumed session whose SessionStart hook
// failed does not stay unreachable. It heals only when the caller provably is
// the record's session: same harness kind and session id, a host pid that
// reads back through ProcStart, and no other record holding that host. Every
// other case, and every write failure, returns rec unchanged: a heal never
// fails the verb that triggered it.
func healGone(reg Registry, rec Record, in ResolveInput, env func(string) string) Record {
	if RecordState(rec, in.ProcStart) != StateGone {
		return rec
	}
	ident, ok := Detect(env, in.PPID)
	if !ok || ident.Kind != rec.HarnessKind || ident.SessionID != rec.SessionID {
		return rec
	}
	if ident.HostPID <= 0 || in.ProcStart == nil {
		return rec
	}
	startedAt, err := in.ProcStart(ident.HostPID)
	if err != nil {
		return rec
	}
	holder, err := reg.ByHost(ident.HostPID, startedAt)
	switch {
	case err == nil && holder.ID != rec.ID:
		return rec
	case err != nil && !errors.Is(err, ErrNotFound):
		return rec
	}
	updated, err := reg.SetHost(rec.ID, ident.HostPID, startedAt)
	if err != nil {
		return rec
	}
	return updated
}
