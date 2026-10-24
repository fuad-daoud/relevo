package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
)

// The repos section: one repository's settings, keyed by the repository's
// identity -- the lower-case hex sha256 of its root commit, the same hash the
// bind verb already travels on the wire as remote.RepoID(root commit). The
// section is a per-item one, so two machines editing two different repositories
// converge on one entry each instead of displacing each other's.

// RepoEntry is one repository's stored settings. Every field is optional: a
// field set to "" is stored as absent, and an entry with no field left is not
// stored at all. Name is a display label only; gate is the acceptance command
// the repository's bindings take when no --gate names one; board_theme is an
// opaque string the board round owns; trimpath is on or off, unset meaning on.
// The unset default is never stored as itself -- an entry holding only it
// would drop as empty -- so an entry that wants on beside nothing else stores
// the word `on` explicitly instead.
type RepoEntry struct {
	Name       string `json:"name,omitempty"`
	Gate       string `json:"gate,omitempty"`
	BoardTheme string `json:"board_theme,omitempty"`
	Trimpath   string `json:"trimpath,omitempty"`
}

// repoEntryFields is the field vocabulary one entry carries. A field the entry
// carries beyond these four is refused by name rather than ignored, so a
// typo'd key never silently stores nothing.
var repoEntryFields = []string{"name", "gate", "board_theme", "trimpath"}

// ParseRepos reads and checks the repos body: an object keyed by repository
// identity, each value an object carrying at most the four known string
// fields, trimpath's restricted to on and off. A body that is not an object, an entry that is not an object (a null
// entry included), an unknown field and a non-string value are each refused,
// naming the section and the entry they were found on. Empty fields are
// dropped, and an entry left with no field at all is dropped, so a body this
// parse accepts encodes back to itself.
func ParseRepos(body []byte) (map[string]RepoEntry, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", Repos, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("%s: section is null", Repos)
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make(map[string]RepoEntry, len(raw))
	for _, key := range keys {
		if len(raw[key]) == 0 || string(raw[key]) == "null" {
			return nil, fmt.Errorf("%s: %s: entry is null", Repos, key)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw[key], &fields); err != nil {
			return nil, fmt.Errorf("%s: %s: entry is not an object: %w", Repos, key, err)
		}
		entry, err := parseRepoEntry(key, fields)
		if err != nil {
			return nil, err
		}
		if entry == (RepoEntry{}) {
			// An entry with no field left is not stored at all.
			continue
		}
		out[key] = entry
	}
	return out, nil
}

// parseRepoEntry validates one entry's fields and folds them into an entry.
// Fields are walked in sorted order, so a body with two problems refuses on
// the same one whichever order the map handed them back in.
func parseRepoEntry(key string, fields map[string]json.RawMessage) (RepoEntry, error) {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)

	var out RepoEntry
	for _, name := range names {
		if !slices.Contains(repoEntryFields, name) {
			return RepoEntry{}, fmt.Errorf("%s: %s: unknown field %q; a repos entry takes name, gate, board_theme and trimpath", Repos, key, name)
		}
		value := fields[name]
		if len(value) == 0 || string(value) == "null" {
			continue
		}
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			return RepoEntry{}, fmt.Errorf("%s: %s: %s is not text: %w", Repos, key, name, err)
		}
		switch name {
		case "name":
			out.Name = s
		case "gate":
			out.Gate = s
		case "board_theme":
			out.BoardTheme = s
		case "trimpath":
			if s != "on" && s != "off" {
				return RepoEntry{}, fmt.Errorf("%s: %s: trimpath is %q, want on or off", Repos, key, s)
			}
			out.Trimpath = s
		}
	}
	return out, nil
}

// EncodeRepos renders the repos section body: one entry per repository with
// its empty fields absent, and an entry left empty dropped. The map's keys
// sort, so the same set always encodes to the same bytes.
func EncodeRepos(m map[string]RepoEntry) ([]byte, error) {
	out := make(map[string]RepoEntry, len(m))
	for key, entry := range m {
		if entry == (RepoEntry{}) {
			continue
		}
		out[key] = entry
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Repos, err)
	}
	return body, nil
}

// loadRepos fills L.Repos from the stored body. An absent section is an empty
// map, so a machine that never used the feature reads no entry and no git.
func loadRepos(doc Doc, L *Loaded) error {
	body, ok := doc[Repos]
	if !ok {
		L.Repos = map[string]RepoEntry{}
		return nil
	}
	r, err := ParseRepos(body)
	if err != nil {
		return err
	}
	L.Repos = r
	return nil
}
