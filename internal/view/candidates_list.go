package view

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// FormatCandidates renders the configured candidates without latency: the
// listing FormatCandidatesLatency prints when no probe history is loaded.
func FormatCandidates(set *candidate.Set, gates []availability.Gate) string {
	return FormatCandidatesLatency(set, gates, nil)
}

// FormatCandidatesLatency renders the configured candidates for `relevo
// candidates`: one line per token, sorted, with the roles it serves and any
// extra args in brackets, and -- when gated -- a trailing note naming why and
// until when. A token with successful probes in lat also carries its p50 time
// to first output, so the mastermind can see what a candidate costs to start. It
// is a listing, not a check -- zero candidates prints the same sentence the
// bind refusal uses, so the mastermind learns the file name once.
func FormatCandidatesLatency(set *candidate.Set, gates []availability.Gate, lat map[string]availability.Summary) string {
	return formatCandidatesLatency(set, gates, lat, func(c candidate.Candidate) string {
		return strings.Join(c.Roles, ", ")
	}, true)
}

// FormatCandidatesLatencyFor is FormatCandidatesLatency with the roles column
// read from reg. Legacy mode delegates, so the output stays byte-identical to
// today's; in file mode the column lists the registry roles that serve the
// candidate -- "(no actor)" when none does -- and the tier segment is omitted,
// because in file mode the tier belongs to the role, not the candidate.
func FormatCandidatesLatencyFor(reg *roles.Registry, set *candidate.Set, gates []availability.Gate, lat map[string]availability.Summary) string {
	if !reg.FileMode() {
		return FormatCandidatesLatency(set, gates, lat)
	}
	return formatCandidatesLatency(set, gates, lat, func(c candidate.Candidate) string {
		var served []string
		for _, name := range reg.Names() {
			if reg.Serves(name, c.Ref()) {
				served = append(served, name)
			}
		}
		if len(served) == 0 {
			return "(no actor)"
		}
		return strings.Join(served, ", ")
	}, false)
}

// CandidateRoles returns the role names that serve the candidate token, in
// reg.Names() order, where reg is the registry the actors config builds, which
// is where a machine's roles live now. When no registry role serves it, it
// falls back to the candidate's own Roles as written in candidates.json. It
// returns nil when the token does not parse, the set is nil, or neither source
// has any role, so a caller can drop the segment whole rather than print an
// empty one.
func CandidateRoles(reg *roles.Registry, set *candidate.Set, token string) []string {
	ref, err := candidate.ParseRef(token)
	if err != nil {
		return nil
	}
	var served []string
	for _, name := range reg.Names() {
		if reg.Serves(name, ref) {
			served = append(served, name)
		}
	}
	if len(served) > 0 {
		return served
	}
	if set == nil {
		return nil
	}
	c, err := set.Lookup(ref)
	if err != nil || len(c.Roles) == 0 {
		return nil
	}
	return append([]string(nil), c.Roles...)
}

// formatCandidatesLatency is the one line renderer behind both forms: rolesFor
// renders the roles column, and withTier prints the candidate's own tier
// segment, which only legacy mode does.
func formatCandidatesLatency(set *candidate.Set, gates []availability.Gate, lat map[string]availability.Summary, rolesFor func(candidate.Candidate) string, withTier bool) string {
	if set == nil || set.Len() == 0 {
		return "no candidates configured; set one with relevo config set candidates (see README \"Candidates\")\n"
	}

	byToken := make(map[string][]availability.Gate)
	for _, g := range gates {
		byToken[g.Token] = append(byToken[g.Token], g)
	}

	refs := set.Refs()
	nameWidth, tokWidth := 0, 0
	for _, ref := range refs {
		if n := set.NameOf(ref); len(n) > nameWidth {
			nameWidth = len(n)
		}
		if len(ref) > tokWidth {
			tokWidth = len(ref)
		}
	}
	var sb strings.Builder
	for _, ref := range refs {
		parsed, _ := candidate.ParseRef(ref)
		c, err := set.Lookup(parsed)
		if err != nil {
			continue
		}
		// The candidate's short name leads the row and the token follows it
		// as plain text in its own column, two spaces apart like the other
		// columns. `relevo config` is read through a pipe, so the block must
		// carry no SGR escapes. The name and the token size their columns
		// separately.
		fmt.Fprintf(&sb, "%-*s  %-*s  %s",
			nameWidth, set.NameOf(ref), tokWidth, ref, rolesFor(c))
		if withTier && c.Tier != "" {
			sb.WriteString("   tier: " + c.Tier)
		}
		if len(c.ExtraArgs) > 0 {
			sb.WriteString("   [" + strings.Join(c.ExtraArgs, " ") + "]")
		}
		if s, ok := lat[ref]; ok && s.N > 0 {
			fmt.Fprintf(&sb, "   ttft p50 %s (n=%d, 30d)", availability.ProbeMS(s.TTFTP50MS), s.N)
		}
		h, _ := harness.Lookup(c.Harness)
		if flag := h.ExtraArgsPermissionFlag(c.ExtraArgs); flag != "" {
			fmt.Fprintf(&sb, `   note: extra_args carries %s; launches at tier harness only -- move it to "tier"`, flag)
		}
		if rowGates := byToken[ref]; len(rowGates) > 0 {
			sb.WriteString("   unavailable: " + strings.Join(mergeGateTexts(rowGates), "; "))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// mergeGateTexts renders one token's gates as the parts of the unavailable:
// note: gates are grouped by their kind and until time, in first-seen order,
// so a candidate gated for three roles prints one part, not three. A group
// whose gates all apply to every role renders exactly as it did before --
// `<kind text> <until text>`; a group any role scopes names the roles it
// covers, sorted and de-duplicated:
//
//	agents missing (builder, reviewer) until cleared
func mergeGateTexts(gates []availability.Gate) []string {
	type group struct {
		kind     string
		until    string
		roles    []string
		hasRoles bool
	}

	var groups []*group
	index := make(map[[2]string]*group, len(gates))
	for _, g := range gates {
		kind, until := availability.GateKindText(g.Kind), availability.GateUntilText(g.Until)
		key := [2]string{kind, until}
		grp, ok := index[key]
		if !ok {
			grp = &group{kind: kind, until: until}
			index[key] = grp
			groups = append(groups, grp)
		}
		if g.Role != "" {
			grp.hasRoles = true
			grp.roles = append(grp.roles, g.Role)
		}
	}

	parts := make([]string, 0, len(groups))
	for _, grp := range groups {
		if !grp.hasRoles {
			parts = append(parts, grp.kind+" "+grp.until)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%s) %s", grp.kind, strings.Join(uniqSorted(grp.roles), ", "), grp.until))
	}
	return parts
}

// uniqSorted returns in's distinct values, sorted.
func uniqSorted(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
