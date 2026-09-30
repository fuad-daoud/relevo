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
