package relevo

import "testing"

// The table pins the precedence the renderer trusts: a failure beside the
// marker reads as failed whatever else is set, so status never shows a dead
// join as running; a marker alone reads as joining on or off, so a locked
// join's owed history keeps today's wording; neither reads as at rest.
// Mutation: flip one condition and the table goes red.
func TestJoinStateReadsTheFailureBesideTheMarker(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                    string
		enabled, marker, failed bool
		want                    JoinOutcome
	}{
		{"a machine at rest is none", false, false, false, JoinNone},
		{"an enabled machine at rest is none", true, false, false, JoinNone},
		{"an off machine's marker is joining", false, true, false, JoinJoining},
		{"an on machine's marker is joining", true, true, false, JoinJoining},
		{"a failure with no marker is failed", false, false, true, JoinFailed},
		{"a failure on an enabled machine is failed", true, false, true, JoinFailed},
		{"a failure beside a marker is failed", false, true, true, JoinFailed},
		{"a failure beside a marker on an enabled machine is failed", true, true, true, JoinFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := JoinState(tc.enabled, tc.marker, tc.failed); got != tc.want {
				t.Errorf("JoinState(enabled=%t, marker=%t, failed=%t) = %v, want %v",
					tc.enabled, tc.marker, tc.failed, got, tc.want)
			}
		})
	}
}

// The table pins each conjunct the line renderer trusts: only a locked key
// drops the instruction, only while the machine is off, and only while a
// marker or a failure says a join is what is off. Every other shape keeps
// whatever dbSyncKeyClause renders.
// Mutation: flip the || to && and the single-row cases go red.
func TestSuppressUnlockHintNeedsALockedKeyAnOffMachineAndAJoin(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                    string
		key                     string
		enabled, marker, failed bool
		want                    bool
	}{
		{"locked off with a marker drops the hint", "locked", false, true, false, true},
		{"locked off with a failure drops the hint", "locked", false, false, true, true},
		{"locked off with both drops the hint", "locked", false, true, true, true},
		{"locked off with neither keeps the hint", "locked", false, false, false, false},
		{"locked on with a marker keeps the hint", "locked", true, true, false, false},
		{"locked on with a failure keeps the hint", "locked", true, false, true, false},
		{"locked on with both keeps the hint", "locked", true, true, true, false},
		{"locked on with neither keeps the hint", "locked", true, false, false, false},
		{"unlocked off with both keeps the hint", "unlocked", false, true, true, false},
		{"stale off with both keeps the hint", "stale", false, true, true, false},
		{"no key off with a marker keeps the hint", "none", false, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := SuppressUnlockHint(tc.key, tc.enabled, tc.marker, tc.failed); got != tc.want {
				t.Errorf("SuppressUnlockHint(key=%q, enabled=%t, marker=%t, failed=%t) = %t, want %t",
					tc.key, tc.enabled, tc.marker, tc.failed, got, tc.want)
			}
		})
	}
}
