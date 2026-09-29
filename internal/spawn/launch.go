package spawn

import (
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
)

// HeadlessLaunch renders the argv for one headless round: the candidate's
// binary, then its print form with the prompt, the budget, the round's working
// tree and the binding's state directory filled in. Pure. An unknown kind is an
// error, not a panic: the configured set was validated on load, but a binding
// written by a future relevo could name a kind this one does not know.
func HeadlessLaunch(c candidate.Candidate, role harness.RoleSpec, tier harness.Tier, budget time.Duration, prompt, dir, state string) ([]string, error) {
	h, ok := harness.Lookup(c.Harness)
	if !ok {
		return nil, fmt.Errorf("unknown harness kind %q", c.Harness)
	}
	l, err := h.Launch(c.Provider, c.Model, c.ExtraArgs, role, tier)
	if err != nil {
		return nil, err
	}
	if l.PromptAt < 0 {
		return nil, fmt.Errorf("harness %q has no print form", c.Harness)
	}
	return append([]string{h.Binary}, l.PrintArgs(prompt, budget, dir, state)...), nil
}
