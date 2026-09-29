package roles

import "github.com/fuad-daoud/relevo/internal/harness"

// builtins derives one Role per built-in role name from harness.RoleByName:
// the table in internal/harness stays the single source, so a change to
// roleTable is picked up here without a second table to keep in step.
//
// A built-in is a writer exactly when its shape is ShapeBuilder, and it
// carries the shipped agent definition for every known kind, so Custom is
// always false. It has no candidates and no tier of its own: those come from
// policy.json in legacy mode or from roles.json in file mode.
func builtins() map[string]Role {
	out := make(map[string]Role, len(harness.RoleNames()))
	for _, name := range harness.RoleNames() {
		spec, ok := harness.RoleByName(name)
		if !ok {
			continue
		}
		role := Role{
			Name:        name,
			Shape:       spec.Shape,
			Check:       spec.Shape == harness.ShapeBuilder,
			Builtin:     true,
			Definitions: make(map[string]Definition, len(harness.All())),
		}
		var requires []string
		if len(spec.Definitions) > 1 {
			requires = spec.Definitions[1:]
		}
		for _, h := range harness.All() {
			role.Definitions[h.Kind] = Definition{
				Agent:    spec.Definition,
				Requires: append([]string(nil), requires...),
				Custom:   false,
			}
		}
		out[name] = role
	}
	return out
}
