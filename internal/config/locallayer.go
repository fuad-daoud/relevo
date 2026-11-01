package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The machine-local layer of the config doc. A section's shared body is the
// whole truth for it; a config_local row is a merge-patch over one key of it
// holding this machine's overrides. The fields landing there -- an account's
// config dir and home, the policy's serve block -- name things existing only
// here, so a merged read is the only view any caller sees.
//
// A patch is stored rather than a whole value so a shared edit that touches a
// neighbouring key still reaches this machine, and one row per doc key holds
// every field that key overrides.

// readLocalPatches returns every override whose key belongs to sec, keyed by
// doc key. It reads through the transaction so a validated prospective
// document is one snapshot rather than two.
func readLocalPatches(t *db.Tx, sec Section) (map[string]json.RawMessage, error) {
	keys, err := t.ConfigLocalKeys(string(sec))
	if err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(keys))
	for _, key := range keys {
		patch, ok, err := t.ConfigLocalGet(key)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		out[key] = patch
	}
	return out, nil
}

// mergeLocal overlays sec's overrides on body. An override naming a key body
// lacks is skipped: the local layer never introduces an item of its own, so a
// stale override cannot conjure an account Load would refuse.
func mergeLocal(sec Section, body json.RawMessage, patches map[string]json.RawMessage) (json.RawMessage, error) {
	if len(patches) == 0 {
		return body, nil
	}
	if !perItemSections[sec] {
		patch, ok := patches[string(sec)]
		if !ok {
			return body, nil
		}
		return mergePatch(body, patch)
	}

	items, err := splits(sec, body)
	if err != nil {
		return nil, err
	}
	prefix := string(sec) + keySeparator
	for key, patch := range patches {
		name, ok := strings.CutPrefix(key, prefix)
		if !ok {
			continue
		}
		if _, held := items[name]; !held {
			continue
		}
		merged, err := mergePatch(items[name], patch)
		if err != nil {
			return nil, fmt.Errorf("config_local %s: %w", key, err)
		}
		items[name] = merged
	}
	return joins(sec, items)
}

// mergePatch applies an RFC 7386 merge-patch to base: a null member removes,
// an object member recurses, anything else replaces. An array merges member
// by member under each element's name, because an array's index is not an
// identity two machines agree on.
//
// The empty object changes nothing. It is what a diff of equal values
// produces, and merging it as an object would empty a field nobody touched.
func mergePatch(base, patch json.RawMessage) (json.RawMessage, error) {
	var pv map[string]json.RawMessage
	if err := json.Unmarshal(patch, &pv); err != nil || pv == nil {
		// A non-object patch replaces, the one case RFC 7386 leaves to the value.
		return append(json.RawMessage(nil), compactJSON(patch)...), nil
	}
	if len(pv) == 0 {
		return base, nil
	}

	var bv map[string]json.RawMessage
	if err := json.Unmarshal(base, &bv); err != nil || bv == nil {
		bv = map[string]json.RawMessage{}
	}
	out := make(map[string]json.RawMessage, len(bv)+len(pv))
	for key, value := range bv {
		out[key] = value
	}
	for key, value := range pv {
		if string(value) == "null" {
			delete(out, key)
			continue
		}
		if existing, ok := out[key]; ok {
			merged, err := mergePatch(existing, value)
			if err != nil {
				return nil, err
			}
			out[key] = merged
			continue
		}
		out[key] = value
	}
	return json.Marshal(out)
}

// writeLocalPatches stores the overrides a shared write implies in the
// caller's transaction, so the stripped shared half and the resupplying local
// row commit together: neither is right alone.
func writeLocalPatches(t *db.Tx, patches map[string]json.RawMessage, now time.Time) error {
	for _, key := range slices.Sorted(maps.Keys(patches)) {
		if err := t.ConfigLocalPut(key, patches[key], now); err != nil {
			return err
		}
	}
	return nil
}

// mergedDoc overlays every section's overrides on the shared document, the
// only document any reader or writer validates or decodes.
func mergedDoc(t *db.Tx, shared Doc) (Doc, error) {
	out := make(Doc, len(shared))
	for sec, body := range shared {
		patches, err := readLocalPatches(t, sec)
		if err != nil {
			return nil, err
		}
		merged, err := mergeLocal(sec, body, patches)
		if err != nil {
			return nil, err
		}
		out[sec] = merged
	}
	return out, nil
}

func mergedBody(t *db.Tx, sec Section, body json.RawMessage) (json.RawMessage, error) {
	patches, err := readLocalPatches(t, sec)
	if err != nil {
		return nil, err
	}
	return mergeLocal(sec, body, patches)
}

func compactJSON(raw json.RawMessage) []byte {
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		return append([]byte(nil), raw...)
	}
	return out.Bytes()
}
