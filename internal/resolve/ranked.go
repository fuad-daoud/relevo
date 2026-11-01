package resolve

import (
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// RankedEntry is one row of the walk; FormatPolicy renders the same rows.
type RankedEntry struct {
	Candidate candidate.Candidate
	How       How // HowOrder or HowUnlisted
	Position  int // 1-based index in order[role] for HowOrder; 0 for HowUnlisted
	Off       bool
}

// RankedRole is one role's candidates in the registry's ranked order, as the
// walk wants them: the candidate behind each token is looked up in set, and an
// entry whose token does not parse or is not configured is skipped here. An
// unknown role has no ranked list, so the result is nil.
func RankedRole(reg *roles.Registry, set *candidate.Set, role string) []RankedEntry {
	return RankedRoleFor(reg, set, role, nil)
}

// RankedRoleFor is RankedRole with every entry whose candidate carries a
// Demote gate moved to the end, keeping relative order. The walk, the config
// rows and the refusal check all read this one partition so they cannot
// disagree.
func RankedRoleFor(reg *roles.Registry, set *candidate.Set, role string, gates []availability.Gate) []RankedEntry {
	info, ok := reg.Role(role)
	if !ok {
		return nil
	}
	var out []RankedEntry
	for _, r := range info.Ranked {
		ref, err := candidate.ParseRef(r.Token)
		if err != nil {
			continue
		}
		c, err := set.Lookup(ref)
		if err != nil {
			continue
		}
		if r.Position > 0 {
			out = append(out, RankedEntry{Candidate: c, How: HowOrder, Position: r.Position, Off: r.Off})
			continue
		}
		out = append(out, RankedEntry{Candidate: c, How: HowUnlisted, Off: r.Off})
	}
	return demoteLast(out, GatesForRole(gates, role))
}

// demoteLast moves the entries whose candidate has a Demote gate to the end,
// stably.
func demoteLast(in []RankedEntry, gates []availability.Gate) []RankedEntry {
	demoted := make(map[string]bool)
	for _, g := range gates {
		if g.Demote {
			demoted[g.Token] = true
		}
	}
	if len(demoted) == 0 {
		return in
	}
	out := make([]RankedEntry, 0, len(in))
	var tail []RankedEntry
	for _, e := range in {
		if demoted[e.Candidate.Ref().String()] {
			tail = append(tail, e)
			continue
		}
		out = append(out, e)
	}
	return append(out, tail...)
}

// RankedList is the order's configured, serving entries first, then every
// other serving candidate in ref order; an order entry relevo cannot use is
// skipped here and reported by PolicyWarnings, never an error, because
// removing a candidate must not break every subcommand.
func RankedList(set *candidate.Set, pol policy.Policy, role string, gates ...availability.Gate) []RankedEntry {
	return RankedRoleFor(LegacyRegistry(set, pol), set, role, gates)
}

// SkipGates is gates without the Demote ones: a demote window reorders the
// walk, it never skips or refuses a candidate.
func SkipGates(gates []availability.Gate) []availability.Gate {
	out := make([]availability.Gate, 0, len(gates))
	for _, g := range gates {
		if !g.Demote {
			out = append(out, g)
		}
	}
	return out
}
