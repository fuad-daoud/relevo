package synclog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/fuad-daoud/relevo/internal/db"
)

// BodyHash is the value `head` carries for a row: the hex sha256 of the row's
// body as it is stored in the log.
//
// The digest is taken over the body's canonical encoding rather than over the
// bytes as they arrived. Two bodies naming the same columns and values in a
// different order describe the same row, and a digest that read the arrival
// order would call them different and re-export the row on every reconcile
// forever. One byte changed in one value still lands on a different digest.
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

// columnsOf turns a decoded body into the column slice EncodeBody takes. The
// order does not matter to the digest -- EncodeBody names the columns through a
// map, and encoding a map sorts its keys -- but it is sorted here so the slice
// is a value rather than a map iteration's accident.
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
