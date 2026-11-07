package relevo

// JoinOutcome is the one shape `db sync status` renders for a machine's join
// rows: a recorded failure, a marker with nothing recorded against it, or
// neither.
type JoinOutcome int

const (
	// JoinNone is a machine with neither a join marker nor a failure recorded.
	JoinNone JoinOutcome = iota
	// JoinJoining is a marker with no failure recorded against it: the join is
	// still the daemon's to finish.
	JoinJoining
	// JoinFailed is a failure recorded beside the marker: nothing runs the
	// join, whatever else is set.
	JoinFailed
)

// JoinState reduces a machine's join rows to the one shape status renders. A
// failure wins over every combination, because a join that recorded one is
// failed even where a marker beside it says otherwise; a marker alone is a
// join in progress. The enabled mark is read with the rows because the rule
// is over the machine's marks, though today's precedence never branches on
// it: a join with sync on and a marker is owed history still sending, which
// keeps today's wording by construction.
func JoinState(enabled, hasMarker, hasFailure bool) JoinOutcome {
	if hasFailure {
		return JoinFailed
	}
	if hasMarker {
		return JoinJoining
	}
	return JoinNone
}

// SuppressUnlockHint reports whether the status line keeps `key: locked` but
// drops the unlock instruction. Unlock needs sync on, so naming it while the
// machine is off in a join sends the reader to a command that cannot work
// yet; the resume hint beside it says what does.
func SuppressUnlockHint(key string, enabled, hasMarker, hasFailure bool) bool {
	return key == "locked" && !enabled && (hasMarker || hasFailure)
}
