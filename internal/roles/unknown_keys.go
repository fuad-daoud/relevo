package roles

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/jsonshape"
	"github.com/fuad-daoud/relevo/internal/pathscope"
)

// KnownFields returns the entry-level JSON field names for section, sorted,
// or nil for any section other than "agents" or "actors".
func KnownFields(section string) []string {
	var t reflect.Type
	switch section {
	case "agents":
		t = reflect.TypeOf(AgentEntry{})
	case "actors":
		t = reflect.TypeOf(Actor{})
	default:
		return nil
	}
	var fields []string
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name != "" && name != "-" {
			fields = append(fields, name)
		}
	}
	sort.Strings(fields)
	return fields
}

// UnknownKeys returns the sorted key paths in body that section does not declare
// (entry-level keys, plus keys inside native.<kind> rows and scope).
// It returns nil for any section other than "agents" or "actors".
func UnknownKeys(section string, body []byte) []string {
	if section != "agents" && section != "actors" {
		return nil
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil
	}

	var paths []string
	switch section {
	case "agents":
		agentKeys := jsonshape.Keys(reflect.TypeOf(AgentEntry{}))
		defKeys := jsonshape.Keys(reflect.TypeOf(DefRow{}))
		for _, name := range slices.Sorted(maps.Keys(data)) {
			row, ok := data[name].(map[string]any)
			if !ok {
				continue
			}
			for _, key := range slices.Sorted(maps.Keys(row)) {
				if key == "native" {
					defs, ok := row[key].(map[string]any)
					if !ok {
						continue
					}
					for _, kind := range slices.Sorted(maps.Keys(defs)) {
						def, ok := defs[kind].(map[string]any)
						if !ok {
							continue
						}
						for _, defKey := range slices.Sorted(maps.Keys(def)) {
							if !hasShapeKey(defKeys, defKey) {
								paths = append(paths, name+".native."+kind+"."+defKey)
							}
						}
					}
					continue
				}
				if !hasShapeKey(agentKeys, key) {
					paths = append(paths, name+"."+key)
				}
			}
		}
	case "actors":
		actorKeys := jsonshape.Keys(reflect.TypeOf(Actor{}))
		scopeKeys := jsonshape.Keys(reflect.TypeOf(pathscope.Scope{}))
		for _, name := range slices.Sorted(maps.Keys(data)) {
			row, ok := data[name].(map[string]any)
			if !ok {
				continue
			}
			for _, key := range slices.Sorted(maps.Keys(row)) {
				if key == "scope" {
					scopeObj, ok := row[key].(map[string]any)
					if !ok {
						continue
					}
					for _, scopeKey := range slices.Sorted(maps.Keys(scopeObj)) {
						if !hasShapeKey(scopeKeys, scopeKey) {
							paths = append(paths, name+".scope."+scopeKey)
						}
					}
					continue
				}
				if !hasShapeKey(actorKeys, key) {
					paths = append(paths, name+"."+key)
				}
			}
		}
	}

	sort.Strings(paths)
	return paths
}
