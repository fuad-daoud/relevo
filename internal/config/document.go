package config

import "github.com/fuad-daoud/relevo/internal/roles"

// decodeDoc derives everything Load reads from section bodies: the parse of
// candidates, policy, roles, agents, actors, prices, servers and hooks, the
// actors-wins roles rebuild, the actors x servers placement cross-check, and
// the registry. It touches no database, so the read path and a writer's
// prospective check share one decode.
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
	if err := checkActorPlacement(&L); err != nil {
		return Loaded{}, err
	}
	if err := loadHooks(doc, &L); err != nil {
		return Loaded{}, err
	}

	reg, err := roles.Build(L.RolesFile, L.Candidates, L.Policy)
	if err != nil {
		return Loaded{}, err
	}
	L.Registry = reg
	return L, nil
}

// validateProspective reports the error the next Load would return for doc,
// unwrapped, so a writer refuses exactly what the read refuses and prints the
// same line.
func validateProspective(doc Doc) error {
	_, err := decodeDoc(doc)
	return err
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
