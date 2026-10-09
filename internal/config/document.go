package config

import (
	"errors"

	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// decodeDoc derives everything Load reads from section bodies: the parse of
// candidates, policy, roles, agents, actors, accounts, prices, servers and
// hooks, the actors-wins roles rebuild, the registry, and the placement
// cross-check of that registry against the servers section. It touches no
// database, so the read path and a writer's prospective check share one decode.
func decodeDoc(doc Doc) (Loaded, error) {
	var L Loaded

	candBody, candOK, err := loadCandidates(doc, &L)
	if err != nil {
		return Loaded{}, err
	}
	polBody, polOK, err := loadPolicy(doc, &L)
	if err != nil {
		return Loaded{}, err
	}
	rolesPresent, err := loadRoles(doc, &L)
	if err != nil {
		return Loaded{}, err
	}
	agents, err := loadAgents(doc, &L)
	if err != nil {
		return Loaded{}, err
	}
	if err := loadActors(doc, &L, agents, rolesPresent, candBody, candOK, polBody, polOK); err != nil {
		return Loaded{}, err
	}
	if err := loadPrices(doc, &L); err != nil {
		return Loaded{}, err
	}
	if err := loadServers(doc, &L); err != nil {
		return Loaded{}, err
	}
	if err := loadHooks(doc, &L); err != nil {
		return Loaded{}, err
	}
	if err := loadAccounts(doc, &L); err != nil {
		return Loaded{}, err
	}
	if err := loadWorkflows(doc, &L); err != nil {
		return Loaded{}, err
	}
	// The rotation check reads both sections, so it runs once the policy and
	// the accounts are parsed.
	if err := policy.ValidateRotation(FileName(Policy), L.Policy.AccountsRotation(), opencodeGroups(L.Accounts)); err != nil {
		return Loaded{}, err
	}

	reg, err := roles.Build(L.RolesFile, L.Candidates, L.Policy)
	if err != nil {
		return Loaded{}, err
	}
	L.Registry = reg
	// The placement cross-check reads the built registry, so it runs after
	// the build: it covers the actors path and any legacy roles row alike.
	if err := checkActorPlacement(&L); err != nil {
		return Loaded{}, err
	}
	return L, nil
}

// ErrInvalidValue marks a refusal of the value a writer was handed: the value
// is what the store declined to keep, not a failure of the store. A caller can
// probe it with errors.Is and report the refusal as the user's input rather
// than as an internal error.
var ErrInvalidValue = errors.New("invalid config value")

// invalidValue is ErrInvalidValue carried alongside the refusal it was derived
// from. Error returns the underlying text byte for byte, so wrapping never
// reaches the line a caller prints, and Unwrap yields both the sentinel and
// the original error, so a probe for either still succeeds under the wrapper.
type invalidValue struct{ cause error }

func (e invalidValue) Error() string { return e.cause.Error() }

func (e invalidValue) Unwrap() []error { return []error{ErrInvalidValue, e.cause} }

// invalidValueOf tags err as a refusal of a value, or returns nil unchanged so
// the writers can wrap every refusal point without a nil guard at each one.
func invalidValueOf(err error) error {
	if err == nil {
		return nil
	}
	return invalidValue{cause: err}
}

// validateProspective reports the error the next Load would return for doc, so
// a writer refuses exactly what the read refuses and prints the same line. The
// error carries ErrInvalidValue: refusing a document a write would have
// produced is a statement about that value, and every writer refusal point
// routes through here.
func validateProspective(doc Doc) error {
	_, err := decodeDoc(doc)
	return invalidValueOf(err)
}

// copyDoc returns doc with every body copied, so a writer can replace or drop a
// section without touching the snapshot it diffs against: record reads the
// stored document, and a body changed in place would diff as a no-op.
func copyDoc(doc Doc) Doc {
	out := make(Doc, len(doc))
	for sec, body := range doc {
		out[sec] = append([]byte(nil), body...)
	}
	return out
}

// docWithout returns a copy of doc without sec's body.
func docWithout(doc Doc, sec Section) Doc {
	out := copyDoc(doc)
	delete(out, sec)
	return out
}
