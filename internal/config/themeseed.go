package config

import (
	"encoding/json"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/board/theme"
	"github.com/fuad-daoud/relevo/internal/db"
)

// The theme seed: the release's palettes written into the themes section
// once per database. The write is a raw migration write, like the
// local-layer move: it records no revision, and the local kv marker beside
// it is what a later open reads, so a palette a user unsets after the seed
// stays unset.
const themeSeedMarker = "board.themes.seeded.v1"

// themeSeeded is the marker's value. It is a JSON string because a kv row
// stores a JSON document.
var themeSeeded = []byte(`"seeded"`)

// EnsureThemesSeed writes each palette the embedded seed file carries into
// the themes section when that name is absent, then records the marker. It
// runs once per database, and a second call with the marker set reads and
// writes nothing. Every machine seeds identical values, so the per-item
// `themes/<name>` keys converge across a sync. It returns whether it wrote.
//
// Absence is judged against the shared layer and the writes land there: the
// local layer never introduces an item of its own, so a locally-removed
// palette still sits in the shared body, where a seed writes nothing, and the
// user's own local override above it is untouched.
func (s *Store) EnsureThemesSeed() (bool, error) {
	_, done, err := s.db.KVGet(themeSeedMarker)
	if err != nil || done {
		return false, err
	}

	wrote := false
	err = s.db.Tx(func(t *db.Tx) error {
		// The marker is re-checked inside the transaction, so two processes
		// opening the same database cannot both seed: the second commits a
		// no-op over the first one's body.
		if _, done, err := t.KVGet(themeSeedMarker); err != nil || done {
			return err
		}
		items := map[string]json.RawMessage{}
		if body, ok, err := t.ConfigGet(string(Themes)); err != nil {
			return err
		} else if ok {
			if err := json.Unmarshal(body, &items); err != nil {
				return fmt.Errorf("%s: stored body: %w", Themes, err)
			}
		}
		for _, name := range theme.SeedNames() {
			if _, present := items[name]; present {
				continue
			}
			items[name] = theme.SeedEntries()[name]
			wrote = true
		}
		if wrote {
			body, err := json.Marshal(items)
			if err != nil {
				return err
			}
			if err := t.ConfigPut(string(Themes), body, s.now().UTC()); err != nil {
				return err
			}
		}
		return t.KVPut(themeSeedMarker, themeSeeded)
	})
	if err != nil {
		return false, err
	}
	return wrote, nil
}
