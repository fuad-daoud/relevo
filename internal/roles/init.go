package roles

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
)

// ErrAmbiguousTier reports candidates of one role carrying different tiers, so
// no single tier can be written for the role (#374 §3.1). It stops `relevo
// config init` before anything is written.
var ErrAmbiguousTier = errors.New("candidates of one role carry different tiers")

// FromLegacy translates today's candidates.json roles/tier and policy.json
// order/tier into a File (#374 §3.1): the second return is the notes, human
// lines for the CLI to print, in harness.RoleNames() order.
//
// For every ordered role, the file it produces makes the registry pick exactly
// what the legacy registry picks. Shape, Check and Definitions stay nil: legacy
// has no custom definitions, and the built-in shape is implied.
func FromLegacy(set *candidate.Set, pol policy.Policy) (*File, []string, error) {
	reg, _ := Build(nil, set, pol)

	rows := make(map[string]Row, len(harness.RoleNames()))
	var notes []string

	for _, name := range harness.RoleNames() {
		role, _ := reg.Role(name)

		// Candidates is always non-nil, so an empty list says "none"
		// rather than "absent", which would keep the built-in row.
		tokens := make([]string, 0, len(role.Ranked))
		for _, r := range role.Ranked {
			tokens = append(tokens, r.Token)
		}

		tier, err := legacyTier(name, tokens, set, pol)
		if err != nil {
			return nil, nil, err
		}
		rows[name] = Row{Candidates: tokens, Tier: tier}

		if !role.Ordered && len(tokens) > 1 {
			notes = append(notes, fmt.Sprintf("%s: %d candidates had no order (relevo refused to choose); written in ref order -- reorder roles.json %s.candidates", name, len(tokens), name))
		}
		if k := unlistedCount(role.Ranked); k > 0 && role.Ordered {
			notes = append(notes, fmt.Sprintf("%s: %d candidate(s) not in order.%s appended after it, as relevo ranked them", name, k, name))
		}
	}

	return &File{Rows: rows}, notes, nil
}

// legacyTier picks the tier FromLegacy writes for one role, first match wins
// (#374 §3.1): policy's tier for the role; else the candidates' own tier when
// every one of them carries the same non-empty tier; else nil when none does;
// else ErrAmbiguousTier naming each candidate token beside its tier, "none"
// for the ones that set none.
func legacyTier(name string, tokens []string, set *candidate.Set, pol policy.Policy) (*string, error) {
	if t, ok := pol.TierFor(name); ok {
		word := string(t)
		return &word, nil
	}

	distinct := make(map[string]bool)
	var pairs []string
	setCount := 0
	for _, tok := range tokens {
		tier := candidateTier(set, tok)
		if tier == "" {
			pairs = append(pairs, tok+"=none")
			continue
		}
		setCount++
		distinct[tier] = true
		pairs = append(pairs, tok+"="+tier)
	}

	switch {
	case setCount == 0:
		// No candidate carries a tier: the role has none.
		return nil, nil
	case setCount == len(tokens) && len(distinct) == 1:
		var word string
		for t := range distinct {
			word = t
		}
		return &word, nil
	}

	sort.Strings(pairs)
	return nil, fmt.Errorf("%s: %s: %w", name, strings.Join(pairs, ", "), ErrAmbiguousTier)
}

// candidateTier returns the tier tok's configured candidate carries, or "" when
// tok (a candidate name or a token) is not a configured candidate or sets no
// tier.
func candidateTier(set *candidate.Set, tok string) string {
	if set == nil {
		return ""
	}
	c, err := set.Resolve(tok)
	if err != nil {
		return ""
	}
	return c.Tier
}

// unlistedCount returns how many of ranked's entries are not in the role's
// order: Position 0 is "unlisted, after order" in legacy mode.
func unlistedCount(ranked []Ranked) int {
	n := 0
	for _, r := range ranked {
		if r.Position == 0 {
			n++
		}
	}
	return n
}

// Encode renders f as roles.json (#374 §3.1): JSON, two-space indent, a
// trailing newline, and only the fields each row sets -- an omitted field keeps
// what the role already had, so writing one would change the file's meaning.
// Rows are keyed by role name; encoding/json's sorted map-key order is fine.
//
// An empty candidates list is emitted as [], so the round trip through
// LoadWithWarnings says exactly what the file says.
func (f *File) Encode() ([]byte, error) {
	rows := make(map[string]map[string]any, len(f.Rows))
	for name, row := range f.Rows {
		out := make(map[string]any, 5)
		if row.Shape != nil {
			out["shape"] = *row.Shape
		}
		if row.Check != nil {
			out["gate"] = *row.Check
		}
		if row.Definitions != nil {
			out["definitions"] = row.Definitions
		}
		if row.Candidates != nil {
			out["candidates"] = row.Candidates
		}
		if row.Tier != nil {
			out["tier"] = *row.Tier
		}
		rows[name] = out
	}

	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
