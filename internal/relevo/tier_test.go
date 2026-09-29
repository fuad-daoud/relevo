package relevo

import (
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestResolveTier(t *testing.T) {
	t.Parallel()

	pol := policy.Policy{
		Tier: map[string]string{
			"builder": "read",
		},
	}
	candWithTier := candidate.Candidate{Tier: "edit"}
	candEmpty := candidate.Candidate{}

	// explicit wins
	if got := resolveTier("yolo", candWithTier, pol, "builder"); got != harness.TierYolo {
		t.Errorf("explicit yolo: got %v, want yolo", got)
	}

	// candidate over policy
	if got := resolveTier("", candWithTier, pol, "builder"); got != harness.TierEdit {
		t.Errorf("candidate edit over policy read: got %v, want edit", got)
	}

	// policy over harness
	if got := resolveTier("", candEmpty, pol, "builder"); got != harness.TierRead {
		t.Errorf("policy read over harness: got %v, want read", got)
	}

	// all empty -> harness
	if got := resolveTier("", candEmpty, policy.Policy{}, "builder"); got != harness.TierHarness {
		t.Errorf("all empty: got %v, want harness", got)
	}
}

func TestCheckTierCap(t *testing.T) {
	t.Parallel()

	defaultPol := policy.Policy{} // MaxTierOrDefault() == edit
	yoloPol := policy.Policy{MaxTier: "yolo"}

	// yolo vs default -> ErrTierAboveMax
	if err := checkTierCap(harness.TierYolo, defaultPol, false); !errors.Is(err, ErrTierAboveMax) {
		t.Errorf("yolo vs default without allowYolo: err = %v, want ErrTierAboveMax", err)
	}

	// yolo vs default with allowYolo -> nil
	if err := checkTierCap(harness.TierYolo, defaultPol, true); err != nil {
		t.Errorf("yolo vs default with allowYolo: unexpected err: %v", err)
	}

	// edit vs default -> nil
	if err := checkTierCap(harness.TierEdit, defaultPol, false); err != nil {
		t.Errorf("edit vs default: unexpected err: %v", err)
	}

	// harness -> nil always
	if err := checkTierCap(harness.TierHarness, defaultPol, false); err != nil {
		t.Errorf("harness vs default: unexpected err: %v", err)
	}

	// yolo vs max_tier: yolo -> nil
	if err := checkTierCap(harness.TierYolo, yoloPol, false); err != nil {
		t.Errorf("yolo vs max_tier yolo: unexpected err: %v", err)
	}
}

func TestEffectiveTier(t *testing.T) {
	t.Parallel()

	// RoundTier over Tier over harness
	b := store.Binding{
		Tier:      "read",
		RoundTier: "yolo",
	}
	if got := effectiveTier(b); got != harness.TierYolo {
		t.Errorf("RoundTier yolo over Tier read: got %v, want yolo", got)
	}

	b.RoundTier = ""
	if got := effectiveTier(b); got != harness.TierRead {
		t.Errorf("Tier read over harness: got %v, want read", got)
	}

	b.Tier = ""
	if got := effectiveTier(b); got != harness.TierHarness {
		t.Errorf("all empty: got %v, want harness", got)
	}
}
