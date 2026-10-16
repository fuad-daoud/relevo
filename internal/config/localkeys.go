package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The one key vocabulary the config doc uses. A doc entry addresses a single
// key, so the keys an entry can name, the rows config_local holds and the
// machine-field patches are all written against are the same strings here
// rather than three derivations that could drift apart.

// perItemSections sync as one key per item, so two machines editing two
// different accounts converge on one entry each instead of one entry per
// section in which either machine's edit would displace the other's.
var perItemSections = map[Section]bool{
	Candidates: true,
	Agents:     true,
	Actors:     true,
	Accounts:   true,
	Servers:    true,
	Repos:      true,
	Themes:     true,
}

// keySeparator joins a section name to an item name in a doc key.
const keySeparator = "/"

// itemKey is the doc key of one item of a per-item section.
func itemKey(sec Section, name string) string {
	return string(sec) + keySeparator + name
}

// splitKey divides a doc key into its section and, for a per-item section, the
// item it names. A key this machine has no shape for is refused rather than
// guessed at: applying it would write to a section or an item the entry never
// named.
func splitKey(key string) (Section, string, error) {
	sec, name, split := strings.Cut(key, keySeparator)
	section := Section(sec)
	if !known(section) {
		return "", "", fmt.Errorf("config doc key %q names no section", key)
	}
	if !perItemSections[section] {
		if split {
			return "", "", fmt.Errorf("config doc key %q names an item of a whole-section section", key)
		}
		return section, "", nil
	}
	if !split || name == "" || strings.Contains(name, keySeparator) {
		return "", "", fmt.Errorf("config doc key %q names no item of section %s", key, section)
	}
	return section, name, nil
}

// itemName reads the name an item of an array-shaped section carries. The
// section's own validator is what says a name is well formed; this only has to
// find it, so an absent name is a refusal and a malformed one is left to
// validation.
func itemName(row map[string]json.RawMessage) (string, error) {
	raw, ok := row["name"]
	if !ok {
		return "", fmt.Errorf("item has no name")
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		return "", fmt.Errorf("item name is not text: %w", err)
	}
	return name, nil
}

// splits writes the body of a per-item section into its items, keyed by the
// item's own name. Candidates and accounts are arrays of named rows, so their
// index is not an identity two machines agree on; agents, actors and servers
// are objects keyed by the name already.
func splits(sec Section, body json.RawMessage) (map[string]json.RawMessage, error) {
	if !perItemSections[sec] {
		return map[string]json.RawMessage{string(sec): body}, nil
	}
	if namedArraySection(sec) {
		return splitNamedArray(sec, body)
	}
	return splitNamedObject(sec, body)
}

// namedArraySection reports whether a per-item section stores its items as an
// array of objects each carrying a name, rather than as an object keyed by it.
func namedArraySection(sec Section) bool { return sec == Candidates || sec == Accounts }

func splitNamedArray(sec Section, body json.RawMessage) (map[string]json.RawMessage, error) {
	var rows []json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("%s: %w", sec, err)
	}
	out := make(map[string]json.RawMessage, len(rows))
	for _, raw := range rows {
		var row map[string]json.RawMessage
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, fmt.Errorf("%s: item is not an object: %w", sec, err)
		}
		name, err := itemName(row)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", sec, err)
		}
		out[name] = raw
	}
	return out, nil
}

func splitNamedObject(sec Section, body json.RawMessage) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("%s: %w", sec, err)
	}
	if obj == nil {
		return nil, fmt.Errorf("%s: section is null", sec)
	}
	return obj, nil
}

// joins rebuilds a per-item section's body from its items, in sorted name
// order. The order is derived rather than remembered because a body built from
// a map has no other order to keep: two machines then write the same bytes for
// the same items, so a key that did not change does not diff as changed.
func joins(sec Section, items map[string]json.RawMessage) (json.RawMessage, error) {
	if !perItemSections[sec] {
		return items[string(sec)], nil
	}
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)

	if !namedArraySection(sec) {
		return json.Marshal(items)
	}
	rows := make([]json.RawMessage, 0, len(names))
	for _, name := range names {
		row, ok := items[name]
		if !ok {
			continue
		}
		rows = append(rows, row)
	}
	return json.Marshal(rows)
}
