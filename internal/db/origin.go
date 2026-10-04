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
// daemon's log. One field per table the pass stamps, so a log line says which
// tables the rows actually came from rather than only that something ran.
type OriginBackfillStats struct {
	DoneAt         time.Time `json:"done_at"`
	Origin         string    `json:"origin"`
	BindingRecords int64     `json:"binding_records"`
	Bindings       int64     `json:"bindings"`
	Repos          int64     `json:"repos"`
	Masterminds    int64     `json:"masterminds"`
	Chains         int64     `json:"chains"`
}

// backfillTables is every table the pass stamps, in the order it stamps them.
//
// It is spelled out here rather than read from originGateTables so the pass's
// order is its own, and a test pins the two against each other: a table the gate
// counts that this list omits is a table whose rows the gate refuses on with no
// fix the gate could name, and a table here the gate does not count is a table
// stamped for nothing. Every table in it must also report into the stats, or the
// log line would understate the rows the pass moved.
func backfillTables() []string {
	return []string{"binding_record", "binding", "repo", "mastermind", "chains"}
}

// countFor is the stats field that reports how many rows the pass stamped in
// table, or nil when the table has no field -- which is a list that drifted from
// the stats rather than a row count of zero, and the test says so.
func (s *OriginBackfillStats) countFor(table string) *int64 {
	switch table {
	case "binding_record":
		return &s.BindingRecords
	case "binding":
		return &s.Bindings
	case "repo":
		return &s.Repos
	case "mastermind":
		return &s.Masterminds
	case "chains":
		return &s.Chains
	}
	return nil
}

// BackfillOriginOnce stamps this installation's origin on the shared rows that
// predate the origin column, once per database. An existing kv row means an
// earlier pass finished, so ran is false. The update needs no backup: it is
// additive, and reversing it is setting the column back to ”.
//
// It reaches every table the origin gate counts, because the gate is the only
// definition of that set: a table the pass left alone would hold rows the gate
// refuses on forever, with no fix it names. The rows it stamps were all written
// by this machine before the origin column existed, so the installation that owns
// them is the local one -- one row per installation in the mirror tables is
// acceptable until sync merges two installations' history.
//
// The whole set moves in one transaction and the marker is written last inside
// it, so a failure on any table leaves no marker and the next start retries from
// the top rather than resuming on a half-stamped database.
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
		for _, table := range backfillTables() {
			count := stats.countFor(table)
			if count == nil {
				return fmt.Errorf("origin backfill: %s has no stats field: %w", table, ErrInvalid)
			}
			res, aerr := t.exec(`UPDATE `+table+` SET origin = ? WHERE origin = ''`, origin)
			if aerr != nil {
				return fmt.Errorf("origin backfill: %s: %w", table, mapBusy(aerr))
			}
			if *count, aerr = res.RowsAffected(); aerr != nil {
				return fmt.Errorf("origin backfill: %s rows: %w", table, aerr)
			}
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
