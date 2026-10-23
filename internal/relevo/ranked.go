package relevo

import (
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// rankedEntry is one row of the walk; T4's FormatPolicy renders the same rows.
type rankedEntry struct {
	Candidate candidate.Candidate
	How       How // HowOrder or HowUnlisted
	Position  int // 1-based index in order[role] for HowOrder; 0 for HowUnlisted
	Off       bool
}

// rankedRole is one role's candidates in the registry's ranked order, as the
// walk wants them: the candidate behind each token is looked up in set, and an
// entry whose token does not parse or is not configured is skipped here. An
// unknown role has no ranked list, so the result is nil.
func rankedRole(reg *roles.Registry, set *candidate.Set, role string) []rankedEntry {
	return rankedRoleFor(reg, set, role, nil)
}

// rankedRoleFor is rankedRole with every entry whose candidate carries a
// Demote gate moved to the end, keeping relative order. The walk, the config
// rows and the refusal check all read this one partition so they cannot
// disagree.
func rankedRoleFor(reg *roles.Registry, set *candidate.Set, role string, gates []availability.Gate) []rankedEntry {
	info, ok := reg.Role(role)
	if !ok {
		return nil
	}
	var out []rankedEntry
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
			out = append(out, rankedEntry{Candidate: c, How: HowOrder, Position: r.Position, Off: r.Off})
			continue
		}
		out = append(out, rankedEntry{Candidate: c, How: HowUnlisted, Off: r.Off})
	}
	return demoteLast(out, gatesForRole(gates, role))
}

// demoteLast moves the entries whose candidate has a Demote gate to the end,
// stably.
func demoteLast(in []rankedEntry, gates []availability.Gate) []rankedEntry {
	demoted := make(map[string]bool)
	for _, g := range gates {
		if g.Demote {
			demoted[g.Token] = true
		}
	}
	if len(demoted) == 0 {
		return in
	}
	out := make([]rankedEntry, 0, len(in))
	var tail []rankedEntry
	for _, e := range in {
		if demoted[e.Candidate.Ref().String()] {
			tail = append(tail, e)
			continue
		}
		out = append(out, e)
	}
	return append(out, tail...)
}

// rankedList is the order's configured, serving entries first, then every
// other serving candidate in ref order; an order entry relevo cannot use is
// skipped here and reported by PolicyWarnings (T4), never an error, because
// removing a candidate must not break every subcommand.
//
// It is the legacy registry's ranked list: rankedRoleFor over the derivation
// of set and pol.
func rankedList(set *candidate.Set, pol policy.Policy, role string, gates ...availability.Gate) []rankedEntry {
	return rankedRoleFor(legacyRegistry(set, pol), set, role, gates)
}

// skipGates is gates without the Demote ones: a demote window reorders the
// walk, it never skips or refuses a candidate.
func skipGates(gates []availability.Gate) []availability.Gate {
	out := make([]availability.Gate, 0, len(gates))
	for _, g := range gates {
		if !g.Demote {
			out = append(out, g)
		}
	}
	return out
}
