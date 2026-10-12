package config

import (
	"bytes"
	"encoding/json"
	"sort"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// txKV lets the hybrid clock keep its position inside the write's own
// transaction, so a stamp is spent only if the write commits.
type txKV struct{ t *db.Tx }

func (k txKV) KVGet(key string) ([]byte, bool, error) { return k.t.KVGet(key) }
func (k txKV) KVPut(key string, v []byte) error       { return k.t.KVPut(key, v) }
func (k txKV) KVDelete(key string) error              { return k.t.KVDelete(key) }

// stampWrites records a stamp for every syncable item whose value differs
// between two documents, in the transaction of the write. The stamp is per
// item: a section-wide one would let an old edit of one item ride a later edit
// of another. An item that arrived from a peer is not stamped.
func (s *Store) stampWrites(t *db.Tx, before, after Doc) error {
	if s.arrived {
		return nil
	}
	changed, err := changedItems(before, after)
	if err != nil || len(changed) == 0 {
		return err
	}
	clock := synclog.NewHybridClockAt(txKV{t}, s.now)
	for _, key := range changed {
		stamp, err := clock.Now()
		if err != nil {
			return err
		}
		if err := t.ConfigWriteStampPut(key, stamp.String()); err != nil {
			return err
		}
	}
	return nil
}

// changedItems is the sorted doc keys whose item was added, changed or removed
// between two documents.
func changedItems(before, after Doc) ([]string, error) {
	old, err := itemsOf(before, true)
	if err != nil {
		return nil, err
	}
	cur, err := itemsOf(after, true)
	if err != nil {
		return nil, err
	}
	var keys []string
	for key, item := range cur {
		if prev, held := old[key]; !held || !sameJSON(prev, item) {
			keys = append(keys, key)
		}
	}
	for key := range old {
		if _, held := cur[key]; !held {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func sameJSON(a, b json.RawMessage) bool {
	var x, y bytes.Buffer
	if json.Compact(&x, a) != nil || json.Compact(&y, b) != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(x.Bytes(), y.Bytes())
}
