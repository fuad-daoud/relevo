package ingest

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestDeriveOutcome(t *testing.T) {
	cases := []struct {
		name    string
		events  []store.LogEntry
		members map[string]bool
		b       store.Binding
		want    string
	}{
		{
			"report beats a done marker",
			[]store.LogEntry{{Round: 1, Kind: store.KindReport}},
			map[string]bool{donePathBase(1): true},
			store.Binding{Round: 1, State: store.StateActive},
			db.OutcomeReported,
		},
		{
			"done marker with no report",
			[]store.LogEntry{{Round: 1, Kind: store.KindPrompt}},
			map[string]bool{donePathBase(1): true},
			store.Binding{Round: 1, State: store.StateActive},
			db.OutcomeDoneNoReport,
		},
		{
			"exit on the current round",
			[]store.LogEntry{{Round: 1, Kind: store.KindPrompt}, {Round: 1, Kind: store.KindExit}},
			map[string]bool{},
			store.Binding{Round: 1, State: store.StateNeedsYou},
			db.OutcomeExited,
		},
		{
			"needs_you on the current round",
			[]store.LogEntry{{Round: 1, Kind: store.KindPrompt}},
			map[string]bool{},
			store.Binding{Round: 1, State: store.StateNeedsYou},
			db.OutcomeHalted,
		},
		{
			"halt text names the round",
			[]store.LogEntry{{Round: 1, Kind: store.KindPrompt}},
			map[string]bool{},
			store.Binding{Round: 1, State: store.StateActive, Halt: "builder exited (code 1) without a report"},
			db.OutcomeHalted,
		},
		{
			"switch on a non-current round",
			[]store.LogEntry{{Round: 1, Kind: store.KindPrompt}, {Round: 1, Kind: store.KindSwitch}},
			map[string]bool{},
			store.Binding{Round: 2, State: store.StateActive},
			db.OutcomeSwitched,
		},
		{
			"nothing",
			[]store.LogEntry{{Round: 1, Kind: store.KindPrompt}},
			map[string]bool{},
			store.Binding{Round: 2, State: store.StateActive},
			db.OutcomeOpen,
		},
	}
	for _, c := range cases {
		if got := deriveOutcome(c.events, 1, c.b, c.members); got != c.want {
			t.Errorf("%s: deriveOutcome = %q, want %q", c.name, got, c.want)
		}
	}
}

type candidateCase struct {
	name    string
	events  []store.LogEntry
	b       store.Binding
	n       int
	wantTok string
	want    candidate.Ref
}

func TestCandidateForRound(t *testing.T) {
	for _, c := range candidateForRoundCases() {
		tok, ref, ok := candidateForRound(c.events, c.n, c.b)
		if !ok {
			t.Errorf("%s: ok = false, want true", c.name)
			continue
		}
		if tok != c.wantTok || ref != c.want {
			t.Errorf("%s: got %q/%+v, want %q/%+v", c.name, tok, ref, c.wantTok, c.want)
		}
	}
}

func candidateForRoundCases() []candidateCase {
	return []candidateCase{
		{
			"pick note",
			[]store.LogEntry{{Round: 1, Kind: store.KindPick, Note: "picked opencode/openrouter/z-ai/glm-5.3-flash on host1: initial spawn"}},
			store.Binding{}, 1,
			"opencode/openrouter/z-ai/glm-5.3-flash",
			candidate.Ref{Harness: "opencode", Provider: "openrouter", Model: "z-ai/glm-5.3-flash"},
		},
		{
			"switch arrow note",
			[]store.LogEntry{{Round: 2, Kind: store.KindSwitch, Note: "switched opencode/openrouter/z-ai/glm-5.3-flash -> agy/google/gemini-3.8-flash-high"}},
			store.Binding{}, 2,
			"agy/google/gemini-3.8-flash-high",
			candidate.Ref{Harness: "agy", Provider: "google", Model: "gemini-3.8-flash-high"},
		},
		{
			"falls back to the binding candidate",
			nil,
			store.Binding{BuilderCandidate: "claude/anthropic/sonnet"}, 1,
			"claude/anthropic/sonnet",
			candidate.Ref{Harness: "claude", Provider: "anthropic", Model: "sonnet"},
		},
		{
			"keeps the effort hash verbatim, strips it from the ref",
			[]store.LogEntry{{Round: 1, Kind: store.KindPick, Note: "picked opencode/cline-pass/cline-pass/glm-5.3-flash#high on host1: spawn"}},
			store.Binding{}, 1,
			"opencode/cline-pass/cline-pass/glm-5.3-flash#high",
			candidate.Ref{Harness: "opencode", Provider: "cline-pass", Model: "cline-pass/glm-5.3-flash"},
		},
		{
			"last pick or switch wins",
			[]store.LogEntry{
				{Round: 1, Kind: store.KindPick, Note: "picked a/b/c for builder: order #1"},
				{Round: 1, Kind: store.KindSwitch, Note: "switched builder (exited (code 1) without a report): picked claude/anthropic/sonnet for builder: order #5"},
			},
			store.Binding{BuilderCandidate: "x/y/z"}, 1,
			"claude/anthropic/sonnet",
			candidate.Ref{Harness: "claude", Provider: "anthropic", Model: "sonnet"},
		},
		{
			"a consult pick is skipped",
			[]store.LogEntry{
				{Round: 1, Kind: store.KindPick, Note: "picked opencode/cline-pass/cline-pass/glm-5.3-flash#high for builder: order #1"},
				{Round: 1, Kind: store.KindPick, Note: "picked claude/anthropic/sonnet for reviewer: order #1"},
			},
			store.Binding{}, 1,
			"opencode/cline-pass/cline-pass/glm-5.3-flash#high",
			candidate.Ref{Harness: "opencode", Provider: "cline-pass", Model: "cline-pass/glm-5.3-flash"},
		},
		{
			"an only consult pick falls back",
			[]store.LogEntry{{Round: 1, Kind: store.KindPick, Note: "picked claude/anthropic/sonnet for reviewer: order #1"}},
			store.Binding{BuilderCandidate: "claude/anthropic/sonnet"}, 1,
			"claude/anthropic/sonnet",
			candidate.Ref{Harness: "claude", Provider: "anthropic", Model: "sonnet"},
		},
	}
}

func TestParsePickNoteForms(t *testing.T) {
	tests := []struct {
		note string
		want string
	}{
		{"picked claude/anthropic/sonnet for builder: order #1", "claude/anthropic/sonnet"},
		{"picked opencode/openrouter/z-ai/glm-5.3-flash on host1: spawn", "opencode/openrouter/z-ai/glm-5.3-flash"},
		{"picked agy/test/m: explicit, policy bypassed", "agy/test/m"},
		{"picked codex/openai/gpt-5.6-terra:high for builder: order #2", "codex/openai/gpt-5.6-terra:high"},
		{"picked opencode/cline-pass/cline-pass/glm-5.3-flash#high on h: s", "opencode/cline-pass/cline-pass/glm-5.3-flash#high"},
		{"picked claude/anthropic/sonnet on zen: server's pick; placement zen (explicit)", "claude/anthropic/sonnet"},
		{"picked agy/test/m for builder: order #1; placement zen (actor); skipped backup (unreachable)", "agy/test/m"},
		{"picked x/y/z", "x/y/z"},
		{"nothing picked", ""},
	}
	for _, tt := range tests {
		if got := parsePickNote(tt.note); got != tt.want {
			t.Errorf("parsePickNote(%q) = %q, want %q", tt.note, got, tt.want)
		}
	}
}

func TestParseSwitchNoteForms(t *testing.T) {
	tests := []struct {
		note string
		want string
	}{
		{"switched builder (exited (code 1) without a report): picked claude/anthropic/sonnet for builder: order #5", "claude/anthropic/sonnet"},
		{"switched a/b/c -> d/e/f", "d/e/f"},
		{"switched builder (reason): cannot switch", ""},
	}
	for _, tt := range tests {
		if got := parseSwitchNote(tt.note); got != tt.want {
			t.Errorf("parseSwitchNote(%q) = %q, want %q", tt.note, got, tt.want)
		}
	}
}

// TestIsOtherActorPick pins the rule: only "picked <tok> for <role>:" with a role
// other than the binding's own actor is another actor's pick.
func TestIsOtherActorPick(t *testing.T) {
	tests := []struct {
		note  string
		actor string
		want  bool
	}{
		{"picked claude/anthropic/sonnet for reviewer: order #1", "builder", true},
		{"picked a/b/c for verify: sole candidate", "builder", true},
		{"picked a/b/c for builder: order #1", "builder", false},
		{"picked a/b/c for designer: order #1", "designer", false},
		{"picked a/b/c for designer: order #1", "builder", true},
		{"picked a/b/c on host1: spawn", "builder", false},
		{"picked a/b/c", "builder", false},
		{"picked a/b/c for : x", "builder", false},
		{"switched builder (exited (code 1) without a report): picked a/b/c for builder: order #5", "builder", false},
	}
	for _, tt := range tests {
		if got := isOtherActorPick(tt.note, tt.actor); got != tt.want {
			t.Errorf("isOtherActorPick(%q, %q) = %v, want %v", tt.note, tt.actor, got, tt.want)
		}
	}
}

func TestSwitchesForRound(t *testing.T) {
	events := []store.LogEntry{
		{Round: 1, Kind: store.KindSwitch},
		{Round: 1, Kind: store.KindSwitch},
		{Round: 2, Kind: store.KindSwitch},
		{Round: 1, Kind: store.KindPrompt},
	}

	for round, want := range map[int]int{1: 2, 2: 1, 3: 0} {
		if got := switchesForRound(events, round); got != want {
			t.Errorf("switchesForRound(round %d) = %d, want %d", round, got, want)
		}
	}
}

// TestSwitchesSkipRelaunch pins that a switch replacing a lost process with the
// same candidate is not counted.
func TestSwitchesSkipRelaunch(t *testing.T) {
	events := []store.LogEntry{
		{Round: 1, Kind: store.KindSwitch, Note: "switched builder (rate-limited): picked agy/google/x for builder: order #1"},
		{Round: 1, Kind: store.KindSwitch, Note: "relaunched builder (lost to a daemon restart at 2026-09-24T10:00:00Z): picked agy/google/x for builder: same candidate, not counted"},
		{Round: 1, Kind: store.KindSwitch, Note: "resumed session sess-1 builder (lost to a daemon restart at 2026-09-24T10:00:00Z): picked agy/google/x for builder: same candidate, not counted"},
	}

	if got := switchesForRound(events, 1); got != 1 {
		t.Errorf("switchesForRound(round 1) = %d, want 1", got)
	}
}

// TestCandidateForRoundCarriesTheEarlierRoundsBuilder pins that a round with no
// pick or switch of its own takes the builder the earlier rounds left running,
// not the binding's live candidate, which a later round may have changed.
// A server's copy of a binding can hold no pick, so a round before the first
// switch learns its candidate from what that switch replaced, not from the
// binding's newest one.
func TestCandidateForRoundReadsWhatAServerSwitchReplaced(t *testing.T) {
	events := []store.LogEntry{
		{Round: 2, Kind: store.KindSwitch, Note: "switched builder (gated while queued): picked claude/anthropic/b for builder: order #2; replaced opencode/zen/a"},
	}
	b := store.Binding{Round: 2, BuilderCandidate: "claude/anthropic/b"}

	tok, _, ok := candidateForRound(events, 1, b)
	if !ok || tok != "opencode/zen/a" {
		t.Errorf("candidateForRound(round 1) = %q, %v, want what the switch replaced", tok, ok)
	}
	tok, _, ok = candidateForRound(events, 2, b)
	if !ok || tok != "claude/anthropic/b" {
		t.Errorf("candidateForRound(round 2) = %q, %v, want the switch's pick", tok, ok)
	}
}

func TestCandidateForRoundCarriesTheEarlierRoundsBuilder(t *testing.T) {
	events := []store.LogEntry{
		{Round: 1, Kind: store.KindPick, Note: "picked opencode/zen/space-bunny-free for builder: order #1"},
		{Round: 3, Kind: store.KindSwitch, Note: "switched builder (rate-limited): picked claude/anthropic/claude-sonnet-5-5:medium for builder: order #2"},
	}
	b := store.Binding{Round: 3, BuilderCandidate: "claude/anthropic/claude-sonnet-5-5:medium"}

	tok, _, ok := candidateForRound(events, 2, b)
	if !ok || tok != "opencode/zen/space-bunny-free" {
		t.Errorf("candidateForRound(round 2) = %q, %v, want the pick of round 1", tok, ok)
	}
	tok, _, ok = candidateForRound(events, 3, b)
	if !ok || tok != "claude/anthropic/claude-sonnet-5-5:medium" {
		t.Errorf("candidateForRound(round 3) = %q, %v, want the switch of round 3", tok, ok)
	}
}

// TestCandidateForRoundBeforeTheFirstSwitch pins a served binding whose log holds
// no pick, only what a poll saw: each round names the candidate that ran it, not
// the binding's newest one.
func TestCandidateForRoundBeforeTheFirstSwitch(t *testing.T) {
	const (
		muse = "muse/meta/muse-spark-1.3:xhigh"
		qwen = "opencode/alibaba-tp/qwen3.8-flash#xhigh"
	)
	poll := func(round int, from, to string) store.LogEntry {
		return store.LogEntry{Round: round, Kind: store.KindSwitch, Note: store.PollSwitchNotePrefix + "zen: " + from + " -> " + to}
	}
	tests := []struct {
		name    string
		events  []store.LogEntry
		current string
		want    map[int]string
	}{
		{
			name:    "one switch late",
			events:  []store.LogEntry{poll(9, muse, qwen)},
			current: qwen,
			want:    map[int]string{1: muse, 8: muse, 9: qwen, 10: qwen},
		},
		{
			name:    "one switch early",
			events:  []store.LogEntry{poll(2, qwen, muse)},
			current: muse,
			want:    map[int]string{1: qwen, 2: muse, 3: muse},
		},
		{
			name:    "switched and back",
			events:  []store.LogEntry{poll(4, muse, qwen), poll(6, qwen, muse)},
			current: muse,
			want:    map[int]string{1: muse, 3: muse, 4: qwen, 5: qwen, 6: muse, 7: muse},
		},
		{
			name: "the first of two later switches names it, whatever the log order",
			events: []store.LogEntry{
				poll(6, qwen, "c/d/e"), poll(4, muse, qwen),
			},
			current: "c/d/e",
			want:    map[int]string{1: muse},
		},
		{
			name: "a shipped note ahead of the poll's does not hide the old candidate",
			events: []store.LogEntry{
				{Round: 3, Kind: store.KindSwitch, Note: "switched builder (rate-limited): picked " + qwen + " for builder: order #2"},
				poll(3, muse, qwen),
			},
			current: qwen,
			want:    map[int]string{1: muse, 3: qwen},
		},
		{
			name: "only a shipped note names no old candidate",
			events: []store.LogEntry{
				{Round: 3, Kind: store.KindSwitch, Note: "switched builder (a -> b): picked " + qwen + " for builder: order #2"},
			},
			current: qwen,
			want:    map[int]string{1: qwen},
		},
		{
			name:    "a poll with no old candidate names none",
			events:  []store.LogEntry{poll(3, "", qwen)},
			current: qwen,
			want:    map[int]string{1: qwen},
		},
		{
			name: "entries that are not a switch are not the next switch",
			events: []store.LogEntry{
				{Round: 2, Kind: store.KindSwitch, Note: "nudged builder -> again"},
				{Round: 2, Kind: store.KindSwitch, Note: "switched builder (reason): cannot switch"},
				{Round: 3, Kind: store.KindSwitch, Note: "resumed session s1 builder (lost to a daemon restart at t): picked " + muse + " for builder: same candidate, not counted"},
				{Round: 3, Kind: store.KindExit, Note: "switched x -> y"},
				poll(4, muse, qwen),
			},
			current: qwen,
			want:    map[int]string{1: muse},
		},
		{
			name: "a note naming no one does not erase the round's switch",
			events: []store.LogEntry{
				{Round: 1, Kind: store.KindPick, Note: "picked " + muse + " for builder: order #1"},
				poll(2, muse, qwen),
				{Round: 2, Kind: store.KindSwitch, Note: "nudged builder"},
				{Round: 2, Kind: store.KindSwitch, Note: "switched builder (reason): cannot switch"},
			},
			current: "c/d/e",
			want:    map[int]string{1: muse, 2: qwen, 3: qwen},
		},
		{
			name:    "no switch keeps the binding's candidate",
			events:  nil,
			current: qwen,
			want:    map[int]string{1: qwen},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := store.Binding{BuilderCandidate: tt.current}
			for n, want := range tt.want {
				tok, _, ok := candidateForRound(tt.events, n, b)
				if !ok || tok != want {
					t.Errorf("candidateForRound(round %d) = %q, %v, want %q", n, tok, ok, want)
				}
			}
		})
	}
}

// TestSwitchesIgnorePollEntriesWhenTheServerShippedTheHistory pins that the
// entry a poll writes for a switch it happened to observe does not count beside
// the server's own record of the same switch.
func TestSwitchesIgnorePollEntriesWhenTheServerShippedTheHistory(t *testing.T) {
	events := []store.LogEntry{
		{Round: 3, Kind: store.KindSwitch, Note: store.PollSwitchNotePrefix + "zen: a -> b"},
		{Round: 3, Kind: store.KindSwitch, Note: "switched builder (rate-limited): picked b for builder: order #2"},
		{Round: 4, Kind: store.KindSwitch, Note: store.PollSwitchNotePrefix + "zen: b -> c"},
	}

	if got := switchesForRound(events, 3); got != 1 {
		t.Errorf("switchesForRound(round 3) = %d, want 1", got)
	}
	if got := switchesForRound(events, 4); got != 1 {
		t.Errorf("switchesForRound(round 4) = %d, want the poll entry alone to count", got)
	}
}

func TestDeriveOutcomeStopWithoutReportIsDoneNoReport(t *testing.T) {
	events := []store.LogEntry{{Round: 1, Kind: store.KindPrompt}, {Round: 1, Kind: store.KindStop}}
	b := store.Binding{Round: 1, State: store.StateDone}
	if got := deriveOutcome(events, 1, b, map[string]bool{}); got != db.OutcomeDoneNoReport {
		t.Errorf("deriveOutcome = %q, want %q", got, db.OutcomeDoneNoReport)
	}
}

func TestDeriveOutcomeReportBeatsStop(t *testing.T) {
	events := []store.LogEntry{{Round: 1, Kind: store.KindStop}, {Round: 1, Kind: store.KindReport}}
	b := store.Binding{Round: 1, State: store.StateDone}
	if got := deriveOutcome(events, 1, b, map[string]bool{}); got != db.OutcomeReported {
		t.Errorf("deriveOutcome = %q, want %q", got, db.OutcomeReported)
	}
}
