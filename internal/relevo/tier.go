package relevo

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

var (
	ErrTierAboveMax = errors.New("tier exceeds max_tier")
)

// resolveRoleTier is the chain: explicit, the registry's tier for the role,
// harness. explicit and candidate values are already validated by their
// parsers; an unparseable stored value is treated as harness. In legacy mode
// reg.TierFor is candidate.Tier then policy.TierFor(role), so the chain is
// unchanged; in file mode it is the role's row tier (#374 §4.4).
func resolveRoleTier(explicit string, c candidate.Candidate, reg *roles.Registry, role string) harness.Tier {
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

// resolveTier is resolveRoleTier over the legacy registry derived from pol,
// which is what every pre-roles.json caller meant. It stays for tests
// (#374 §4.4).
func resolveTier(explicit string, c candidate.Candidate, pol policy.Policy, role string) harness.Tier {
	return resolveRoleTier(explicit, c, legacyRegistry(nil, pol), role)
}

// checkTierCap refuses tier when tier.Above(pol.MaxTierOrDefault()) and
// !allowYolo: `tier %s exceeds max_tier %s; pass --allow-yolo or raise
// max_tier in ~/.config/relevo/policy.json`. harness never refuses.
func checkTierCap(tier harness.Tier, pol policy.Policy, allowYolo bool) error {
	maxTier := pol.MaxTierOrDefault()
	if tier.Above(maxTier) && !allowYolo {
		return fmt.Errorf("%w: tier %s exceeds max_tier %s; pass --allow-yolo or raise max_tier in config policy", ErrTierAboveMax, tier, maxTier)
	}
	return nil
}

// readerTier floors a reader's permission tier at edit (A5 §4): a reader must
// be able to write its artifact directory, and shape does not change the tools
// an agent gets (cockpit spec §3.2). A resolved harness or read tier becomes
// edit. When the harness refuses edit, the lowest tier it does allow that can
// write is used instead (opencode allows only yolo). A policy max_tier below
// edit refuses the bind.
func readerTier(tier harness.Tier, kind string, pol policy.Policy) (harness.Tier, error) {
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

// effectiveTier is RoundTier if set, else Tier, else harness.
func effectiveTier(b store.Binding) harness.Tier {
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
