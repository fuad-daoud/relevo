package resolve

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

var (
	ErrTierAboveMax = errors.New("tier exceeds max_tier")
)

// ResolveTier is the chain: explicit, the registry's tier for the role,
// harness. explicit and candidate values are already validated by their
// parsers; an unparseable stored value is treated as harness. In legacy mode
// reg.TierFor is candidate.Tier then policy.TierFor(role), so the chain is
// unchanged; in file mode it is the role's row tier.
func ResolveTier(explicit string, c candidate.Candidate, reg *roles.Registry, role string) harness.Tier {
	if explicit != "" {
		if t, err := harness.ParseTier(explicit); err == nil {
			return t
		}
	}
	if t, ok := reg.TierFor(role, c); ok {
		return t
	}
	return harness.TierHarness
}

// resolveTier is ResolveTier over the legacy registry derived from pol,
// which is what every caller without a registry means.
func resolveTier(explicit string, c candidate.Candidate, pol policy.Policy, role string) harness.Tier {
	return ResolveTier(explicit, c, legacyRegistry(nil, pol), role)
}

// CheckTierCap refuses tier when tier.Above(pol.MaxTierOrDefault()) and
// !allowYolo: `tier %s exceeds max_tier %s; pass --allow-yolo or raise
// max_tier in ~/.config/relevo/policy.json`. harness never refuses.
func CheckTierCap(tier harness.Tier, pol policy.Policy, allowYolo bool) error {
	maxTier := pol.MaxTierOrDefault()
	if tier.Above(maxTier) && !allowYolo {
		return fmt.Errorf("%w: tier %s exceeds max_tier %s; pass --allow-yolo or raise max_tier in config policy", ErrTierAboveMax, tier, maxTier)
	}
	return nil
}

// LaunchTier derives the tier a launch will use and refuses it when the stored
// tier is above max_tier without the explicit allowance. It is the one place
// that states the rule: a launch re-checks the stored tier every time, so an
// over-cap record never starts without a caller that carries the allowance.
func LaunchTier(b store.Binding, pol policy.Policy, allowYolo bool) (harness.Tier, error) {
	tier := EffectiveTier(b)
	if err := CheckTierCap(tier, pol, allowYolo); err != nil {
		return tier, err
	}
	return tier, nil
}

// ReaderTier floors a reader's permission tier at edit: a reader must
// be able to write its artifact directory, and shape does not change the tools
// an agent gets. A resolved harness or read tier becomes
// edit. When the harness refuses edit, the lowest tier it does allow that can
// write is used instead (opencode allows only yolo). A policy max_tier below
// edit refuses the bind.
func ReaderTier(tier harness.Tier, kind string, pol policy.Policy) (harness.Tier, error) {
	if tier == harness.TierHarness || tier == harness.TierRead {
		tier = harness.TierEdit
	}
	if pol.MaxTierOrDefault().Rank() < harness.TierEdit.Rank() {
		return tier, fmt.Errorf("a reader actor needs tier edit; max_tier is %s", pol.MaxTierOrDefault())
	}
	if h, ok := harness.Lookup(kind); ok {
		if _, err := h.PermissionArgs(tier); errors.Is(err, harness.ErrTierUnsupported) {
			for _, t := range []harness.Tier{harness.TierEdit, harness.TierYolo} {
				if _, err := h.PermissionArgs(t); err == nil {
					tier = t
					break
				}
			}
		}
	}
	return tier, nil
}

// TierUnsupportedMessage reports whether a wire refusal's message carries a
// harness-tier refusal: the server answers one 422 invalid with the
// ErrTierUnsupported text a local bind produces, on the same code other
// create and send refusals share, so the message is what tells them apart.
func TierUnsupportedMessage(msg string) bool {
	return strings.Contains(msg, harness.ErrTierUnsupported.Error())
}

// TierUnsupportedFromWire rebuilds the harness refusal from a server's answer.
// The server's message already opens with the sentinel's text, so it is cut
// once here rather than printed twice.
func TierUnsupportedFromWire(msg string) error {
	return fmt.Errorf("%w: %s", harness.ErrTierUnsupported, strings.TrimPrefix(msg, harness.ErrTierUnsupported.Error()+": "))
}

// EffectiveTier is RoundTier if set, else Tier, else harness.
func EffectiveTier(b store.Binding) harness.Tier {
	if b.RoundTier != "" {
		if t, err := harness.ParseTier(b.RoundTier); err == nil {
			return t
		}
	}
	if b.Tier != "" {
		if t, err := harness.ParseTier(b.Tier); err == nil {
			return t
		}
	}
	return harness.TierHarness
}
