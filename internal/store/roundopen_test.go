package store

import "testing"

// TestRoundOpenIsTheSingleDefinition pins the predicate the daemon, the served
// view, `stop` and the history ingest all read. Every case here is a shape one
// of those readers gets wrong when it spells the condition its own way.
func TestRoundOpenIsTheSingleDefinition(t *testing.T) {
	t.Parallel()

	prompt := LogEntry{Round: 1, Direction: DirToBuilder, Kind: KindPrompt, Confirmed: true}
	legacyPlan := LogEntry{Round: 1, Direction: DirToBuilder, Kind: "plan", Confirmed: true}
	report := LogEntry{Round: 1, Direction: DirToMasterMind, Kind: KindReport, Confirmed: true}
	nudge := LogEntry{Round: 1, Direction: DirToBuilder, Kind: KindPrompt, Note: NudgeNote, Confirmed: true}
	otherRound := LogEntry{Round: 2, Direction: DirToBuilder, Kind: KindPrompt, Confirmed: true}

	cases := []struct {
		name    string
		entries []LogEntry
		round   int
		want    bool
	}{
		{"empty log", nil, 1, false},
		{"a prompt and no report", []LogEntry{prompt}, 1, true},
		{"a prompt then a report", []LogEntry{prompt, report}, 1, false},
		{"a re-sent round: two prompts, no report", []LogEntry{prompt, prompt}, 1, true},
		// The legacy plan spelling is a prompt too: a round written before the
		// rename is open, not unknown.
		{"the legacy plan spelling", []LogEntry{legacyPlan}, 1, true},
		// A nudge reminds about a round; it does not send one, so it cannot
		// open a round and cannot close one either.
		{"a nudge alone", []LogEntry{nudge}, 1, false},
		{"a nudge after a real prompt", []LogEntry{prompt, nudge}, 1, true},
		{"a nudge reported on", []LogEntry{prompt, report, nudge}, 1, false},
		// Only the named round counts.
		{"a prompt for another round", []LogEntry{otherRound}, 1, false},
		{"a report for another round", []LogEntry{prompt, {Round: 2, Direction: DirToMasterMind, Kind: KindReport}}, 1, true},
		// A report is a report whichever direction it names, but the to-builder
		// direction is the only other one the log uses and must not close a
		// round on its own.
		{"a to-builder prompt cannot be its own report", []LogEntry{prompt, prompt}, 1, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RoundOpen(c.entries, c.round); got != c.want {
				t.Errorf("RoundOpen = %v, want %v", got, c.want)
			}
		})
	}
}

// TestHasPromptAndHasKindAgreeWithRoundOpen keeps the two halves of the shared
// predicate spellable on their own, since readers ask about one kind at a time.
func TestHasPromptAndHasKindAgreeWithRoundOpen(t *testing.T) {
	t.Parallel()

	prompt := LogEntry{Round: 3, Direction: DirToBuilder, Kind: KindPrompt, Confirmed: true}
	report := LogEntry{Round: 3, Direction: DirToMasterMind, Kind: KindReport, Confirmed: true}

	if !HasPrompt([]LogEntry{prompt}, 3) {
		t.Error("HasPrompt = false for a prompt of that round")
	}
	if HasPrompt([]LogEntry{prompt}, 4) {
		t.Error("HasPrompt = true for another round")
	}
	if !HasKind([]LogEntry{report}, 3, DirToMasterMind, KindReport) {
		t.Error("HasKind = false for a report of that round")
	}
	if HasKind([]LogEntry{report}, 3, DirToBuilder, KindReport) {
		t.Error("HasKind = true for the wrong direction")
	}
	// The two halves must compose into the whole: a prompt with no report is
	// open, and the report is what closes it.
	if !RoundOpen([]LogEntry{prompt}, 3) {
		t.Error("RoundOpen = false for a prompt with no report")
	}
	if RoundOpen([]LogEntry{prompt, report}, 3) {
		t.Error("RoundOpen = true once the report landed")
	}
}
