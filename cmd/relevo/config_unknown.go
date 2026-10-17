package main

import (
	"strings"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// guardUnknownFields refuses unknown fields on agents.<n> and actors.<n> on
// config set, refusing only keys present in updated and absent from the stored body.
func guardUnknownFields(rt relevo.Runtime, sec config.Section, oldBody, updated []byte) error {
	if sec != config.Agents && sec != config.Actors {
		return nil
	}
	if oldBody == nil {
		if stored, present, err := rt.Config.Body(sec); err != nil {
			return err
		} else if present {
			oldBody = stored
		}
	}
	newKeys := roles.UnknownKeys(string(sec), updated)
	if len(newKeys) == 0 {
		return nil
	}
	oldSet := make(map[string]bool)
	if len(oldBody) > 0 {
		for _, k := range roles.UnknownKeys(string(sec), oldBody) {
			oldSet[k] = true
		}
	}
	var added []string
	for _, k := range newKeys {
		if !oldSet[k] {
			added = append(added, k)
		}
	}
	if len(added) == 0 {
		return nil
	}

	key := added[0]
	fullPath := string(sec) + "." + key
	known := roles.KnownFields(string(sec))
	knownStr := strings.Join(known, ", ")

	if sec == config.Agents {
		actor := strings.Split(key, ".")[0]
		return fail(codeConfigInvalid, "%s: unknown field (known: %s); outcomes are declared on the actor: relevo config set actors.%s.outputs '{...}'", fullPath, knownStr, actor)
	}
	return fail(codeConfigInvalid, "%s: unknown field (known: %s)", fullPath, knownStr)
}
