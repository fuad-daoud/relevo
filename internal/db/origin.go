package db

import (
	"encoding/json"
	"fmt"
	"time"
)

// originBackfillKVKey is the kv key a finished origin backfill is recorded
// under. Its presence makes the pass one-time; a failure leaves no key, so the
// next daemon start retries.
const originBackfillKVKey = "origin-backfill.v1"

// OriginBackfillStats records one finished pass, both in the kv row and in the
// daemon's log.
type OriginBackfillStats struct {
	DoneAt         time.Time `json:"done_at"`
	Origin         string    `json:"origin"`
	BindingRecords int64     `json:"binding_records"`
	Bindings       int64     `json:"bindings"`
}

// BackfillOriginOnce stamps this installation's origin on the shared rows that
// predate the origin column, once per database. An existing kv row means an
// earlier pass finished, so ran is false. The update needs no backup: it is
// additive, and reversing it is setting the column back to ”.
//
// Until it runs, the origin IN (?, ”) rule keeps an old row visible to this
// installation, so a database works whether or not the pass has run yet.
func BackfillOriginOnce(d *DB, origin string, now time.Time) (stats OriginBackfillStats, ran bool, err error) {
	if _, ok, kerr := d.KVGet(originBackfillKVKey); kerr != nil {
		return OriginBackfillStats{}, false, fmt.Errorf("origin backfill: kv get %s: %w", originBackfillKVKey, kerr)
	} else if ok {
		return OriginBackfillStats{}, false, nil
	}

	stats = OriginBackfillStats{DoneAt: now, Origin: origin}
	err = d.Tx(func(t *Tx) error {
		records, aerr := t.exec(`UPDATE binding_record SET origin = ? WHERE origin = ''`, origin)
		if aerr != nil {
			return fmt.Errorf("origin backfill: binding_record: %w", mapBusy(aerr))
		}
		if stats.BindingRecords, aerr = records.RowsAffected(); aerr != nil {
			return fmt.Errorf("origin backfill: binding_record rows: %w", aerr)
		}

		bindings, aerr := t.exec(`UPDATE binding SET origin = ? WHERE origin = ''`, origin)
		if aerr != nil {
			return fmt.Errorf("origin backfill: binding: %w", mapBusy(aerr))
		}
		if stats.Bindings, aerr = bindings.RowsAffected(); aerr != nil {
			return fmt.Errorf("origin backfill: binding rows: %w", aerr)
		}

		value, merr := json.Marshal(stats)
		if merr != nil {
			return fmt.Errorf("origin backfill: marshal stats: %w", merr)
		}
		return t.KVPut(originBackfillKVKey, value)
	})
	if err != nil {
		return OriginBackfillStats{}, false, err
	}
	return stats, true, nil
}
