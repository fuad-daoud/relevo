package resolve

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// NormRole is the stored form of a requested role: "" and "builder" both give
// "", because a builder binding never records the word "builder".
func NormRole(role string) string {
	if role == "builder" {
		return ""
	}
	return role
}

// ActorShape returns store.ShapeWriter or store.ShapeReader for role in reg.
func ActorShape(reg *roles.Registry, role string) (string, error) {
	r, ok := reg.Role(store.Binding{Role: NormRole(role)}.WriterRole())
	if !ok {
		return "", fmt.Errorf("unknown actor %q (known: %v): %w", role, reg.Names(), ErrUnknownRole)
	}
	if r.Shape == harness.ShapeBuilder {
		return store.ShapeWriter, nil
	}
	return store.ShapeReader, nil
}

// RoleChecks reports whether a binding of role takes policy.json's
// gate.default when neither --gate nor --no-gate is given: the role's Check,
// or true when the registry does not know the role (the caller has already
// refused that).
func RoleChecks(reg *roles.Registry, role string) bool {
	if r, ok := reg.Role(role); ok {
		return r.Check
	}
	return true
}

// BindingSpec is the launch spec for a stored binding's round: the binding's
// role's definition for kind. A role the registry does not define is an error
// wrapping ErrUnknownRole, never a fallback to builder.
func BindingSpec(reg *roles.Registry, b store.Binding, kind string) (harness.RoleSpec, error) {
	role := b.WriterRole()
	if _, ok := reg.Role(role); !ok {
		return harness.RoleSpec{}, fmt.Errorf("binding %s runs actor %q, which config actors no longer defines: %w", b.Name, role, ErrUnknownRole)
	}
	return reg.Spec(role, kind)
}
