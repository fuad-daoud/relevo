// Package roles reads roles.json: which roles exist, the agent definition
// each runs on each harness kind, the candidates each uses, ranked, and its
// tier. Nothing outside the package uses the registry yet (#374).
package roles

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/jsonshape"
)

// ErrBadRoles reports a roles.json that does not validate. Callers treat it
// exactly as they treat a bad policy.json: fatal at startup.
var ErrBadRoles = errors.New("bad roles")

// roleNamePattern is the shape of a role name. A role name is a map key, a
// state name and a word on the command line, so it stays lowercase and short.
var roleNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// agentNamePattern is the shape of an agent definition name. The name becomes
// a file name, so it admits only what a file name needs.
var agentNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// File is the decoded roles.json: an object keyed by role name.
type File struct {
	// Rows is one entry per role the file names. A role the file omits
	// keeps its built-in row.
	Rows map[string]Row
	// Source names where this File came from: empty means roles.json (the
	// default), SourceActors means it was derived from the actors section
	// (A2 round 2). It is not part of the JSON shape.
	Source string `json:"-"`
}

// Row is one role's entry in roles.json. Every field is optional and
// nil-able, so "absent" can be told apart from "zero": an omitted field
// keeps what the role already had, while an empty list means "none".
type Row struct {
	// Shape is "writer" or "reader". A built-in row may give it only to
	// repeat the built-in shape; a new role must give it. A new writer runs
	// as a binding's role (`relevo bind --worktree --actor` / `relevo bind --actor`); a
	// new reader is bound with `relevo bind --actor`.
	Shape *string `json:"shape"`

	// Check marks a writer role whose round closes on a check. true is
	// refused on a reader role. The JSON key stays "gate": a roles section is
	// only ever the input of the A2 migration, which still carries that key.
	Check *bool `json:"gate,omitempty"`

	// Definitions overrides the agent definition per harness kind. A kind
	// the row omits keeps the shipped definition (built-in roles) or has
	// none (new roles).
	Definitions map[string]DefRow `json:"definitions"`

	// Candidates is the role's candidate tokens, most preferred first.
	// nil is absent; an empty list is allowed and means no candidates.
	Candidates []string `json:"candidates"`

	// Off is the raw candidates that are off: each is kept in Candidates, at
	// its position, but the pick skips it unless it is named explicitly (A2
	// §3.4). Every entry must also appear in Candidates, as the same raw
	// string.
	Off []string `json:"off,omitempty"`

	// Tier is the role's default permission tier.
	Tier *string `json:"tier"`
}

// DefRow is one role's definition for one harness kind.
type DefRow struct {
	// Agent is the definition's name; it becomes a file name. Required.
	Agent string `json:"agent"`
	// Requires is every definition the agent dispatches to, beside itself.
	// Optional.
	Requires []string `json:"requires"`
}

// Load reads and validates roles.json, discarding the unknown-key warnings
// LoadWithWarnings returns. A missing file is nil and not an error, because
// roles.json is optional: absent, the registry derives the roles from the
// legacy candidates.json and policy.json fields.
func Load(path string) (*File, error) {
	f, _, err := LoadWithWarnings(path)
	return f, err
}

// LoadWithWarnings reads and validates roles.json, returning one warning per
// key this relevo does not know (#372 §4.4): a key a newer relevo reads must not
// stop this relevo, and a typo surfaces in `relevo doctor`.
//
// A missing file is nil, nil, nil. A file that does not validate is an error
// wrapping ErrBadRoles, naming the file, the row and the field; the warnings
// found so far are returned with it, as policy.LoadWithWarnings does.
func LoadWithWarnings(path string) (*File, []string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	return Parse(path, raw)
}

// Parse validates roles from data, naming them by name in every message. name
// is the file path when LoadWithWarnings calls it, so an existing message is
// unchanged; internal/config passes the stored section's file name. It applies
// exactly the rules LoadWithWarnings documents.
func Parse(name string, raw []byte) (*File, []string, error) {
	path := name
	var rows map[string]Row
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, nil, fmt.Errorf("%s: %v: %w", path, err, ErrBadRoles)
	}
	if rows == nil {
		// A top-level null decodes into a nil map without an error; the
		// file's top level must be an object.
		return nil, nil, fmt.Errorf("%s: top-level value must be an object: %w", path, ErrBadRoles)
	}

	warnings := rolesUnknownKeyWarnings(path, raw)

	f := &File{Rows: rows}
	if err := validate(path, f); err != nil {
		return nil, warnings, err
	}
	return f, warnings, nil
}

// validate applies every §5.1 rule to f. Rows are checked in sorted name
// order and fields in rule order, so the first error is deterministic.
func validate(path string, f *File) error {
	for _, name := range sortedNames(f.Rows) {
		row := f.Rows[name]
		badField := func(field, reason string) error {
			return fmt.Errorf("%s: %s.%s: %s: %w", path, name, field, reason, ErrBadRoles)
		}

		if !roleNamePattern.MatchString(name) {
			return fmt.Errorf("%s: %s: bad role name: %w", path, name, ErrBadRoles)
		}

		// shape
		shape := harness.ShapeConsult
		builtin, isBuiltin := harness.RoleByName(name)
		if isBuiltin {
			shape = builtin.Shape
		}
		if row.Shape != nil {
			word := *row.Shape
			if word != "writer" && word != "reader" {
				return badField("shape", "must be writer or reader")
			}
			if isBuiltin {
				if word != shapeWord(builtin.Shape) {
					return badField("shape", "built-in role is "+shapeWord(builtin.Shape))
				}
			} else if word == "writer" {
				// #382 §4: a new writer row is accepted. Shape is what the
				// gate check below reads, so record the writer word as one.
				shape = harness.ShapeBuilder
			}
		} else if !isBuiltin {
			return badField("shape", "required for a new role")
		}

		// gate
		if row.Check != nil && *row.Check && shape == harness.ShapeConsult {
			return badField("gate", "a reader role has no gate")
		}

		// definitions
		for _, kind := range sortedDefKinds(row.Definitions) {
			d := row.Definitions[kind]
			if _, ok := harness.Lookup(kind); !ok {
				return badField("definitions."+kind, fmt.Sprintf("unknown harness %q (known: %v)", kind, knownKinds()))
			}
			if d.Agent == "" {
				return badField("definitions."+kind+".agent", "required")
			}
			if !agentNamePattern.MatchString(d.Agent) {
				return badField("definitions."+kind+".agent", fmt.Sprintf("bad name %q", d.Agent))
			}
			for i, req := range d.Requires {
				if !agentNamePattern.MatchString(req) {
					return badField(fmt.Sprintf("definitions.%s.requires[%d]", kind, i), fmt.Sprintf("bad name %q", req))
				}
			}
		}

		// candidates
		seen := make(map[string]bool, len(row.Candidates))
		for i, tok := range row.Candidates {
			// An entry is a candidate name or a canonical token (A1 §4.2).
			if !candidate.IsName(tok) {
				if _, err := candidate.ParseRef(tok); err != nil {
					return badField(fmt.Sprintf("candidates[%d]", i), fmt.Sprintf("%q: want a candidate name or harness/provider/model", tok))
				}
			}
			if seen[tok] {
				return badField(fmt.Sprintf("candidates[%d]", i), fmt.Sprintf("duplicate token %q", tok))
			}
			seen[tok] = true
		}

		// off: every off entry must also be a candidate, as the same raw
		// string (A2 §3.4).
		for i, tok := range row.Off {
			if !seen[tok] {
				return badField(fmt.Sprintf("off[%d]", i), fmt.Sprintf("%q is not in candidates", tok))
			}
		}

		// tier
		if row.Tier != nil {
			if _, err := harness.ParseTier(*row.Tier); err != nil {
				return badField("tier", err.Error())
			}
		}
	}
	return nil
}

// shapeWord renders a shape as the word roles.json uses.
func shapeWord(shape harness.RoleShape) string {
	switch shape {
	case harness.ShapeBuilder:
		return "writer"
	case harness.ShapeConsult:
		return "reader"
	}
	return string(shape)
}

// knownKinds returns every known harness kind, sorted, for error text.
func knownKinds() []string {
	all := harness.All()
	kinds := make([]string, 0, len(all))
	for _, h := range all {
		kinds = append(kinds, h.Kind)
	}
	return kinds
}

// sortedNames returns rows' names, sorted, so validation is deterministic.
func sortedNames(rows map[string]Row) []string {
	names := make([]string, 0, len(rows))
	for name := range rows {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// sortedDefKinds returns a row's definition kinds, sorted.
func sortedDefKinds(defs map[string]DefRow) []string {
	kinds := make([]string, 0, len(defs))
	for kind := range defs {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

// rolesUnknownKeyWarnings returns one warning per decoded key path that Row's
// and DefRow's JSON shapes (jsonshape.Keys) do not declare, formatted exactly
// as policyUnknownKeyWarnings formats its own (#372 §4.4).
//
// The known key sets are derived from the types themselves, so a field added
// to Row or DefRow can never be reported as unknown.
func rolesUnknownKeyWarnings(path string, raw []byte) []string {
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		// The typed decode already reported the shape error.
		return nil
	}

	rowKeys := jsonshape.Keys(reflect.TypeOf(Row{}))
	defKeys := jsonshape.Keys(reflect.TypeOf(DefRow{}))

	var paths []string
	for _, role := range sortedObjectKeys(data) {
		row, ok := data[role].(map[string]any)
		if !ok {
			continue
		}
		for _, key := range sortedObjectKeys(row) {
			if key == "definitions" {
				defs, ok := row[key].(map[string]any)
				if !ok {
					continue
				}
				for _, kind := range sortedObjectKeys(defs) {
					def, ok := defs[kind].(map[string]any)
					if !ok {
						continue
					}
					for _, defKey := range sortedObjectKeys(def) {
						if !hasShapeKey(defKeys, defKey) {
							paths = append(paths, role+".definitions."+kind+"."+defKey)
						}
					}
				}
				continue
			}
			if !hasShapeKey(rowKeys, key) {
				paths = append(paths, role+"."+key)
			}
		}
	}

	sort.Strings(paths)

	base := filepath.Base(path)
	warnings := make([]string, 0, len(paths))
	for _, p := range paths {
		warnings = append(warnings, fmt.Sprintf("%s: unknown key %q (a typo, or a key a newer relevo reads)", base, p))
	}
	return warnings
}

// sortedObjectKeys returns a decoded JSON object's keys, sorted.
func sortedObjectKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// hasShapeKey reports whether k is declared by a jsonshape leaf set: as a leaf
// itself, or as the parent of a nested object ("."), an array ("[]") or a map
// ("{}").
func hasShapeKey(leaves []string, k string) bool {
	if k == "" {
		return false
	}
	for _, l := range leaves {
		if l == k || strings.HasPrefix(l, k+".") || strings.HasPrefix(l, k+"[]") || strings.HasPrefix(l, k+"{}") {
			return true
		}
	}
	return false
}
