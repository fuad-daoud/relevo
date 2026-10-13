package config

import (
	"encoding/json"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
)

// Moving the machine fields out of the shared config_doc is a step in Go
// rather than a migration in SQL, because the fields have to be read, split out
// and stored as merge patches against their keys, and a SQL migration can only
// rewrite whole rows. It runs once, is idempotent, and is recorded by a local
// kv marker so a machine that has run it is distinguishable from one that has
// not.

// localLayerMarker is the local kv row the move is recorded by. It sits in the
// machine-local file because it is a statement about this machine's own config
// rows, which is exactly what the layer it guards is.
const localLayerMarker = "sync.config_local.v1"

// movedMarker is the marker value. It is a JSON string because a kv row stores
// a JSON document.
var movedMarker = []byte(`"moved"`)

// EnsureLocalLayer moves every machine field out of the shared config and into
// the local layer, recording the marker. moved is false once the marker is set,
// which is what makes a second call a no-op rather than a second move.
func (s *Store) EnsureLocalLayer() (moved bool, err error) {
	_, done, err := s.db.KVGet(localLayerMarker)
	if err != nil || done {
		return false, err
	}

	moved = false
	err = s.db.Tx(func(t *db.Tx) error {
		stored, err := readDoc(t)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		for _, sec := range machineFieldSections() {
			body, ok := stored[sec]
			if !ok {
				continue
			}
			sharedBody, patches, err := splitMachineFields(sec, body)
			if err != nil {
				return err
			}
			if len(patches) == 0 {
				continue
			}
			if err := writeLocalPatches(t, patches, now); err != nil {
				return err
			}
			if err := t.ConfigPut(string(sec), sharedBody, now); err != nil {
				return err
			}
			moved = true
		}
		return t.KVPut(localLayerMarker, movedMarker)
	})
	if err != nil {
		return false, err
	}
	return moved, nil
}

// localLayerReady refuses while the machine fields are still in the shared
// config, which is the state an exporter must not send a peer: until the move
// has run, the shared bodies still carry this machine's directories.
func (s *Store) localLayerReady() error {
	_, done, err := s.db.KVGet(localLayerMarker)
	if err != nil {
		return err
	}
	if !done {
		return fmt.Errorf("config: machine fields are still in the shared config: %w", ErrInvalidValue)
	}
	return nil
}

func machineFieldSections() []Section {
	out := make([]Section, 0, len(machineFields))
	for _, sec := range Sections {
		if stripsMachineFields(sec) {
			out = append(out, sec)
		}
	}
	return out
}

// PutLocal stores doc as this machine's overrides rather than as shared
// sections, so nothing in it is written where another machine could read it.
//
// The patches are diffs against what the shared layer already says, which is
// what keeps an override from freezing a neighbouring key: a peer that changes
// an account's groups still reaches this machine, and only the field this write
// named is held here.
func (s *Store) PutLocal(doc Doc) error {
	return s.db.Tx(func(t *db.Tx) error {
		for _, sec := range Sections {
			next, ok := doc[sec]
			if !ok {
				continue
			}
			patches, err := localPatchesFor(t, sec, next)
			if err != nil {
				return invalidValueOf(err)
			}
			if err := writeLocalPatches(t, patches, s.now().UTC()); err != nil {
				return err
			}
		}
		return validateLocalProspective(t, doc)
	})
}

// localPatchesFor returns the overrides turning sec's merged value into next,
// one row per doc key. The local layer overrides what is shared and never adds
// or removes a section or an item, so a write that would is refused: it is a
// shared write the caller has to make without --local.
func localPatchesFor(t *db.Tx, sec Section, next json.RawMessage) (map[string]json.RawMessage, error) {
	shared, ok, err := t.ConfigGet(string(sec))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%s: the shared config has no such section, and adding one is a shared write", sec)
	}
	if string(compactJSON(next)) == "null" {
		return nil, fmt.Errorf("%s: a local override cannot remove a shared section", sec)
	}
	current, err := mergedBody(t, sec, shared)
	if err != nil {
		return nil, err
	}

	patches := map[string]json.RawMessage{}
	if perItemSections[sec] {
		heldItems, err := splits(sec, current)
		if err != nil {
			return nil, err
		}
		nextItems, err := splits(sec, next)
		if err != nil {
			return nil, err
		}
		for name := range nextItems {
			if _, held := heldItems[name]; !held {
				return nil, fmt.Errorf("%s: the shared config has no such item, and adding one is a shared write", itemKey(sec, name))
			}
		}
		for name := range heldItems {
			if _, kept := nextItems[name]; !kept {
				return nil, fmt.Errorf("%s: a local override cannot remove a shared item", itemKey(sec, name))
			}
			patches, err = diffInto(t, patches, itemKey(sec, name), heldItems[name], nextItems[name])
			if err != nil {
				return nil, err
			}
		}
		return patches, nil
	}
	return diffInto(t, patches, string(sec), current, next)
}

// diffInto records the merge-patch turning base into next under key, laid over
// the row already stored there. The row accumulates rather than replacing,
// because each write's patch is a diff against the merged value and a row
// replaced by one would drop every override an earlier write had stored. An
// absent next is a removal, which the patch expresses as a null, and a next
// equal to base is no override at all, so nothing is written for it.
func diffInto(t *db.Tx, patches map[string]json.RawMessage, key string, base, next json.RawMessage) (map[string]json.RawMessage, error) {
	diff := diffPatch(base, next)
	if string(diff) == "{}" {
		return patches, nil
	}
	held, _, err := t.ConfigLocalGet(key)
	if err != nil {
		return patches, err
	}
	if len(held) == 0 {
		patches[key] = diff
		return patches, nil
	}
	merged, err := mergePatch(held, diff)
	if err != nil {
		return patches, err
	}
	patches[key] = merged
	return patches, nil
}

// diffPatch returns the merge-patch that turns base into next: a member next
// does not have is a null, a member it has recurses, and a value of another
// kind is next itself.
//
// Both sides are a single key's value, never a whole array: an array-shaped
// section is taken apart into its named items first, so the patch is taken per
// item and an array is never diffed by position.
func diffPatch(base, next json.RawMessage) json.RawMessage {
	out := map[string]json.RawMessage{}

	var nextObj map[string]json.RawMessage
	if err := json.Unmarshal(next, &nextObj); err == nil && nextObj != nil {
		baseObj := decodeObject(base)
		for key := range baseObj {
			if _, ok := nextObj[key]; !ok {
				out[key] = json.RawMessage("null")
			}
		}
		for key, value := range nextObj {
			out[key] = diffPatch(baseObj[key], value)
		}
		return mustJSON(out)
	}

	if string(compactJSON(base)) == string(compactJSON(next)) {
		return json.RawMessage("{}")
	}
	return compactJSON(next)
}

// validateLocalProspective refuses a local write that would leave the merged
// config invalid, on the same terms a shared write is refused: an override is
// still a config this machine has to run.
func validateLocalProspective(t *db.Tx, doc Doc) error {
	shared, err := readDoc(t)
	if err != nil {
		return err
	}
	for sec, body := range doc {
		prospective := copyDoc(shared)
		prospective[sec] = body
		merged, err := mergedDoc(t, prospective)
		if err != nil {
			return invalidValueOf(err)
		}
		if err := validateProspective(merged); err != nil {
			return err
		}
	}
	return nil
}

func decodeObject(raw json.RawMessage) map[string]json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return map[string]json.RawMessage{}
	}
	return obj
}

func mustJSON(v any) json.RawMessage {
	out, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return out
}
