package release

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// TTL is how long a cached answer stands before the daemon refreshes it.
const TTL = 24 * time.Hour

const cacheKey = "release-check"

// Cache is the last answer relevo got from the release endpoint, stored in
// the machine database's kv row "release-check" (formerly the file
// <state>/release-check.json).
type Cache struct {
	Latest    string    `json:"latest"`
	CheckedAt time.Time `json:"checked_at"`
	Source    string    `json:"source"`
}

// Load reads the cache. An absent or malformed cache is (Cache{}, false, nil),
// never an error: a corrupt cache must only fail to inform a caller, never
// fail it.
func Load(kv db.KV) (Cache, bool, error) {
	data, ok, err := kv.KVGet(cacheKey)
	if err != nil {
		return Cache{}, false, err
	}
	if !ok {
		return Cache{}, false, nil
	}

	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		return Cache{}, false, nil
	}
	return c, true, nil
}

// Save writes the whole cache document to the kv row "release-check".
func Save(kv db.KV, c Cache) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal release cache: %w", err)
	}
	data = append(data, '\n')
	return kv.KVPut(cacheKey, data)
}

// Stale reports whether a refresh is due: no cache, or CheckedAt older than ttl.
func Stale(c Cache, ok bool, now time.Time, ttl time.Duration) bool {
	if !ok {
		return true
	}
	return now.Sub(c.CheckedAt) > ttl
}
