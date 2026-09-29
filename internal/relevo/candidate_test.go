package relevo

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
)

const (
	testOpencodeRef = "opencode/test/m"
	testClaudeRef   = "claude/test/m"
	testAgyRef      = "agy/test/m"
)

// testCandidatesJSON mirrors the shape of the three aliases DefaultTable
// used to ship, plus a reviewer on claude, so migrated tests keep their
// meaning: opencode and agy serve builder only; claude serves both.
const testCandidatesJSON = `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]}
]`

// testTwoProviderJSON has builders on two providers, so a rate limit on
// one leaves the other ungated.
const testTwoProviderJSON = `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"agy","provider":"other","model":"m","roles":["builder"]}
]`

// testTwoReviewerJSON: claude and opencode serve reviewer; agy does not.
const testTwoReviewerJSON = `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"agy","provider":"test","model":"m","roles":["builder"]}
]`

// candidateSet loads a candidate set from a JSON body, for tests that need
// a specific configuration without a file in the repo.
func candidateSet(t *testing.T, body string) *candidate.Set {
	t.Helper()
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write candidate set fixture: %v", err)
	}
	set, err := candidate.Load(path)
	if err != nil {
		t.Fatalf("load candidate set fixture: %v", err)
	}
	return set
}

// orderOf builds a Policy ordering role by toks, for tests that need one
// role's order without a policy.json file on disk.
func orderOf(role string, toks ...string) policy.Policy {
	return policy.Policy{Order: map[string][]string{role: toks}}
}

// spawnGate is a SpawnFailed gate on token, for tests.
func spawnGate(token string) availability.Gate {
	return availability.Gate{Token: token, Kind: availability.SpawnFailed, Until: baseTime.Add(10 * time.Minute)}
}

// limit is a RateLimited gate on token with no Until, for tests.
func limit(token string) availability.Gate {
	return availability.Gate{Token: token, Kind: availability.RateLimited}
}

func TestResolveCandidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		setBody         string
		pol             policy.Policy
		gates           []availability.Gate
		token           string
		role            string
		wantRef         string
		wantHow         How
		wantPosition    int
		wantSkipped     []string
		wantErr         error
		messageContains []string
	}{
		{
			name:    "named, serves",
			setBody: testCandidatesJSON,
			token:   "claude/test/m",
			role:    "builder",
			wantRef: "claude/test/m",
			wantHow: HowExplicit,
		},
		{
			name:    "named, serves consult",
			setBody: testCandidatesJSON,
			token:   "claude/test/m",
			role:    "reviewer",
			wantRef: "claude/test/m",
			wantHow: HowExplicit,
		},
		{
			name:            "named, does not serve",
			setBody:         testCandidatesJSON,
			token:           "agy/test/m",
			role:            "reviewer",
			wantErr:         ErrRoleNotServed,
			messageContains: []string{`does not serve actor "reviewer"`, "[builder]"},
		},
		{
			name:            "named, unknown",
			setBody:         testCandidatesJSON,
			token:           "claude/test/opus",
			role:            "builder",
			wantErr:         candidate.ErrUnknownCandidate,
			messageContains: []string{"configured:"},
		},
		{
			name:            "named, malformed",
			setBody:         testCandidatesJSON,
			token:           "claude/test",
			role:            "builder",
			wantErr:         candidate.ErrBadRef,
			messageContains: []string{"harness/provider/model"},
		},
		{
			name:            "empty set, no token",
			setBody:         "[]",
			token:           "",
			role:            "builder",
			wantErr:         ErrNoCandidates,
			messageContains: []string{"relevo config set candidates"},
		},
		{
			name:            "none serve, no token",
			setBody:         testCandidatesJSON,
			token:           "",
			role:            "researcher",
			wantErr:         ErrRoleNotServed,
			messageContains: []string{`no configured candidate serves actor "researcher"`},
		},
		{
			name:    "exactly one, no token",
			setBody: testCandidatesJSON,
			token:   "",
			role:    "reviewer",
			wantRef: "claude/test/m",
			wantHow: HowSole,
		},
		{
			name:            "ambiguous, no token",
			setBody:         testCandidatesJSON,
			token:           "",
			role:            "builder",
			wantErr:         ErrAmbiguousCandidate,
			messageContains: []string{`3 candidates serve "builder"`, "name one with", "config policy"},
		},
		{
			name:         "order, first ungated",
			setBody:      testCandidatesJSON,
			pol:          orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef),
			token:        "",
			role:         "builder",
			wantRef:      testAgyRef,
			wantHow:      HowOrder,
			wantPosition: 1,
		},
		{
			name:         "order, first gated",
			setBody:      testCandidatesJSON,
			pol:          orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef),
			gates:        []availability.Gate{spawnGate(testAgyRef)},
			token:        "",
			role:         "builder",
			wantRef:      testClaudeRef,
			wantHow:      HowOrder,
			wantPosition: 2,
			wantSkipped:  []string{testAgyRef},
		},
		{
			name:        "order, two gated, unlisted wins",
			setBody:     testCandidatesJSON,
			pol:         orderOf("builder", testAgyRef, testClaudeRef),
			gates:       []availability.Gate{spawnGate(testAgyRef), spawnGate(testClaudeRef)},
			token:       "",
			role:        "builder",
			wantRef:     testOpencodeRef,
			wantHow:     HowUnlisted,
			wantSkipped: []string{testAgyRef, testClaudeRef},
		},
		{
			name:    "order, all gated",
			setBody: testCandidatesJSON,
			pol:     orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef),
			gates:   []availability.Gate{limit(testAgyRef), limit(testClaudeRef), limit(testOpencodeRef)},
			token:   "",
			role:    "builder",
			wantErr: ErrAllGated,
			messageContains: []string{
				`every candidate serving "builder" is gated`,
				"agy/test/m (rate-limited until cleared)",
				"--candidate",
				"relevo gate --clear",
			},
		},
		{
			name:         "order, unknown entry skipped",
			setBody:      testCandidatesJSON,
			pol:          orderOf("builder", "claude/test/nope", testOpencodeRef),
			token:        "",
			role:         "builder",
			wantRef:      testOpencodeRef,
			wantHow:      HowOrder,
			wantPosition: 2,
		},
		{
			name:         "order, non-serving entry skipped",
			setBody:      testTwoReviewerJSON,
			pol:          orderOf("reviewer", testAgyRef, testOpencodeRef),
			token:        "",
			role:         "reviewer",
			wantRef:      testOpencodeRef,
			wantHow:      HowOrder,
			wantPosition: 2,
		},
		{
			name:    "sole beats order",
			setBody: testCandidatesJSON,
			pol:     orderOf("reviewer", testClaudeRef),
			token:   "",
			role:    "reviewer",
			wantRef: testClaudeRef,
			wantHow: HowSole,
		},
		{
			name:         "order, two gates on one token",
			setBody:      testCandidatesJSON,
			pol:          orderOf("builder", testAgyRef, testClaudeRef),
			gates:        []availability.Gate{spawnGate(testAgyRef), limit(testAgyRef)},
			token:        "",
			role:         "builder",
			wantRef:      testClaudeRef,
			wantHow:      HowOrder,
			wantPosition: 2,
			wantSkipped:  []string{testAgyRef, testAgyRef},
		},
		{
			name:    "sole, gated",
			setBody: testCandidatesJSON,
			gates:   []availability.Gate{limit(testClaudeRef)},
			token:   "",
			role:    "reviewer",
			wantErr: ErrAllGated,
		},
		{
			name:    "explicit, gated, proceeds",
			setBody: testCandidatesJSON,
			gates:   []availability.Gate{limit(testAgyRef)},
			token:   testAgyRef,
			role:    "builder",
			wantRef: testAgyRef,
			wantHow: HowExplicit,
		},
		{
			name:    "no order, two serve, refuses",
			setBody: testTwoProviderJSON,
			token:   "",
			role:    "builder",
			wantErr: ErrAmbiguousCandidate,
		},
		{
			name:         "order with gate on other provider",
			setBody:      testTwoProviderJSON,
			pol:          orderOf("builder", "agy/other/m", testClaudeRef),
			gates:        []availability.Gate{limit("agy/other/m")},
			token:        "",
			role:         "builder",
			wantRef:      testClaudeRef,
			wantHow:      HowOrder,
			wantPosition: 2,
			wantSkipped:  []string{"agy/other/m"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := candidateSet(t, tt.setBody)
			got, err := resolveCandidate(set, tt.pol, tt.gates, tt.token, tt.role)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("resolveCandidate() err = %v, want %v", err, tt.wantErr)
				}
				for _, sub := range tt.messageContains {
					if !strings.Contains(err.Error(), sub) {
						t.Errorf("resolveCandidate() error %q does not contain %q", err.Error(), sub)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveCandidate() unexpected error: %v", err)
			}
			if got.Candidate.Ref().String() != tt.wantRef {
				t.Errorf("resolveCandidate() ref = %q, want %q", got.Candidate.Ref().String(), tt.wantRef)
			}
			if got.How != tt.wantHow {
				t.Errorf("resolveCandidate() how = %q, want %q", got.How, tt.wantHow)
			}
			if got.Position != tt.wantPosition {
				t.Errorf("resolveCandidate() position = %d, want %d", got.Position, tt.wantPosition)
			}
			gotSkipped := make([]string, len(got.Skipped))
			for i, s := range got.Skipped {
				gotSkipped[i] = s.Token
			}
			if len(gotSkipped) != len(tt.wantSkipped) {
				t.Errorf("resolveCandidate() skipped = %v, want %v", gotSkipped, tt.wantSkipped)
			} else {
				for i := range gotSkipped {
					if gotSkipped[i] != tt.wantSkipped[i] {
						t.Errorf("resolveCandidate() skipped = %v, want %v", gotSkipped, tt.wantSkipped)
						break
					}
				}
			}
			if tt.name == "explicit, gated, proceeds" && len(got.Gates) != 1 {
				t.Errorf("resolveCandidate() gates = %v, want len 1", got.Gates)
			}
		})
	}
}

func TestResolveCandidateIsDeterministic(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)

	c1, err1 := resolveCandidate(set, policy.Policy{}, nil, "", "reviewer")
	c2, err2 := resolveCandidate(set, policy.Policy{}, nil, "", "reviewer")
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v, %v", err1, err2)
	}
	if c1.Candidate.Ref().String() != c2.Candidate.Ref().String() {
		t.Errorf("got %q and %q, want identical ref", c1.Candidate.Ref().String(), c2.Candidate.Ref().String())
	}

	_, errA1 := resolveCandidate(set, policy.Policy{}, nil, "", "builder")
	_, errA2 := resolveCandidate(set, policy.Policy{}, nil, "", "builder")
	if errA1 == nil || errA2 == nil {
		t.Fatalf("expected errors for ambiguous case")
	}
	if errA1.Error() != errA2.Error() {
		t.Errorf("got %q and %q, want identical error string", errA1.Error(), errA2.Error())
	}
}

func TestExplainResolution(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	lookup := func(tok string) candidate.Candidate {
		ref, err := candidate.ParseRef(tok)
		if err != nil {
			t.Fatalf("ParseRef(%q): %v", tok, err)
		}
		c, err := set.Lookup(ref)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", tok, err)
		}
		return c
	}
	claude := lookup(testClaudeRef)
	agy := lookup(testAgyRef)
	opencode := lookup(testOpencodeRef)

	until := baseTime.Add(10 * time.Minute)
	untilText := availability.GateUntilText(until)

	tests := []struct {
		name string
		role string
		res  Resolution
		want string
	}{
		{
			name: "sole",
			role: "reviewer",
			res:  Resolution{How: HowSole, Candidate: claude},
			want: "picked claude/test/m for reviewer: sole candidate",
		},
		{
			name: "order",
			role: "builder",
			res: Resolution{
				How:       HowOrder,
				Position:  2,
				Candidate: claude,
				Skipped:   []Skip{{Token: testAgyRef, Kind: availability.SpawnFailed, Until: until}},
			},
			want: "picked claude/test/m for builder: order #2; skipped " + testAgyRef + " (spawn failed " + untilText + ")",
		},
		{
			name: "unlisted",
			role: "builder",
			res: Resolution{
				How:       HowUnlisted,
				Candidate: opencode,
				Skipped: []Skip{
					{Token: testAgyRef, Kind: availability.SpawnFailed, Until: until},
					{Token: testClaudeRef, Kind: availability.RateLimited},
				},
			},
			want: "picked opencode/test/m for builder: unlisted, after order; skipped " + testAgyRef + " (spawn failed " + untilText + "), " + testClaudeRef + " (rate-limited until cleared)",
		},
		{
			// #93: three `relevo gate <token>` calls on one provider are three
			// ledger entries and three Skips, but one sentence.
			name: "duplicate gates on one token render once",
			role: "builder",
			res: Resolution{
				How:       HowOrder,
				Position:  2,
				Candidate: claude,
				Skipped: []Skip{
					{Token: testAgyRef, Kind: availability.RateLimited},
					{Token: testAgyRef, Kind: availability.RateLimited},
					{Token: testAgyRef, Kind: availability.RateLimited},
				},
			},
			want: "picked claude/test/m for builder: order #2; skipped " + testAgyRef + " (rate-limited until cleared)",
		},
		{
			name: "explicit with duplicate gates renders once",
			role: "builder",
			res: Resolution{
				How:       HowExplicit,
				Candidate: agy,
				Gates: []Skip{
					{Token: testAgyRef, Kind: availability.RateLimited},
					{Token: testAgyRef, Kind: availability.RateLimited},
				},
			},
			want: "picked agy/test/m for builder: explicit, policy bypassed; gated: rate-limited until cleared",
		},
		{
			name: "explicit",
			role: "builder",
			res:  Resolution{How: HowExplicit, Candidate: agy},
			want: "picked agy/test/m for builder: explicit, policy bypassed",
		},
		{
			name: "explicit inherited gated",
			role: "builder",
			res: Resolution{
				How:           HowExplicit,
				Candidate:     agy,
				InheritedFrom: "source",
				Gates:         []Skip{{Token: testAgyRef, Kind: availability.RateLimited}},
			},
			want: "picked agy/test/m for builder: explicit, inherited from source, policy bypassed; gated: rate-limited until cleared",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExplainResolution(tt.role, tt.res); got != tt.want {
				t.Errorf("ExplainResolution() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestPickTextUsesNamesStoredNoteUnchanged pins A1 §4.4: the note
// ExplainResolution renders -- the KindPick entry `relevo log` reads, and
// what the outcome and stats parsers match -- keeps every candidate token,
// byte-identical to its pre-A1 text, while PickText, the line a human reads,
// names each candidate.
func TestPickTextUsesNamesStoredNoteUnchanged(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	ref, err := candidate.ParseRef(testClaudeRef)
	if err != nil {
		t.Fatalf("ParseRef(%q): %v", testClaudeRef, err)
	}
	claude, err := set.Lookup(ref)
	if err != nil {
		t.Fatalf("Lookup(%q): %v", testClaudeRef, err)
	}

	until := baseTime.Add(10 * time.Minute)
	untilText := availability.GateUntilText(until)

	res := Resolution{
		How:       HowOrder,
		Position:  2,
		Candidate: claude,
		Skipped: []Skip{
			{Token: testAgyRef, Kind: availability.SpawnFailed, Until: until},
			{Token: testOpencodeRef, Kind: availability.RateLimited},
		},
	}

	// The pre-A1 golden, every candidate named by its token.
	stored := "picked claude/test/m for builder: order #2; skipped " +
		testAgyRef + " (spawn failed " + untilText + "), " +
		testOpencodeRef + " (rate-limited until cleared)"
	if got := ExplainResolution("builder", res); got != stored {
		t.Errorf("ExplainResolution() = %q, want the pre-A1 golden %q", got, stored)
	}
	if note := pickEntry(baseTime, 1, "builder", res).Note; note != stored {
		t.Errorf("stored pick note = %q, want the pre-A1 golden %q", note, stored)
	}

	// PickText names each candidate: DeriveNames gives testCandidatesJSON's
	// entries agy-m, claude-m and m.
	want := "picked claude-m for builder: order #2; skipped " +
		"agy-m (spawn failed " + untilText + "), m (rate-limited until cleared)"
	if got := PickText("builder", res, set); got != want {
		t.Errorf("PickText() = %q, want %q", got, want)
	}

	// Without a set the line reads exactly as the stored note does.
	if got := PickText("builder", res, nil); got != stored {
		t.Errorf("PickText(nil set) = %q, want %q", got, stored)
	}
}

func TestCandidateKind(t *testing.T) {
	t.Parallel()

	rt := Runtime{
		Candidates: candidateSet(t, testCandidatesJSON),
		Gates:      testGateKV(t),
		Now:        func() time.Time { return baseTime },
	}
	if got := CandidateKind(rt, testClaudeRef); got != "claude" {
		t.Errorf("CandidateKind(%q) = %q, want claude", testClaudeRef, got)
	}
	if got := CandidateKind(rt, "claude/test/nope"); got != "" {
		t.Errorf("CandidateKind(unknown) = %q, want empty", got)
	}
}

// fakeRoleChecker is a harness.RoleChecker test double: Missing(kind) looks
// up kind in the map, returning nil (present) for any kind not listed. It
// answers the same way whatever definitions are asked for.
type fakeRoleChecker map[string][]string

func (f fakeRoleChecker) Missing(kind string, _ []string) []string { return f[kind] }

// TestRolesMissingSkipsInOrder pins #238's ranked-walk half: a candidate
// whose harness kind is missing role files is gated like any other and
// skipped in order, without an error -- resolveCandidate picks the next
// one and ExplainResolution names why the first was skipped.
//
// Mutation check: with rt.Roles == nil (the control case below), no gate is
// synthesised and the first candidate in order is picked; a change that
// makes rolesMissingGates run even when rt.Roles is nil would make this
// control assertion fail.
func TestRolesMissingSkipsInOrder(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Policy = orderOf("builder", testOpencodeRef, testClaudeRef)
	rt.Roles = fakeRoleChecker{"opencode": {".config/opencode/agents/researcher.md"}}

	res, err := resolveCandidate(rt.Candidates, rt.Policy, availability.Gates(AvailabilityDeps(rt)), "", "builder")
	if err != nil {
		t.Fatalf("resolveCandidate: %v", err)
	}
	if res.Token() != testClaudeRef {
		t.Errorf("picked %q, want %q", res.Token(), testClaudeRef)
	}
	if explain := ExplainResolution("builder", res); !strings.Contains(explain, "skipped "+testOpencodeRef+" (agents missing") {
		t.Errorf("ExplainResolution = %q, want it to contain %q", explain, "skipped "+testOpencodeRef+" (agents missing")
	}

	// Control: rt.Roles == nil means no check is configured, so nothing is
	// gated and the first candidate in order is picked -- today's
	// behaviour.
	rt.Roles = nil
	res2, err := resolveCandidate(rt.Candidates, rt.Policy, availability.Gates(AvailabilityDeps(rt)), "", "builder")
	if err != nil {
		t.Fatalf("resolveCandidate (rt.Roles == nil): %v", err)
	}
	if res2.Token() != testOpencodeRef {
		t.Errorf("picked %q with rt.Roles == nil, want %q", res2.Token(), testOpencodeRef)
	}
}

// TestRolesMissingRefusesExplicit pins #238's explicit-pick half: unlike
// every other gate (recorded, but the pick proceeds), roles_missing refuses
// an explicit --candidate pick outright, because it cannot succeed.
func TestRolesMissingRefusesExplicit(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Roles = fakeRoleChecker{"opencode": {".config/opencode/agents/researcher.md"}}

	_, err := resolveCandidate(rt.Candidates, rt.Policy, availability.Gates(AvailabilityDeps(rt)), testOpencodeRef, "builder")
	if err == nil {
		t.Fatal("resolveCandidate succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "agent definitions missing") || !strings.Contains(err.Error(), "relevo config agents --kind") {
		t.Errorf("err = %q, want it to contain %q and %q", err.Error(), "agent definitions missing", "relevo config agents --kind")
	}
}

// TestResolveRoleByName pins A1 §4.2: an explicit candidate may be named by
// its short name, and the refusals name the candidate by that name.
func TestResolveRoleByName(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)

	res, err := resolveCandidate(set, policy.Policy{}, nil, "claude-m", "builder")
	if err != nil {
		t.Fatalf("resolveCandidate(claude-m): %v", err)
	}
	if got := res.Token(); got != testClaudeRef {
		t.Errorf("Token() = %q, want %q", got, testClaudeRef)
	}
	if res.How != HowExplicit {
		t.Errorf("How = %q, want %q", res.How, HowExplicit)
	}

	_, err = resolveCandidate(set, policy.Policy{}, nil, "agy-m", "reviewer")
	if !errors.Is(err, ErrRoleNotServed) {
		t.Fatalf("resolveCandidate(agy-m, reviewer) err = %v, want ErrRoleNotServed", err)
	}
	if !strings.Contains(err.Error(), `candidate "agy-m" does not serve actor "reviewer"`) {
		t.Errorf("err = %q, want it to name the candidate by its short name", err.Error())
	}

	_, err = resolveCandidate(set, policy.Policy{}, nil, "nope", "builder")
	if !errors.Is(err, candidate.ErrUnknownCandidate) {
		t.Errorf("resolveCandidate(nope) err = %v, want ErrUnknownCandidate", err)
	}
}
