package relevo

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrBadBuilder wraps every refusal of a `relevo send --candidate` token itself:
// an unknown candidate, a bad ref, a role it does not serve, roles_missing,
// or a tier above the cap. It exists so the server can map these to 422
// rather than 500 (#318).
var ErrBadBuilder = errors.New("send --candidate")

// ResolveSendBuilder is ResolveSendBuilderFor with the built-in builder's
// role, so the callers and tests that predate roles stay unchanged.
func ResolveSendBuilder(rt Runtime, current, token string) (*Resolution, error) {
	return ResolveSendBuilderFor(rt, "builder", current, token)
}

// ResolveSendBuilderFor turns --candidate's token into the resolution to apply
// for a binding whose writer role is role, or nil for a no-op. An explicit
// token is resolved exactly as `relevo bind --worktree --candidate` resolves one, so a gated
// candidate still resolves (its gates are recorded on the Resolution) and only
// roles_missing refuses (#238).
//
// It returns nil, nil when the token canonicalises to the binding's current
// candidate: naming the builder a binding already has changes nothing. Every
// error is wrapped with ErrBadBuilder, naming the token.
//
// Precondition: token != "". It is a pure read of the ledger and candidates.
func ResolveSendBuilderFor(rt Runtime, role, current, token string) (*Resolution, error) {
	res, err := resolveRole(rt.RoleRegistry(), rt.Candidates, availability.Gates(AvailabilityDeps(rt)), token, role, pickFor(rt))
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", ErrBadBuilder, token, err)
	}
	if res.Token() == current {
		return nil, nil
	}
	return &res, nil
}

// applyBuilder is the persist half of a `relevo send --candidate`: it moves the
// binding to res's candidate for this round and every later one, re-derives
// the binding's stored tier for the new candidate through the normal chain,
// and clears any round exclusion, which belongs to the old builder's round.
//
// It leaves Builder.Mode, AgentName and Server as they are. A candidate whose
// re-derived tier is above max_tier without allowYolo is refused with
// ErrBadBuilder, leaving the binding unchanged. Pure.
func applyBuilder(b store.Binding, res Resolution, reg *roles.Registry, pol policy.Policy, allowYolo bool) (store.Binding, error) {
	tier := resolveRoleTier("", res.Candidate, reg, bindingRole(b))
	if err := checkTierCap(tier, pol, allowYolo); err != nil {
		return b, fmt.Errorf("%w %s: %w", ErrBadBuilder, res.Token(), err)
	}
	b.BuilderCandidate = res.Token()
	b.BuilderAccount = res.Account
	b.Builder.Kind = res.Candidate.Harness
	b.Tier = string(tier)
	b.RoundExcluded = nil
	return b, nil
}

// roundOpenIn reports whether the binding's round is open. It is store's single
// definition of the condition, read here for the --candidate refusal.
func roundOpenIn(entries []store.LogEntry, round int) bool {
	return store.RoundOpen(entries, round)
}

// candidateSendRefused reports whether `relevo send --candidate` is refused
// because the binding's round is already open, with one exception: a served
// binding that has halted in NEEDS YOU may be re-pointed at a named candidate.
//
// The why: a halt is a human decision point. On a served binding `relevo
// stop` performs the stop on the halted round, and naming the candidate that
// continues *this* round works without stopping first, so the human has two
// levers there. A local binding is not exempt -- it stops and re-sends as its
// refusal has always said.
//
// "Served" has two spellings, because relevo keeps two copies of such a
// binding: the server's copy carries Owner and a headless builder, while the
// client's copy carries Builder.Mode == ModeRemote and no Owner. This is the
// same "not local" test RunningProcs uses.
func candidateSendRefused(b store.Binding, entries []store.LogEntry) bool {
	if !roundOpenIn(entries, b.Round) {
		return false
	}
	served := b.Owner != "" || b.Builder.Remote()
	return !(served && b.State == store.StateNeedsYou)
}

// HasPromptEntry reports whether the log holds a to-builder prompt entry of
// round, in either kind spelling, with HasEntry's shape and nudge exclusion.
// It reads store's shared definition rather than repeating it.
func HasPromptEntry(entries []store.LogEntry, round int) bool {
	return store.HasPrompt(entries, round)
}
