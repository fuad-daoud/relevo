package release

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

const TTL = 24 * time.Hour

const cacheKey = "release-check"

// Cache is the last answer relevo got from the release endpoint.
type Cache struct {
	Latest    string    `json:"latest"`
	CheckedAt time.Time `json:"checked_at"`
	Source    string    `json:"source"`
}

// Load reads the cache. An absent or malformed cache is (Cache{}, false,
// nil): a corrupt cache never fails its caller.
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

func Save(kv db.KV, c Cache) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal release cache: %w", err)
	}
	data = append(data, '\n')
	return kv.KVPut(cacheKey, data)
}

func Stale(c Cache, ok bool, now time.Time, ttl time.Duration) bool {
	if !ok {
		return true
	}
	return now.Sub(c.CheckedAt) > ttl
}
