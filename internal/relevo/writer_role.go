package relevo

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// BindingRole is the writer role b runs: b.Role, or "builder" when it is "".
// It is exported for internal/serve, which has a store.Binding and needs the
// same name a client-side binding would run (#382).
func BindingRole(b store.Binding) string {
	if b.Role == "" {
		return "builder"
	}
	return b.Role
}

// bindingRole is BindingRole for this package: every place that used to pass
// the literal "builder" as a role asks this instead.
func bindingRole(b store.Binding) string {
	return BindingRole(b)
}

// normRole is the stored form of a requested role: "" and "builder" both give
// "", because a builder binding never records the word "builder".
func normRole(role string) string {
	if role == "builder" {
		return ""
	}
	return role
}

// NormRole is normRole for callers outside this package. It is exported for
// internal/serve, which resolves a remote add's role against the server's own
// roles.json and must store the same "" for builder (#382).
func NormRole(role string) string {
	return normRole(role)
}

// actorShape returns store.ShapeWriter or store.ShapeReader for role (after
// normRole; "" means builder) in reg. A name no role has is an error wrapping
// ErrUnknownRole.
func actorShape(reg *roles.Registry, role string) (string, error) {
	r, ok := reg.Role(bindingRole(store.Binding{Role: normRole(role)}))
	if !ok {
		return "", fmt.Errorf("unknown actor %q (known: %v): %w", role, reg.Names(), ErrUnknownRole)
	}
	if r.Shape == harness.ShapeBuilder {
		return store.ShapeWriter, nil
	}
	return store.ShapeReader, nil
}

// ActorShape is actorShape for callers outside this package. It is exported
// for internal/serve, which resolves a remote create's actor against the
// server's own roles.json, never the client's (#382), and refuses a reader
// because readers are local-only in A5.
func ActorShape(rt Runtime, role string) (string, error) {
	return actorShape(rt.RoleRegistry(), role)
}

// roleChecks reports whether a binding of role takes policy.json's gate.default
// when neither --gate nor --no-gate is given: the role's Check, or true when
// the registry does not know the role (the caller has already refused that).
func roleChecks(reg *roles.Registry, role string) bool {
	if r, ok := reg.Role(role); ok {
		return r.Check
	}
	return true
}

// bindingSpec is the launch spec for a stored binding's round: the binding's
// role's definition for kind. A role that roles.json no longer defines is an
// error wrapping ErrUnknownRole -- never a fallback to builder (#382 §5.4).
func bindingSpec(rt Runtime, b store.Binding, kind string) (harness.RoleSpec, error) {
	role := bindingRole(b)
	if _, ok := rt.RoleRegistry().Role(role); !ok {
		return harness.RoleSpec{}, fmt.Errorf("binding %s runs actor %q, which config actors no longer defines: %w", b.Name, role, ErrUnknownRole)
	}
	return rt.RoleRegistry().Spec(role, kind)
}
