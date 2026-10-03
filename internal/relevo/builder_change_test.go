package relevo

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestResolveSendBuilderUnknownToken(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	_, err := ResolveSendBuilder(rt, testAgyRef, "agy/test/nope")
	if err == nil {
		t.Fatal("ResolveSendBuilder succeeded for an unknown token, want an error")
	}
	if !errors.Is(err, ErrBadBuilder) {
		t.Errorf("err = %v, want it to wrap ErrBadBuilder", err)
	}
	if !errors.Is(err, candidate.ErrUnknownCandidate) {
		t.Errorf("err = %v, want it to wrap candidate.ErrUnknownCandidate", err)
	}
	if !strings.Contains(err.Error(), "agy/test/nope") {
		t.Errorf("err = %q, want it to name the token", err.Error())
	}
}

// TestResolveSendBuilderRolesMissingRefused pins #238's explicit-pick half
// through ResolveSendBuilder: roles_missing is the one gate that refuses an
// explicit --candidate pick, and the refusal is wrapped as ErrBadBuilder.
func TestResolveSendBuilderRolesMissingRefused(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Roles = fakeRoleChecker{"opencode": {".config/opencode/agents/researcher.md"}}

	_, err := ResolveSendBuilder(rt, testAgyRef, testOpencodeRef)
	if err == nil {
		t.Fatal("ResolveSendBuilder succeeded for a roles_missing candidate, want a refusal")
	}
	if !errors.Is(err, ErrBadBuilder) {
		t.Errorf("err = %v, want it to wrap ErrBadBuilder", err)
	}
	if !strings.Contains(err.Error(), "agent definitions missing") {
		t.Errorf("err = %q, want it to say agent definitions missing", err.Error())
	}
}

// TestResolveSendBuilderGatedResolves pins the explicit-pick rule: a
// rate-limited candidate still resolves, and the live gate is recorded on the
// Resolution so the pick line can name the bypass.
func TestResolveSendBuilderGatedResolves(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	if _, err := availability.Unavailable(AvailabilityDeps(rt), testAgyRef, time.Time{}, "quota"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	res, err := ResolveSendBuilder(rt, testClaudeRef, testAgyRef)
	if err != nil {
		t.Fatalf("ResolveSendBuilder: %v", err)
	}
	if res == nil {
		t.Fatal("res = nil, want a resolution for a different candidate")
	}
	if res.Token() != testAgyRef {
		t.Errorf("Token() = %q, want %q", res.Token(), testAgyRef)
	}
	if res.How != HowExplicit {
		t.Errorf("How = %q, want %q", res.How, HowExplicit)
	}
	if len(res.Gates) == 0 {
		t.Error("Gates is empty, want the live rate-limit gate recorded")
	}
}

// TestResolveSendBuilderCurrentIsNoop: naming the binding's own candidate is a
// no-op with no pick entry.
//
// A candidate token has exactly one spelling: Lookup is an exact key lookup on
// Ref.String() and ParseRef is a pure re-join of the token's parts, so no
// differently-spelled token can resolve to the same candidate. The
// canonical-equal case is therefore the only one constructible.
func TestResolveSendBuilderCurrentIsNoop(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	res, err := ResolveSendBuilder(rt, testAgyRef, testAgyRef)
	if err != nil {
		t.Fatalf("ResolveSendBuilder: %v", err)
	}
	if res != nil {
		t.Errorf("res = %+v, want nil for the binding's current candidate", res)
	}
}

func TestApplyBuilderSetsCandidateKindTierAndClearsExcluded(t *testing.T) {
	t.Parallel()

	b := store.Binding{
		BuilderCandidate: testAgyRef,
		Tier:             "harness",
		RoundExcluded:    []string{testAgyRef},
		Builder: store.Endpoint{
			Kind: "agy", Mode: store.ModeHeadless, AgentName: "webshop", Server: "contabo",
		},
	}
	res := Resolution{
		How:       HowExplicit,
		Candidate: candidate.Candidate{Harness: "claude", Provider: "test", Model: "m", Tier: "edit"},
	}

	got, err := applyBuilder(b, res, legacyRegistry(nil, policy.Policy{}), policy.Policy{}, false)
	if err != nil {
		t.Fatalf("applyBuilder: %v", err)
	}
	if got.BuilderCandidate != testClaudeRef {
		t.Errorf("BuilderCandidate = %q, want %q", got.BuilderCandidate, testClaudeRef)
	}
	if got.Builder.Kind != "claude" {
		t.Errorf("Builder.Kind = %q, want claude", got.Builder.Kind)
	}
	if got.Tier != "edit" {
		t.Errorf("Tier = %q, want the re-derived edit", got.Tier)
	}
	if got.RoundExcluded != nil {
		t.Errorf("RoundExcluded = %v, want it cleared", got.RoundExcluded)
	}
	if got.Builder.Mode != store.ModeHeadless || got.Builder.AgentName != "webshop" || got.Builder.Server != "contabo" {
		t.Errorf("Builder identity changed: %+v", got.Builder)
	}
}

func TestApplyBuilderTierAboveCapRefused(t *testing.T) {
	t.Parallel()

	b := store.Binding{BuilderCandidate: testAgyRef, Tier: "harness"}
	res := Resolution{
		How:       HowExplicit,
		Candidate: candidate.Candidate{Harness: "claude", Provider: "test", Model: "m", Tier: "yolo"},
	}

	got, err := applyBuilder(b, res, legacyRegistry(nil, policy.Policy{}), policy.Policy{}, false)
	if err == nil {
		t.Fatal("applyBuilder accepted a tier above max_tier with no allowYolo")
	}
	if !errors.Is(err, ErrBadBuilder) {
		t.Errorf("err = %v, want it to wrap ErrBadBuilder", err)
	}
	if !errors.Is(err, ErrTierAboveMax) {
		t.Errorf("err = %v, want it to wrap ErrTierAboveMax", err)
	}
	if !reflect.DeepEqual(got, b) {
		t.Errorf("binding changed: got %+v, want the original %+v", got, b)
	}
}

func TestRoundOpenIn(t *testing.T) {
	t.Parallel()

	plan := store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt}
	report := store.LogEntry{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport}

	tests := []struct {
		name    string
		entries []store.LogEntry
		want    bool
	}{
		{"no entries", nil, false},
		{"plan only", []store.LogEntry{plan}, true},
		{"legacy plan only", []store.LogEntry{{Round: 1, Direction: store.DirToBuilder, Kind: store.Kind("plan")}}, true},
		{"report only", []store.LogEntry{report}, false},
		{"plan and report", []store.LogEntry{plan, report}, false},
		{"another round's plan", []store.LogEntry{{Round: 2, Direction: store.DirToBuilder, Kind: store.KindPrompt}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := roundOpenIn(tt.entries, 1); got != tt.want {
				t.Errorf("roundOpenIn(%v, 1) = %v, want %v", tt.entries, got, tt.want)
			}
		})
	}
}

// TestCandidateSendRefusedExceptOnAHaltedServedBinding pins the one exception
// to the --candidate refusal: an open round is refused on every binding
// except a served one halted in NEEDS YOU, where the candidate continues the
// round without stopping first (`stop` on the halted round works too).
func TestCandidateSendRefusedExceptOnAHaltedServedBinding(t *testing.T) {
	t.Parallel()

	plan := store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt}
	report := store.LogEntry{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport}

	localActive := store.Binding{Round: 1, State: store.StateActive}
	servedActive := store.Binding{Round: 1, State: store.StateActive, Owner: "owner-1"}
	clientActive := store.Binding{Round: 1, State: store.StateActive, Builder: store.Endpoint{Mode: store.ModeRemote}}
	queued := store.Binding{Round: 1, State: store.StateActive, QueuedAt: time.Now()}
	localHalted := store.Binding{Round: 1, State: store.StateNeedsYou, Halt: "builder exited"}
	servedHalted := store.Binding{Round: 1, State: store.StateNeedsYou, Halt: "builder exited", Owner: "owner-1"}
	clientHalted := store.Binding{Round: 1, State: store.StateNeedsYou, Halt: "builder exited", Builder: store.Endpoint{Mode: store.ModeRemote}}

	tests := []struct {
		name    string
		b       store.Binding
		entries []store.LogEntry
		want    bool
	}{
		{"no prompt", localActive, nil, false},
		{"closed by a report", localActive, []store.LogEntry{plan, report}, false},
		{"another round's prompt", localActive, []store.LogEntry{{Round: 2, Direction: store.DirToBuilder, Kind: store.KindPrompt}}, false},
		{"open, active local", localActive, []store.LogEntry{plan}, true},
		{"open, active served (owner)", servedActive, []store.LogEntry{plan}, true},
		{"open, active served (client copy)", clientActive, []store.LogEntry{plan}, true},
		{"open, queued", queued, []store.LogEntry{plan}, true},
		{"open, needs_you local", localHalted, []store.LogEntry{plan}, true},
		{"open, needs_you served (owner)", servedHalted, []store.LogEntry{plan}, false},
		{"open, needs_you served (client copy)", clientHalted, []store.LogEntry{plan}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := candidateSendRefused(tt.b, tt.entries); got != tt.want {
				t.Errorf("candidateSendRefused(%+v, %v) = %v, want %v", tt.b, tt.entries, got, tt.want)
			}
		})
	}
}
