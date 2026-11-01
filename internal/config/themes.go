package config

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/fuad-daoud/relevo/internal/board/theme"
)

// The themes section: one board palette per name, keyed by the name itself.
// The section is per-item, so two machines editing two palettes converge on
// one entry each instead of displacing each other's. The palettes are seed
// data: the release writes cockpit and blueprint into the section once per
// database (EnsureThemesSeed), after which every lookup reads what is stored
// -- a palette a user unsets stays unset.

// ParseThemes reads and checks the themes body: an object keyed by theme
// name, each value one palette parsed and colour-checked through theme.Parse.
// Only names are validated here beyond that parse; each entry's own refusals
// name the entry and field they were found on.
func ParseThemes(body []byte) (map[string]theme.Theme, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", Themes, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("%s: section is null", Themes)
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make(map[string]theme.Theme, len(raw))
	for _, key := range keys {
		if len(raw[key]) == 0 || string(raw[key]) == "null" {
			return nil, fmt.Errorf("%s: %s: entry is null", Themes, key)
		}
		t, err := theme.Parse(key, raw[key])
		if err != nil {
			return nil, err
		}
		out[key] = t
	}
	return out, nil
}

// EncodeThemes renders the themes section body: one entry per palette with
// its eleven colours, keys sorted, so the same set always encodes to the
// same bytes.
func EncodeThemes(m map[string]theme.Theme) ([]byte, error) {
	body, err := theme.Encode(m)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Themes, err)
	}
	return body, nil
}

// loadThemes fills L.Themes from the stored body. An absent section is an
// empty map, so a machine that never seeded reads none.
func loadThemes(doc Doc, L *Loaded) error {
	body, ok := doc[Themes]
	if !ok {
		L.Themes = map[string]theme.Theme{}
		return nil
	}
	t, err := ParseThemes(body)
	if err != nil {
		return err
	}
	L.Themes = t
	return nil
}
