package view

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/sanitize"
	"github.com/fuad-daoud/relevo/internal/text"
)

// FormatCandidates renders the configured candidates without latency: the
// listing FormatCandidatesLatency prints when no probe history is loaded.
func FormatCandidates(set *candidate.Set, gates []availability.Gate) string {
	return FormatCandidatesLatency(set, gates, nil)
}

// FormatCandidatesLatency renders the configured candidates for `relevo
// candidates`, with a token's p50 time to first output when probe history
// carries it. It is a listing, not a check -- zero candidates prints the same
// sentence the bind refusal uses, so the mastermind learns the file name once.
func FormatCandidatesLatency(set *candidate.Set, gates []availability.Gate, lat map[string]availability.Summary) string {
	return formatCandidatesLatency(set, gates, lat, func(c candidate.Candidate) string {
		return strings.Join(c.Roles, ", ")
	}, true)
}

// FormatCandidatesLatencyFor is FormatCandidatesLatency with the roles column
// read from reg. In file mode the tier segment is omitted, because there the
// tier belongs to the role, not the candidate.
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
// reg.Names() order, falling back to the candidate's own Roles when no
// registry role serves it. It returns nil when the token does not parse, the
// set is nil, or neither source has any role, so a caller can drop the
// segment whole rather than print an empty one.
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

// formatCandidatesLatency is the one line renderer behind both forms.
//
// Every free-text piece of a row goes through sanitize.StripControls on the way out. The
// block is a table of columns, so a single control byte in a token, a role name
// or a gate's wording does not merely print: it shifts every column after it and
// lets the row repaint whatever the terminal draws next. The gate texts come
// from the availability history, which another installation can write, so they
// are as untrusted as the rest. Column widths are measured on the sanitized
// text, which is what keeps the alignment exact: for a clean string the two are
// the same bytes and the block prints exactly as it did before.
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
		if n := len(sanitize.StripControls(set.NameOf(ref))); n > nameWidth {
			nameWidth = n
		}
		if n := len(sanitize.StripControls(ref)); n > tokWidth {
			tokWidth = n
		}
	}
	var sb strings.Builder
	for _, ref := range refs {
		parsed, _ := candidate.ParseRef(ref)
		c, err := set.Lookup(parsed)
		if err != nil {
			continue
		}
		// `relevo config` is read through a pipe, so the block carries no SGR
		// escapes. The name and the token size their columns separately.
		fmt.Fprintf(&sb, "%-*s  %-*s  %s",
			nameWidth, sanitize.StripControls(set.NameOf(ref)), tokWidth, sanitize.StripControls(ref), sanitize.StripControls(rolesFor(c)))
		if withTier && c.Tier != "" {
			sb.WriteString("   tier: " + sanitize.StripControls(c.Tier))
		}
		if len(c.ExtraArgs) > 0 {
			sb.WriteString("   [" + sanitize.StripControls(strings.Join(c.ExtraArgs, " ")) + "]")
		}
		if s, ok := lat[ref]; ok && s.N > 0 {
			fmt.Fprintf(&sb, "   ttft p50 %s (n=%d, 30d)", text.MS(s.TTFTP50MS), s.N)
		}
		h, _ := harness.Lookup(c.Harness)
		if flag := h.ExtraArgsPermissionFlag(c.ExtraArgs); flag != "" {
			fmt.Fprintf(&sb, `   note: extra_args carries %s; launches at tier harness only -- move it to "tier"`, flag)
		}
		if rowGates := byToken[ref]; len(rowGates) > 0 {
			sb.WriteString("   unavailable: " + sanitize.StripControls(strings.Join(mergeGateTexts(rowGates), "; ")))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// mergeGateTexts renders one token's gates as the parts of the unavailable:
// note: gates are grouped by their kind and until time, in first-seen order,
// so a candidate gated for three roles prints one part, not three. A group
// any role scopes names the roles it covers, sorted and de-duplicated.
//
// Both halves are sanitized: a gate's kind and the roles that scope it are
// free text from the availability history, which another installation can
// write, and this is where they become a line on a terminal.
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
		kind, until := availability.GateKindText(g.Kind), g.UntilText()
		if g.Kind == availability.Scheduled {
			kind = g.Note
		}
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
			parts = append(parts, strings.TrimSpace(sanitize.StripControls(grp.kind)+" "+sanitize.StripControls(grp.until)))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%s) %s", sanitize.StripControls(grp.kind), sanitize.StripControls(strings.Join(uniqSorted(grp.roles), ", ")), sanitize.StripControls(grp.until)))
	}
	return parts
}

// uniqSorted returns the distinct values of in, sorted.
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
