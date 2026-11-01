package synclog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/fuad-daoud/relevo/internal/db"
)

// BodyHash is the hex sha256 `head` carries for a row. It runs over the canonical
// encoding, not arrival bytes: same columns in another order hash equal, while one
// changed byte still hashes different.
func BodyHash(body json.RawMessage) (string, error) {
	row, err := DecodeBody(body)
	if err != nil {
		return "", fmt.Errorf("synclog: body hash: %w", err)
	}
	canonical, err := EncodeBody(db.ExchangeRow{Columns: columnsOf(row)})
	if err != nil {
		return "", fmt.Errorf("synclog: body hash: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// columnsOf turns a decoded body into the column slice EncodeBody takes, sorted
// so the slice is deterministic rather than a map iteration's accident.
func columnsOf(row map[string]any) []db.ExchangeColumn {
	names := make([]string, 0, len(row))
	for name := range row {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]db.ExchangeColumn, 0, len(names))
	for _, name := range names {
		out = append(out, db.ExchangeColumn{Name: name, Value: row[name]})
	}
	return out
}
