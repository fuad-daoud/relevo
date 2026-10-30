package resolve

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/policy"
)

func kindGate(token string, kind availability.Kind) availability.Gate {
	return availability.Gate{Token: token, Kind: kind, Until: baseTime.Add(10 * time.Minute)}
}

func TestRoleRefusalsCause(t *testing.T) {
	t.Parallel()

	order := orderOf("builder", testOpencodeRef, testClaudeRef, testAgyRef)
	tests := []struct {
		name      string
		pol       policy.Policy
		gates     []availability.Gate
		wantCause RefusalCause
	}{
		{
			name: "window on every serving token is a window",
			pol:  order,
			gates: []availability.Gate{
				kindGate(testOpencodeRef, availability.Scheduled),
				kindGate(testClaudeRef, availability.Scheduled),
				kindGate(testAgyRef, availability.Scheduled),
			},
			wantCause: CauseWindow,
		},
		{
			name: "a demote window beside a cap is still a cap",
			pol:  order,
			gates: []availability.Gate{
				kindGate(testOpencodeRef, availability.ConcurrencyLimit),
				kindGate(testClaudeRef, availability.ConcurrencyLimit),
				kindGate(testAgyRef, availability.ConcurrencyLimit),
				{Token: testAgyRef, Kind: availability.Scheduled, Demote: true, Until: baseTime.Add(10 * time.Minute)},
			},
			wantCause: CauseCap,
		},
		{
			name: "cap on every serving token is a cap",
			pol:  order,
			gates: []availability.Gate{
				kindGate(testOpencodeRef, availability.ConcurrencyLimit),
				kindGate(testClaudeRef, availability.ConcurrencyLimit),
				kindGate(testAgyRef, availability.ConcurrencyLimit),
			},
			wantCause: CauseCap,
		},
		{
			name: "window beside a cap is a window",
			pol:  order,
			gates: []availability.Gate{
				kindGate(testOpencodeRef, availability.ConcurrencyLimit),
				kindGate(testClaudeRef, availability.Scheduled),
				kindGate(testAgyRef, availability.ConcurrencyLimit),
			},
			wantCause: CauseWindow,
		},
		{
			name: "window beside a rate limit is a gate",
			pol:  order,
			gates: []availability.Gate{
				kindGate(testOpencodeRef, availability.Scheduled),
				limit(testClaudeRef),
				kindGate(testAgyRef, availability.Scheduled),
			},
			wantCause: CauseGate,
		},
		{
			name: "window beside a spawn failure is a gate",
			pol:  order,
			gates: []availability.Gate{
				kindGate(testOpencodeRef, availability.Scheduled),
				spawnGate(testClaudeRef),
				kindGate(testAgyRef, availability.Scheduled),
			},
			wantCause: CauseGate,
		},
		{
			name:      "no order and no gates leaves the cause empty",
			pol:       policy.Policy{},
			wantCause: CauseGate,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			set := candidateSet(t, testCandidatesJSON)
			var got *RoleRefusal
			for _, r := range RoleRefusals(set, tt.pol, tt.gates) {
				if r.Role == "builder" {
					got = &r
				}
			}
			if got == nil {
				t.Fatal("no builder refusal")
			}
			if got.Cause != tt.wantCause {
				t.Errorf("Cause = %q, want %q", got.Cause, tt.wantCause)
			}
		})
	}
}

func TestRoleRefusalsGatedNamesOnlyClearableProviders(t *testing.T) {
	t.Parallel()

	const otherAgy = "agy/other/m"
	set := candidateSet(t, testTwoProviderJSON)
	pol := orderOf("builder", testOpencodeRef, testClaudeRef, otherAgy)
	gates := []availability.Gate{
		kindGate(testOpencodeRef, availability.Scheduled),
		kindGate(testClaudeRef, availability.ConcurrencyLimit),
		limit(otherAgy),
	}
	var got *RoleRefusal
	for _, r := range RoleRefusals(set, pol, gates) {
		if r.Role == "builder" {
			got = &r
		}
	}
	if got == nil {
		t.Fatal("no builder refusal")
	}
	if len(got.Gated) != 1 || got.Gated[0] != "other" {
		t.Errorf("Gated = %v, want only the rate-limited provider [other]: a window or a cap is never cleared", got.Gated)
	}
}
