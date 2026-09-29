package view

import (
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// FormatRoles renders the registry for `relevo config`: one block per role in
// reg.Names() order, with a blank line between blocks --
//
//	<name>  <writer|reader>[  check]  tier <tier or ->  (<reg.Source()>)
//	  candidates  <tok>, <tok>      or   candidates  (none)
//	  <kind>  <agent>[ + <req> ...][  (custom)]
//
// It is pure and never fails: `relevo config` lists what is there.
func FormatRoles(reg *roles.Registry) string {
	blocks := make([]string, 0, len(reg.Names()))
	for _, name := range reg.Names() {
		role, ok := reg.Role(name)
		if !ok {
			continue
		}
		blocks = append(blocks, formatRole(reg, role))
	}
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, "\n\n") + "\n"
}

// formatRole renders one role's block, without its trailing newline.
func formatRole(reg *roles.Registry, role roles.Role) string {
	var b strings.Builder

	b.WriteString(role.Name + "  ")
	if role.Shape == harness.ShapeBuilder {
		b.WriteString("writer")
	} else {
		b.WriteString("reader")
	}
	if role.Check {
		b.WriteString("  check")
	}
	tier := "-"
	if t, ok := reg.RoleTier(role.Name); ok {
		tier = string(t)
	}
	b.WriteString("  tier " + tier + "  (" + reg.Source() + ")")

	// File mode lists the row's tokens as written. Legacy mode lists the
	// resolved Ranked list instead, so a role served only by candidates no
	// order names still shows them, marked (unlisted). Both print each
	// candidate's short name.
	b.WriteString("\n  candidates  ")
	if !reg.FileMode() {
		b.WriteString(legacyCandidates(reg, role.Ranked))
	} else {
		b.WriteString(fileCandidates(reg, role))
	}

	for _, kind := range sortedDefinitionKinds(role.Definitions) {
		d := role.Definitions[kind]
		b.WriteString("\n  " + kind + "  " + d.Agent)
		if len(d.Requires) > 0 {
			b.WriteString(" + " + strings.Join(d.Requires, " + "))
		}
		if d.Custom {
			b.WriteString("  (custom)")
		}
	}

	return b.String()
}

// legacyCandidates renders a role's ranked list for the legacy candidates
// line: each candidate's short name in order, a token whose Position is 0 --
// listed by no order entry -- suffixed " (unlisted)", and "(none)" when the
// list is empty.
func legacyCandidates(reg *roles.Registry, ranked []roles.Ranked) string {
	if len(ranked) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(ranked))
	for _, r := range ranked {
		name := reg.NameOf(r.Token)
		if r.Position == 0 {
			names = append(names, name+" (unlisted)")
		} else {
			names = append(names, name)
		}
	}
	return strings.Join(names, ", ")
}

// fileCandidates renders a role's candidates as written: each entry the role
// resolved prints its candidate's short name, and an entry that did not
// resolve stays raw with " (unknown)", because that is the finding.
// "(none)" when the list is empty.
func fileCandidates(reg *roles.Registry, role roles.Role) string {
	if len(role.Candidates) == 0 {
		return "(none)"
	}
	byPosition := make(map[int]string, len(role.Ranked))
	for _, r := range role.Ranked {
		byPosition[r.Position] = r.Token
	}
	parts := make([]string, 0, len(role.Candidates))
	for i, entry := range role.Candidates {
		if tok, ok := byPosition[i+1]; ok {
			parts = append(parts, reg.NameOf(tok))
			continue
		}
		parts = append(parts, entry+" (unknown)")
	}
	return strings.Join(parts, ", ")
}

// sortedDefinitionKinds returns a role's resolved definition kinds, sorted, so
// the block's kind lines never move between runs.
func sortedDefinitionKinds(defs map[string]roles.Definition) []string {
	kinds := make([]string, 0, len(defs))
	for kind := range defs {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}
