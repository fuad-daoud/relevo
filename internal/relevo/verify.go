package relevo

import (
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
)

// verifyTier is the reviewer's permission tier (#144): in file mode, the
// reviewer row's tier when roles.json set one, else yolo; otherwise
// policy.json tier.reviewer when set, else the candidate's own tier, else
// yolo.
//
// yolo is deliberate and it is why verify is not an ordinary `relevo ask`:
// the consult runs in a throwaway worktree at the builder's HEAD that relevo
// created for it and removes afterwards, so letting it execute tests there
// costs nothing the builder or the mastermind owns. A review that cannot run
// anything is not the review this feature promises. The role definition
// still tells it not to edit; relevo cannot observe writes.
//
// It is deliberately not capped by max_tier: this consult only, in this
// tree only.
func verifyTier(rt Runtime, c candidate.Candidate) harness.Tier {
	reg := rt.RoleRegistry()
	if reg.FileMode() {
		// roles.json is the only place a file-mode role's tier comes from,
		// so the candidate's own tier is ignored here (#374 §4.6).
		if t, ok := reg.TierFor("reviewer", c); ok {
			return t
		}
		return harness.TierYolo
	}
	if t, ok := rt.Policy.TierFor("reviewer"); ok {
		return t
	}
	if c.Tier != "" {
		if t, err := harness.ParseTier(c.Tier); err == nil {
			return t
		}
	}
	return harness.TierYolo
}
