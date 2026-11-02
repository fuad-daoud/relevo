package serve

import (
	"encoding/json"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/db"
)

// The burst node table persists as one JSON document in the machine
// database, in the shape serve.clients keeps its row: the same table the
// head holds in memory, so a restart re-owns its nodes -- offers, verified
// answers, problems and forward marks -- without waiting for a report.

const burstNodesKVKey = "serve.burst.nodes"

// loadBurstTable reads the row; an absent row is a fresh table, the state
// of a head that has never reported. A row that does not decode is
// corruption or a foreign writer, and refusing it at startup beats running
// the report loop on a half-read node list -- startup fails like it does
// for an unreadable clients row. Neither load nor save re-sorts: the rows
// are kept in the provider's response order, and placement breaks its
// running-count ties by it.
func loadBurstTable(kv db.KV) (*burst.NodeTable, error) {
	data, ok, err := kv.KVGet(burstNodesKVKey)
	if err != nil {
		return nil, err
	}
	if !ok {
		return &burst.NodeTable{}, nil
	}
	var t burst.NodeTable
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("burst node table %s: %w", burstNodesKVKey, err)
	}
	return &t, nil
}

// saveBurstTable writes the whole table; every merge and failure that
// changes it is followed by one save, so a crash costs at most the write
// that was in flight.
func saveBurstTable(kv db.KV, t *burst.NodeTable) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return kv.KVPut(burstNodesKVKey, data)
}
