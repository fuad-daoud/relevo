package config

import (
	"fmt"
	"sort"
)

// checkActorPlacement cross-checks every actor's placement against the servers
// section: each entry is the local sentinel or the name of a configured server.
// It runs inside decodeDoc after loadServers and loadActors, so the read path
// and a writer's prospective check refuse the same documents.
func checkActorPlacement(L *Loaded) error {
	names := make([]string, 0, len(L.Actors))
	for name := range L.Actors {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		for i, entry := range L.Actors[name].Placement {
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
