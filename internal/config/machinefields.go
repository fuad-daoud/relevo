package config

import (
	"encoding/json"
	"fmt"
)

// Which fields of the config doc name things on the machine that wrote them,
// and so never leave it. An account's config dir and home are directories on
// this filesystem, and the policy's serve block configures this host's server.
// They are stripped out of every shared write and land in config_local instead,
// while the shared body keeps every field that means the same thing everywhere.

// machineFields lists, per section, the fields routed to the local layer. A
// whole-section field is taken off the section object; on a per-item section
// the same fields are taken off every item, under that item's name.
// Hooks is the path-aware case: `local` is the member the outside-root argv
// split puts a row into. The split is decided by path -- a row naming a file
// under the config root travels, anything else is this machine's -- and the
// rule below only has to say which member of the value those rows travel in.
var machineFields = map[Section][]string{
	Accounts: {"config_dir", "home"},
	Policy:   {"serve"},
	Hooks:    {hooksLocalMember},
}

// stripsMachineFields reports whether sec carries any field routed local.
func stripsMachineFields(sec Section) bool { return len(machineFields[sec]) > 0 }

// splitMachineFields divides one section body into the part that stays shared
// and the overrides describing this machine, keyed by doc key. The split is
// total in both directions: no machine field survives in the shared half and no
// shared field is diverted, so what a peer receives is the whole of what means
// the same thing everywhere.
func splitMachineFields(sec Section, body json.RawMessage) (shared json.RawMessage, patches map[string]json.RawMessage, err error) {
	if !stripsMachineFields(sec) {
		return body, nil, nil
	}
	fields := machineFields[sec]
	if perItemSections[sec] {
		return splitItemMachineFields(sec, body, fields)
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return nil, nil, fmt.Errorf("%s: section is not an object: %w", sec, err)
	}
	taken := takeFields(obj, fields)
	if len(taken) == 0 {
		return body, nil, nil
	}
	patch, err := json.Marshal(taken)
	if err != nil {
		return nil, nil, err
	}
	remaining, err := json.Marshal(obj)
	if err != nil {
		return nil, nil, err
	}
	return remaining, map[string]json.RawMessage{string(sec): patch}, nil
}

func splitItemMachineFields(sec Section, body json.RawMessage, fields []string) (json.RawMessage, map[string]json.RawMessage, error) {
	items, err := splits(sec, body)
	if err != nil {
		return nil, nil, err
	}
	patches := map[string]json.RawMessage{}
	for name, item := range items {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(item, &obj); err != nil || obj == nil {
			return nil, nil, fmt.Errorf("%s: item %s is not an object: %w", sec, name, err)
		}
		taken := takeFields(obj, fields)
		if len(taken) == 0 {
			continue
		}
		patch, err := json.Marshal(taken)
		if err != nil {
			return nil, nil, err
		}
		remaining, err := json.Marshal(obj)
		if err != nil {
			return nil, nil, err
		}
		items[name] = remaining
		patches[itemKey(sec, name)] = patch
	}
	if len(patches) == 0 {
		return body, nil, nil
	}
	remaining, err := joins(sec, items)
	if err != nil {
		return nil, nil, err
	}
	return remaining, patches, nil
}

// takeFields removes fields from obj and returns what it took.
func takeFields(obj map[string]json.RawMessage, fields []string) map[string]json.RawMessage {
	taken := make(map[string]json.RawMessage, len(fields))
	for _, field := range fields {
		value, ok := obj[field]
		if !ok {
			continue
		}
		taken[field] = value
		delete(obj, field)
	}
	return taken
}

// clearedFields returns the overrides a write must add to clear the machine
// fields the written body does not carry: one null per field the stored body
// holds and the written body does not. Without them a field unset on this
// machine would keep coming back from the row the previous write left, and
// `relevo config unset` on it would report success while changing nothing.
func clearedFields(sec Section, stored, body json.RawMessage, patches map[string]json.RawMessage) map[string]json.RawMessage {
	fields, ok := machineFields[sec]
	if !ok {
		return patches
	}
	if perItemSections[sec] {
		return clearedItemFields(sec, stored, body, fields, patches)
	}
	storedSet := fieldSet(stored, fields)
	bodySet := fieldSet(body, fields)
	for _, field := range fields {
		if _, held := storedSet[field]; !held {
			continue
		}
		if _, kept := bodySet[field]; !kept {
			patches = addPatch(patches, string(sec), field, json.RawMessage("null"))
		}
	}
	return patches
}

func clearedItemFields(sec Section, stored, body json.RawMessage, fields []string, patches map[string]json.RawMessage) map[string]json.RawMessage {
	storedItems, err := splits(sec, stored)
	if err != nil {
		return patches
	}
	bodyItems, err := splits(sec, body)
	if err != nil {
		return patches
	}
	for name, item := range storedItems {
		held := fieldSet(item, fields)
		if len(held) == 0 {
			continue
		}
		kept := map[string]json.RawMessage{}
		if item, ok := bodyItems[name]; ok {
			kept = fieldSet(item, fields)
		}
		for _, field := range fields {
			if _, was := held[field]; !was {
				continue
			}
			if _, still := kept[field]; !still {
				patches = addPatch(patches, itemKey(sec, name), field, json.RawMessage("null"))
			}
		}
	}
	return patches
}

// addPatch records one field value in the row for key, keeping any field
// already recorded there. A row is written whole, so a set and a clear landing
// on the same key have to meet before the write rather than overwrite.
func addPatch(patches map[string]json.RawMessage, key, field string, value json.RawMessage) map[string]json.RawMessage {
	if patches == nil {
		patches = map[string]json.RawMessage{}
	}
	current := fieldSet(patches[key], []string{field})
	if current == nil {
		current = map[string]json.RawMessage{}
	}
	current[field] = value
	merged, err := json.Marshal(current)
	if err != nil {
		return patches
	}
	patches[key] = merged
	return patches
}

func fieldSet(body json.RawMessage, fields []string) map[string]json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(fields))
	for _, field := range fields {
		if value, ok := obj[field]; ok {
			out[field] = value
		}
	}
	return out
}

// dropMachineFields returns value without the fields sec routes local, for one
// arriving item or whole-section body. A value that is not an object is
// returned as it is, for validation to refuse.
func dropMachineFields(sec Section, value json.RawMessage) json.RawMessage {
	if value == nil || !stripsMachineFields(sec) {
		return value
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(value, &obj); err != nil || obj == nil {
		return value
	}
	if len(takeFields(obj, machineFields[sec])) == 0 {
		return value
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return value
	}
	return out
}
