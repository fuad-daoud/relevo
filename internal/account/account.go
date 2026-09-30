// Package account models a harness login: the quota groups it serves and the
// pure rules that choose among several logins for one provider. It spawns
// nothing and reads nothing.
package account

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
)

// Kind is the harness kind an account logs into.
type Kind string

const (
	Claude   Kind = "claude"
	Codex    Kind = "codex"
	OpenCode Kind = "opencode"
)

// namePattern is the account name shape, the same as a candidate name. The
// charset excludes "@", the separator of an account gate key.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,23}$`)

// Account is one named login of one harness kind. Exactly one kind's selector
// fields are set; the fields are typed rather than a free environment map so an
// account cannot inject arbitrary process environment.
type Account struct {
	Name        string   `json:"name"`
	Harness     Kind     `json:"harness"`
	Groups      []string `json:"groups"`
	ConfigDir   string   `json:"config_dir,omitempty"` // claude
	Home        string   `json:"home,omitempty"`       // codex
	Integration string   `json:"integration,omitempty"`
	Label       string   `json:"label,omitempty"`
}

// Set is a parsed accounts section, in config order.
type Set []Account

// Parse decodes and validates the accounts section. known lists, per harness
// kind, the quota groups that kind's candidates use; a nil map skips the
// warning for a group no candidate uses.
func Parse(name string, data []byte, known map[Kind][]string) (Set, []string, error) {
	var set Set
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, nil, fmt.Errorf("decode %s: %w", name, err)
	}

	seenNames := make(map[string]int, len(set))
	seenRows := make(map[string]int)
	var warnings []string
	for i, a := range set {
		if err := validate(name, i, a); err != nil {
			return nil, nil, err
		}
		if first, ok := seenNames[a.Name]; ok {
			return nil, nil, fmt.Errorf("%s: account %d: duplicate name %q at index %d and %d", name, i, a.Name, first, i)
		}
		seenNames[a.Name] = i
		if a.Harness == OpenCode {
			// One credential row cannot be two accounts: the active-row record
			// that a flip updates would be ambiguous.
			row := a.Integration + "@" + a.Label
			if first, ok := seenRows[row]; ok {
				return nil, nil, fmt.Errorf("%s: account %d: opencode integration %q label %q is already account %d", name, i, a.Integration, a.Label, first)
			}
			seenRows[row] = i
		}
		warnings = append(warnings, groupWarnings(name, a, known)...)
	}
	return set, warnings, nil
}

// validate checks one account's name, kind and selector fields.
func validate(name string, i int, a Account) error {
	if !namePattern.MatchString(a.Name) {
		return fmt.Errorf("%s: account %d: name %q: want ^[a-z0-9][a-z0-9.-]{0,23}$", name, i, a.Name)
	}
	switch a.Harness {
	case Claude:
		if a.ConfigDir == "" {
			return fmt.Errorf("%s: account %d: %s: config_dir is required for a claude account", name, i, a.Name)
		}
		return foreignSelector(name, i, a, a.Home != "" || a.Integration != "" || a.Label != "", "home", "integration", "label")
	case Codex:
		if a.Home == "" {
			return fmt.Errorf("%s: account %d: %s: home is required for a codex account", name, i, a.Name)
		}
		return foreignSelector(name, i, a, a.ConfigDir != "" || a.Integration != "" || a.Label != "", "config_dir", "integration", "label")
	case OpenCode:
		if a.Integration == "" || a.Label == "" {
			return fmt.Errorf("%s: account %d: %s: integration and label are required for an opencode account", name, i, a.Name)
		}
		return foreignSelector(name, i, a, a.ConfigDir != "" || a.Home != "", "config_dir", "home")
	case "agy":
		return fmt.Errorf("%s: account %d: %s: agy offers no login selector", name, i, a.Name)
	default:
		return fmt.Errorf("%s: account %d: %s: unknown harness %q (known: claude, codex, opencode)", name, i, a.Name, a.Harness)
	}
}

// foreignSelector refuses a selector field that belongs to another kind, which
// is how an account that cannot be honoured is caught at load rather than at
// spawn.
func foreignSelector(name string, i int, a Account, foreign bool, fields ...string) error {
	if !foreign {
		return nil
	}
	return fmt.Errorf("%s: account %d: %s: %s belong to another kind", name, i, a.Name, joinFields(fields))
}

func joinFields(fields []string) string {
	switch len(fields) {
	case 1:
		return fields[0]
	case 2:
		return fields[0] + " and " + fields[1]
	default:
		return fields[0] + ", " + fields[1] + " and " + fields[2]
	}
}

// groupWarnings warns for each group no candidate of the account's harness
// uses: a stale candidate is likelier than a broken account, so it is not an
// error.
func groupWarnings(name string, a Account, known map[Kind][]string) []string {
	if known == nil {
		return nil
	}
	used := make(map[string]bool, len(known[a.Harness]))
	for _, g := range known[a.Harness] {
		used[g] = true
	}
	var out []string
	seen := make(map[string]bool, len(a.Groups))
	for _, g := range a.Groups {
		if used[g] || seen[g] {
			continue
		}
		seen[g] = true
		out = append(out, fmt.Sprintf("%s: %s: group %q matches no %s candidate", name, a.Name, g, a.Harness))
	}
	return out
}

// Pool returns the accounts of harness that serve group, in config order.
func (s Set) Pool(harness Kind, group string) []Account {
	var out []Account
	for _, a := range s {
		if a.Harness == harness && slices.Contains(a.Groups, group) {
			out = append(out, a)
		}
	}
	return out
}
