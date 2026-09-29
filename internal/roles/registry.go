package roles

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
)

// ErrNoDefinition reports a role with no definition for a harness kind, or a
// name no role has. It is recoverable: a caller turns it into a gate or
// relevo.ErrRoleNotServed.
var ErrNoDefinition = errors.New("role has no definition for this harness kind")

// Source constants name where a Registry was built from.
const (
	// SourceFile is a registry built from roles.json.
	SourceFile = "roles.json"
	// SourceActors is a registry built from the actors and agents sections
	// (A2 round 2).
	SourceActors = "actors"
	// SourceLegacy is a registry derived from candidates.json and policy.json.
	SourceLegacy = "legacy"
)

// Definition is the resolved agent definition of one role on one kind.
type Definition struct {
	// Agent is the definition's name; it becomes a file name.
	Agent string
	// Requires is every definition the agent dispatches to, beside itself.
	Requires []string
	// Custom is false exactly when Agent and every Requires name are shipped
	// names for that kind (harness.IsShipped). A false Custom means relevo
	// installs and refreshes the file; a true one means it never touches it.
	Custom bool
}

// Ranked is one candidate in a role's ranked list.
type Ranked struct {
	// Token is the canonical harness/provider/model reference.
	Token string
	// Position is the token's 1-based index in the list it came from, or 0
	// for "unlisted, after order" (legacy mode only).
	Position int
	// Off is true when the entry is off: it keeps its position and Resolved
	// still includes it, so an explicit `--candidate <off one>` is served; only
	// the pick with no explicit token skips it (A2 §3.4).
	Off bool
}

// Role is one role the registry knows.
type Role struct {
	Name string
	// Shape is harness.ShapeBuilder for a writer, harness.ShapeConsult for a
	// reader.
	Shape harness.RoleShape
	// Check marks a writer role whose round closes on a check.
	Check bool
	// Builtin is true for a role relevo's own table defines.
	Builtin bool
	// Candidates is the role's tokens as written: order[R] in legacy mode
	// (nil when absent), the row's candidates in file mode. In file mode an
	// entry may be a candidate name or a canonical token.
	Candidates []string
	// Ranked is Candidates filtered to what this machine can run, in order.
	Ranked []Ranked
	// OffCount is how many of Ranked are off, for views (A2 §2).
	OffCount int
	// Resolved is Ranked's canonical tokens, in order: what Serves compares
	// a reference against in file mode.
	Resolved []string
	// Ordered is true when the role has a preference order to resolve with.
	Ordered bool
	// Definitions is the resolved definition per harness kind, for the kinds
	// the role can run on.
	Definitions map[string]Definition
}

// Registry is a set of roles, built either from roles.json or from the legacy
// fields. Its fields are unexported, and Role returns a copy, so a caller
// cannot change what the registry resolves.
type Registry struct {
	roles  map[string]Role
	names  []string
	source string
	tiers  map[string]harness.Tier
	// set and pol are kept for the legacy derivation's Serves and TierFor.
	set *candidate.Set
	pol policy.Policy
}

// Build returns the registry for f, or the legacy derivation of set and pol
// when f is nil.
//
// A nil set is treated as an empty set -- a machine with no candidates.json --
// and means no role can serve any candidate; in legacy mode, with no set to
// look up, Serves is false. With a file, a role's tier above
// pol.MaxTierOrDefault() is an error wrapping ErrBadRoles, the same cap
// policy.json enforces on its own tier map. The legacy derivation never
// errors.
func Build(f *File, set *candidate.Set, pol policy.Policy) (*Registry, error) {
	if set == nil {
		// A nil set is a machine with no candidates.json: treated as empty,
		// not as a precondition failure (#374 §4.1). Lookup then misses and
		// ForRole yields nothing.
		set = &candidate.Set{}
	}
	if f == nil {
		return buildLegacy(set, pol), nil
	}
	return buildFile(f, set, pol)
}

// buildLegacy derives the roles from policy.json's order and candidates.json's
// roles, reproducing relevo's rankedList and tier chain exactly (#374 §5.2).
// An order entry may be a candidate name or a canonical token: both go through
// Set.Resolve, and Ranked keeps the canonical token.
func buildLegacy(set *candidate.Set, pol policy.Policy) *Registry {
	byName := builtins()
	names := harness.RoleNames()

	for _, name := range names {
		role := byName[name]
		order := pol.OrderFor(name)
		role.Candidates = order
		role.Ordered = len(order) > 0

		seen := make(map[string]bool, len(order))
		for i, tok := range order {
			// The position counts skipped tokens too, as "order #N" does
			// today: dropping an entry must not renumber the ones after it.
			c, err := set.Resolve(tok)
			if err != nil {
				continue
			}
			if !c.Serves(name) {
				continue
			}
			key := c.Ref().String()
			if seen[key] {
				continue
			}
			seen[key] = true
			role.Ranked = append(role.Ranked, Ranked{Token: key, Position: i + 1})
		}
		for _, c := range set.ForRole(name) {
			if seen[c.Ref().String()] {
				continue
			}
			role.Ranked = append(role.Ranked, Ranked{Token: c.Ref().String(), Position: 0})
		}
		role.Resolved = canonicalTokens(role.Ranked)
		byName[name] = role
	}

	return &Registry{
		roles:  byName,
		names:  append([]string(nil), names...),
		source: SourceLegacy,
		set:    set,
		pol:    pol,
	}
}

// buildFile merges the file's rows into the built-ins, then ranks every role's
// candidates. In file mode roles.json is the only place candidates are
// assigned: a built-in row the file omits has no candidates and an empty
// Ranked (#374 §5.3). An entry may be a candidate name or a canonical token:
// both go through Set.Resolve, and Ranked keeps the canonical token. Two
// entries that resolve to the same token keep the first.
func buildFile(f *File, set *candidate.Set, pol policy.Policy) (*Registry, error) {
	byName := builtins()
	names := append([]string(nil), harness.RoleNames()...)
	tiers := make(map[string]harness.Tier)
	offRaw := make(map[string][]string)
	var newNames []string

	for _, name := range sortedNames(f.Rows) {
		row := f.Rows[name]
		base, ok := byName[name]
		if !ok {
			// #382 §4: a new row's shape comes from the file. A writer
			// defaults its gate to on -- a writer changes the tree, so the
			// project's own check applies, exactly as for the built-in
			// builder -- and a reader has no gate.
			shape := harness.ShapeConsult
			if row.Shape != nil && *row.Shape == "writer" {
				shape = harness.ShapeBuilder
			}
			base = Role{
				Name:        name,
				Shape:       shape,
				Check:       shape == harness.ShapeBuilder,
				Builtin:     false,
				Definitions: make(map[string]Definition),
			}
			newNames = append(newNames, name)
		}

		if row.Check != nil {
			base.Check = *row.Check
		}
		for kind, d := range row.Definitions {
			base.Definitions[kind] = Definition{
				Agent:    d.Agent,
				Requires: append([]string(nil), d.Requires...),
				Custom:   customDefinition(kind, d),
			}
		}
		if row.Candidates != nil {
			base.Candidates = append([]string(nil), row.Candidates...)
		}
		if len(row.Off) > 0 {
			offRaw[name] = append([]string(nil), row.Off...)
		}
		if row.Tier != nil {
			t, err := harness.ParseTier(*row.Tier)
			if err != nil {
				return nil, fmt.Errorf("roles.json: %s.tier: %v: %w", name, err, ErrBadRoles)
			}
			if max := pol.MaxTierOrDefault(); t.Above(max) {
				return nil, fmt.Errorf("roles.json: %s.tier: %s exceeds max_tier %s: %w", name, t, max, ErrBadRoles)
			}
			tiers[name] = t
		}
		byName[name] = base
	}
	names = append(names, newNames...)

	for name, role := range byName {
		role.Ordered = true
		role.Ranked = nil
		role.OffCount = 0
		// offTokens is the canonical token of every raw off entry, so an
		// entry that stays in Ranked can be marked without re-resolving.
		offTokens := make(map[string]bool, len(offRaw[name]))
		for _, raw := range offRaw[name] {
			c, err := set.Resolve(raw)
			if err != nil {
				continue
			}
			offTokens[c.Ref().String()] = true
		}
		seen := make(map[string]bool, len(role.Candidates))
		for i, tok := range role.Candidates {
			c, err := set.Resolve(tok)
			if err != nil {
				// Tolerated here; round 3's `relevo config` warns about it.
				continue
			}
			key := c.Ref().String()
			if seen[key] {
				continue
			}
			if _, ok := role.Definitions[c.Harness]; !ok {
				continue
			}
			seen[key] = true
			off := offTokens[key]
			role.Ranked = append(role.Ranked, Ranked{Token: key, Position: i + 1, Off: off})
			if off {
				role.OffCount++
			}
		}
		role.Resolved = canonicalTokens(role.Ranked)
		byName[name] = role
	}

	return &Registry{
		roles:  byName,
		names:  names,
		source: fileSource(f),
		tiers:  tiers,
		set:    set,
		pol:    pol,
	}, nil
}

// fileSource names where f came from: f.Source when it set one, else
// SourceFile, which is what a roles.json parse leaves empty.
func fileSource(f *File) string {
	if f != nil && f.Source != "" {
		return f.Source
	}
	return SourceFile
}

// canonicalTokens returns a ranked list's canonical tokens, in order.
func canonicalTokens(ranked []Ranked) []string {
	toks := make([]string, 0, len(ranked))
	for _, r := range ranked {
		toks = append(toks, r.Token)
	}
	return toks
}

// customDefinition reports whether kind has to be installed by hand for d:
// false exactly when the agent and every name it requires are shipped names
// for that kind.
func customDefinition(kind string, d DefRow) bool {
	if !harness.IsShipped(kind, d.Agent) {
		return true
	}
	for _, req := range d.Requires {
		if !harness.IsShipped(kind, req) {
			return true
		}
	}
	return false
}

// Names returns every role name: the built-ins in table order, then the file's
// new names, sorted. It returns a copy.
func (r *Registry) Names() []string {
	return append([]string(nil), r.names...)
}

// Role returns the named role. It returns a deep copy, so a caller cannot
// mutate the registry.
func (r *Registry) Role(name string) (Role, bool) {
	role, ok := r.roles[name]
	if !ok {
		return Role{}, false
	}
	return copyRole(role), true
}

// Spec returns the launch spec for name on kind, or an error wrapping
// ErrNoDefinition: `unknown role %q` when no role has the name, `role %q has
// no definition for %s` when the role cannot run on that kind.
func (r *Registry) Spec(name, kind string) (harness.RoleSpec, error) {
	role, ok := r.roles[name]
	if !ok {
		return harness.RoleSpec{}, fmt.Errorf("unknown role %q: %w", name, ErrNoDefinition)
	}
	d, ok := role.Definitions[kind]
	if !ok {
		return harness.RoleSpec{}, fmt.Errorf("role %q has no definition for %s: %w", name, kind, ErrNoDefinition)
	}
	return harness.RoleSpec{
		Name:        name,
		Shape:       role.Shape,
		Definition:  d.Agent,
		Definitions: append([]string{d.Agent}, d.Requires...),
	}, nil
}

// Serves reports whether name can run ref on ref's harness kind. It is false
// when the role has no definition for that kind, and otherwise:
//   - in file mode, when ref's canonical token is in the role's resolved list,
//     which is what a name or a token entry resolved to;
//   - in legacy mode, when the candidate is configured and lists the role.
func (r *Registry) Serves(name string, ref candidate.Ref) bool {
	if _, err := r.Spec(name, ref.Harness); err != nil {
		return false
	}
	if r.FileMode() {
		for _, tok := range r.roles[name].Resolved {
			if tok == ref.String() {
				return true
			}
		}
		return false
	}
	c, err := r.set.Lookup(ref)
	if err != nil {
		return false
	}
	return c.Serves(name)
}

// TierFor returns name's tier, implementing spec §4.1 without the explicit
// flag, which stays at the call site:
//   - in file mode, the role's parsed tier, when the file set one;
//   - in legacy mode, exactly the middle of today's chain: the candidate's
//     tier when set and valid, else policy's tier for the role, else false.
func (r *Registry) TierFor(name string, c candidate.Candidate) (harness.Tier, bool) {
	if r.FileMode() {
		t, ok := r.tiers[name]
		return t, ok
	}
	if c.Tier != "" {
		if t, err := harness.ParseTier(c.Tier); err == nil {
			return t, true
		}
	}
	return r.pol.TierFor(name)
}

// RoleTier returns name's own tier, without a candidate: the file tier in file
// mode, and policy's tier for the role in legacy mode (#374 §3.2). ok is false
// when neither sets one.
func (r *Registry) RoleTier(name string) (harness.Tier, bool) {
	if r.FileMode() {
		t, ok := r.tiers[name]
		return t, ok
	}
	return r.pol.TierFor(name)
}

// NameOf returns the short name (A1 §4.4) of the candidate whose canonical
// token is token, delegating to the registry's own set. It returns token
// unchanged when the registry has no set or holds no such candidate, and
// never errors. Display only: everything that decides stays on the token.
func (r *Registry) NameOf(token string) string {
	if r == nil || r.set == nil {
		return token
	}
	return r.set.NameOf(token)
}

// Source returns where the registry's roles came from: SourceFile,
// SourceActors or SourceLegacy.
func (r *Registry) Source() string {
	return r.source
}

// FileMode reports whether the registry's roles were given in full by a file
// or the actors section, rather than derived from the legacy candidates and
// policy fields. Every source test goes through it, so a new file-like source
// needs no caller changed (A2 round 2 R2).
func (r *Registry) FileMode() bool { return r.source != SourceLegacy }

// ListText names where role's candidates are listed, for embedding in error
// texts: the file mode's `roles.json <role>.candidates`, the actors mode's
// `actors <role>.candidates`, and legacy's `order.<role>` (A2 round 2 R2).
func (r *Registry) ListText(role string) string {
	switch r.source {
	case SourceActors:
		return "actors " + role + ".candidates"
	case SourceLegacy:
		return "order." + role
	default:
		return "roles.json " + role + ".candidates"
	}
}

// copyRole returns a deep copy of role.
func copyRole(role Role) Role {
	out := role
	out.Candidates = append([]string(nil), role.Candidates...)
	out.Ranked = append([]Ranked(nil), role.Ranked...)
	out.Resolved = append([]string(nil), role.Resolved...)
	if role.Definitions != nil {
		out.Definitions = make(map[string]Definition, len(role.Definitions))
		for kind, d := range role.Definitions {
			d.Requires = append([]string(nil), d.Requires...)
			out.Definitions[kind] = d
		}
	}
	return out
}
