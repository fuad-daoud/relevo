package relevo

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrBadBuilder wraps every refusal of a `relevo send --candidate` token: an
// unknown candidate, a bad ref, an unserved role, or a tier above the cap. It
// exists so the server maps these to 422 rather than 500.
var ErrBadBuilder = errors.New("send --candidate")

// ResolveSendBuilder is ResolveSendBuilderFor with the built-in builder's role.
func ResolveSendBuilder(rt Runtime, current, token string) (*resolve.Resolution, error) {
	return ResolveSendBuilderFor(rt, "builder", current, token)
}

// ResolveSendBuilderFor turns --candidate's token into the resolution to
// apply for a binding whose writer role is role, or nil for a no-op. An
// explicit token is resolved exactly as `relevo bind --worktree --candidate`
// resolves one, so a gated candidate still resolves, its gates recorded;
// only roles_missing refuses.
// A token naming the current candidate is a no-op. Every error is wrapped
// with ErrBadBuilder, naming the token.
//
// Precondition: token != "". It is a pure read of the ledger and candidates.
func ResolveSendBuilderFor(rt Runtime, role, current, token string) (*resolve.Resolution, error) {
	return resolveSendBuilder(rt, role, current, token, "")
}

// resolveSendBuilder is ResolveSendBuilderFor judging skills in dir; an empty
// dir judges none.
func resolveSendBuilder(rt Runtime, role, current, token, dir string) (*resolve.Resolution, error) {
	gates := resolve.WithSkillGates(ResolveDeps(rt), availability.Gates(AvailabilityDeps(rt)), dir, nil)
	res, err := resolve.ResolveRole(rt.RoleRegistry(), rt.Candidates, gates, token, role, resolve.PickFor(ResolveDeps(rt)))
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", ErrBadBuilder, token, err)
	}
	if res.Token() == current {
		return nil, nil
	}
	return &res, nil
}

// applyBuilder is the persist half of a `relevo send --candidate`: it moves
// the binding to res's candidate, re-derives the stored tier, and clears any
// round exclusion, which belongs to the old builder's round. Builder.Mode,
// AgentName and Server stay as they are. A tier above max_tier without
// allowYolo is refused with ErrBadBuilder, leaving the binding unchanged.
func applyBuilder(b store.Binding, res resolve.Resolution, reg *roles.Registry, pol policy.Policy, allowYolo bool) (store.Binding, error) {
	tier := resolve.ResolveTier("", res.Candidate, reg, b.WriterRole())
	if err := resolve.CheckTierCap(tier, pol, allowYolo); err != nil {
		return b, fmt.Errorf("%w %s: %w", ErrBadBuilder, res.Token(), err)
	}
	b.BuilderCandidate = res.Token()
	b.BuilderAccount = res.Account
	b.BuilderPinned = res.How == resolve.HowExplicit
	b.Builder.Kind = res.Candidate.Harness
	b.Tier = string(tier)
	b.RoundExcluded = nil
	return b, nil
}

// CandidateRoundOpenError refuses `send --candidate` while the round is
// open. It carries the binding so the CLI can name `relevo stop` next.
type CandidateRoundOpenError struct {
	Name  string
	Round int
}

func (e *CandidateRoundOpenError) Error() string {
	return fmt.Sprintf("binding %q has round %d open; relevo stop %s ends it, then send again with --candidate", e.Name, e.Round, e.Name)
}

// roundOpenIn reports whether the binding's round is open: store's single
// definition of the condition.
func roundOpenIn(entries []store.LogEntry, round int) bool {
	return store.RoundOpen(entries, round)
}

// candidateSendRefused reports whether `relevo send --candidate` is refused
// because the round is open, with one exception: a served binding halted in
// NEEDS YOU may be re-pointed at a named candidate. A halt is a human
// decision point, and naming the candidate continues *this* round without
// stopping first.
//
// "Served" has two spellings, because relevo keeps two copies: the server's
// carries Owner and a headless builder, the client's Builder.Mode ==
// ModeRemote and no Owner. This is the same "not local" test
// housekeep.RunningProcs uses.
func candidateSendRefused(b store.Binding, entries []store.LogEntry) bool {
	if !roundOpenIn(entries, b.Round) {
		return false
	}
	served := b.Owner != "" || b.Builder.Remote()
	return !served || b.State != store.StateNeedsYou
}

// HasPromptEntry reports whether the log holds a to-builder prompt entry of
// round. It reads store's shared definition rather than repeating it.
func HasPromptEntry(entries []store.LogEntry, round int) bool {
	return store.HasPrompt(entries, round)
}
