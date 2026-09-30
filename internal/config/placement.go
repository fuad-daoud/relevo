package config

import (
	"fmt"
	"sort"
)

// checkActorPlacement cross-checks every role's placement against the servers
// section: each entry is the local sentinel or the name of a configured server.
// It reads the built registry rather than the actors section directly, so it
// covers the actors path and a placement a legacy roles row carried alike, and
// it runs inside decodeDoc once the registry exists, so the read path and a
// writer's prospective check refuse the same documents.
func checkActorPlacement(L *Loaded) error {
	names := L.Registry.Names()
	sort.Strings(names)

	for _, name := range names {
		role, ok := L.Registry.Role(name)
		if !ok {
			continue
		}
		for i, entry := range role.Placement {
			if entry == "local" {
				continue
			}
			if _, ok := L.Servers[entry]; !ok {
				return fmt.Errorf("actors: %s.placement[%d]: server %q is not in the servers section", name, i, entry)
			}
		}
	}
	return nil
}
