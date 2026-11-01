package main

import (
	"strings"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// guardUnknownFields refuses unknown fields on config set, refusing only keys
// present in updated and absent from the stored body.
func guardUnknownFields(rt relevo.Runtime, sec config.Section, oldBody, updated []byte) error {
	unknown, known := unknownKeyFuncs(sec)
	if unknown == nil {
		return nil
	}
	if oldBody == nil {
		if stored, present, err := rt.Config.Body(sec); err != nil {
			return err
		} else if present {
			oldBody = stored
		}
	}
	newKeys := unknown(updated)
	if len(newKeys) == 0 {
		return nil
	}
	oldSet := make(map[string]bool)
	if len(oldBody) > 0 {
		for _, k := range unknown(oldBody) {
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
	knownStr := strings.Join(known(), ", ")

	if sec == config.Agents && agentContractField[key[strings.LastIndex(key, ".")+1:]] {
		return fail(codeConfigInvalid, "%s: unknown field (known: %s); outcomes are declared on the actor: relevo config set actors.<actor>.outputs '{...}'", fullPath, knownStr)
	}
	return fail(codeConfigInvalid, "%s: unknown field (known: %s)", fullPath, knownStr)
}

// agentContractField names the fields people reach for when they try to give
// an agent a round contract; only those get the pointer to actor outputs. The
// actor is named generically because one agent can back several actors.
var agentContractField = map[string]bool{"outcomes": true, "outputs": true, "role": true}

// unknownKeyFuncs returns the unknown-key finder and known-key lister for sec;
// nils mean the section's keys are not checked.
func unknownKeyFuncs(sec config.Section) (func([]byte) []string, func() []string) {
	switch sec {
	case config.Agents, config.Actors:
		return func(b []byte) []string { return roles.UnknownKeys(string(sec), b) },
			func() []string { return roles.KnownFields(string(sec)) }
	case config.Candidates:
		return candidate.UnknownKeys, candidate.KnownFields
	case config.Policy:
		return policy.UnknownKeys, policy.KnownFields
	}
	return nil, nil
}
