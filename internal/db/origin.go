package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// originBackfillKVKey is the kv key a finished origin backfill is recorded
// under. Its presence makes the pass one-time; a failure leaves no key, so the
// next daemon start retries. It is machine-local: the pass stamps this
// machine's installation id onto the shared rows, and a shared marker would
// tell another installation's pass there was nothing here to stamp.
const originBackfillKVKey = "origin-backfill.v1"

// OriginBackfillTwin is one index's share of a pass: how many stale rows it
// resolved by dropping them for the stamped live row, and how many it left for a
// person. It names the table and the counts, never what a row held, so it is
// safe in the kv record and in a log line.
type OriginBackfillTwin struct {
	Table   string `json:"table"`
	Index   string `json:"index"`
	Dropped int64  `json:"dropped"`
	Halted  int64  `json:"halted"`
}

// OriginBackfillStats records one finished pass, both in the kv row and in the
// daemon's log. One field per table the pass stamps, so a log line says which
// tables the rows actually came from rather than only that something ran.
type OriginBackfillStats struct {
	DoneAt         time.Time            `json:"done_at"`
	Origin         string               `json:"origin"`
	BindingRecords int64                `json:"binding_records"`
	Bindings       int64                `json:"bindings"`
	Repos          int64                `json:"repos"`
	Masterminds    int64                `json:"masterminds"`
	Chains         int64                `json:"chains"`
	Twins          []OriginBackfillTwin `json:"twins,omitempty"`
	LeftUnstamped  map[string]int64     `json:"left_unstamped,omitempty"`
}

// Stamped is how many rows the pass moved, across every table it stamps.
func (s OriginBackfillStats) Stamped() int64 {
	return s.BindingRecords + s.Bindings + s.Repos + s.Masterminds + s.Chains
}

// Dropped is how many stale rows the pass resolved in favour of the stamped live
// row, across every table it stamps.
func (s OriginBackfillStats) Dropped() int64 {
	var n int64
	for _, t := range s.Twins {
		n += t.Dropped
	}
	return n
}

// Halted is how many stale rows the pass left for a person, across every table
// it stamps.
func (s OriginBackfillStats) Halted() int64 {
	var n int64
	for _, t := range s.Twins {
		n += t.Halted
	}
	return n
}

// HaltedByTable is the same halt counted per table, so a journal line says which
// table is waiting on a person and how many rows in it, rather than only a total
// that names no table. It reports tables and counts, never a row's contents.
func (s OriginBackfillStats) HaltedByTable() map[string]int64 {
	byTable := make(map[string]int64, len(s.Twins))
	for _, t := range s.Twins {
		if t.Halted > 0 {
			byTable[t.Table] += t.Halted
		}
	}
	if len(byTable) == 0 {
		return nil
	}
	return byTable
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

// noteTwin adds n rows to the (table, index) entry, creating it on first sight,
// so one table that collides on both of its indexes reports both rather than one
// overwritten count.
func (s *OriginBackfillStats) noteTwin(table, index string, dropped, halted int64) {
	if dropped == 0 && halted == 0 {
		return
	}
	for i := range s.Twins {
		if s.Twins[i].Table == table && s.Twins[i].Index == index {
			s.Twins[i].Dropped += dropped
			s.Twins[i].Halted += halted
			return
		}
	}
	s.Twins = append(s.Twins, OriginBackfillTwin{Table: table, Index: index, Dropped: dropped, Halted: halted})
}

// BackfillOriginOnce stamps this installation's origin on the shared rows that
// predate the origin column, once per database. The update needs no backup: it
// is additive, and reversing it is setting the column back to ”.
//
// It reaches every table the origin gate counts, because the gate is the only
// definition of that set: a table the pass left alone would hold rows the gate
// refuses on forever, with no fix it names. The rows it stamps were all written
// by this machine before the origin column existed, so the installation that owns
// them is the local one -- one row per installation in the mirror tables is
// acceptable until sync merges two installations' history.
//
// It moves the set one table at a time, each in its own transaction, and settles
// that table's stale twins before it stamps anything. A row whose stamp would
// collide with a row this installation already stamped is not a reason to give up
// on the pass: on a mirror table the stale row loses to the stamped live row, so
// everything naming it is repointed at the live id and then it is dropped (see
// the twin rule in origin_twin.go and the repoint in origin_repoint.go), and only
// where the rule declines -- a binding_record twin, or a reference that cannot
// move -- does the row stay unstamped and halt for a person. Either way the pass
// continues past that row: the other rows in that table, and every table already
// done, keep the stamps they have; the pass is a resume, not an all-or-nothing.
//
// The marker is written last, to the machine-local file (never the shared
// one), in a write of its own, and only once the gate counts nothing
// anywhere. So a pass that halted leaves no marker and the
// next start resumes: it finds the settled tables holding nothing to stamp, so it
// re-stamps nothing, skips nothing, and stamps only what is left.
//
// The marker is a record, not the decision. It is verified against the gate
// before it is trusted: an older, narrower pass over fewer tables left one
// behind on a database the five-table pass had never seen, and reading it as
// "already done" skipped the whole 5-table pass while the gate still counted
// unstamped rows. So a marker whose gate counts are not empty is stale and is
// ignored, and the pass runs (resume) over what remains. The once-only
// guarantee is unchanged and does not rest on the marker: a second run over a
// settled database finds every table already stamped, so it stamps zero rows.
//
// Until it runs, the origin IN (?, ”) rule keeps an old row visible to this
// installation, so a database works whether or not the pass has run yet.
func BackfillOriginOnce(d *DB, origin string, now time.Time) (stats OriginBackfillStats, ran bool, err error) {
	local := d.LocalOrSelf()
	// A marker is only believed once the gate agrees it is finished. One written
	// by the older narrower pass sits on a database this pass has never stamped,
	// and trusting it skipped the work while the gate still counted rows; so the
	// counts decide, and a marker over a database that still holds unstamped rows
	// is stale and the pass runs (resume) to finish it.
	settled, err := backfillSettled(local, d)
	if err != nil {
		return OriginBackfillStats{}, false, err
	}
	if settled {
		return OriginBackfillStats{}, false, nil
	}

	stats = OriginBackfillStats{DoneAt: now, Origin: origin}
	for _, table := range backfillTables() {
		count := stats.countFor(table)
		if count == nil {
			return stats, false, fmt.Errorf("origin backfill: %s has no stats field: %w", table, ErrInvalid)
		}
		rule, ok := backfillTwinRule[table]
		if !ok {
			return stats, false, fmt.Errorf("origin backfill: %s has no twin rule: %w", table, ErrInvalid)
		}
		settled, serr := settleTwins(d, table, origin, rule, &stats)
		if serr != nil {
			return stats, false, serr
		}
		var res sql.Result
		terr := d.Tx(func(t *Tx) error {
			query := `UPDATE ` + table + ` SET origin = ? WHERE origin = ''`
			args := []any{origin}
			if len(settled) > 0 {
				query += ` AND id NOT IN (?` + strings.Repeat(", ?", len(settled)-1) + `)`
				for _, id := range settled {
					args = append(args, id)
				}
			}
			var aerr error
			res, aerr = t.exec(query, args...)
			return aerr
		})
		if terr != nil {
			return stats, false, fmt.Errorf("origin backfill: %s: %w", table, mapBusy(terr))
		}
		if *count, err = res.RowsAffected(); err != nil {
			return stats, false, fmt.Errorf("origin backfill: %s rows: %w", table, err)
		}
	}

	// The marker is written only once nothing anywhere is left unstamped, so the
	// once-only guarantee stays honest: a pass that halted leaves no key, and the
	// next start picks the work up where this one stopped.
	counts, cerr := CountEmptyOrigins(d)
	if cerr != nil {
		return stats, false, fmt.Errorf("origin backfill: counts: %w", mapBusy(cerr))
	}
	if !counts.Empty() {
		stats.LeftUnstamped = make(map[string]int64, len(counts.Tables))
		for _, t := range counts.Tables {
			if t.Rows > 0 {
				stats.LeftUnstamped[t.Table] = t.Rows
			}
		}
		return stats, false, fmt.Errorf("origin backfill: %d table(s) still hold rows this pass must not stamp, so nothing is recorded: %s; the pass left %d row(s) unstamped and dropped %d stale row(s) for a person to decide",
			len(stats.LeftUnstamped), counts.String(), stats.Halted(), stats.Dropped())
	}

	marker := stats
	if verr := putOriginBackfillStats(local, marker); verr != nil {
		return stats, false, fmt.Errorf("origin backfill: record the pass: %w", mapBusy(verr))
	}
	return stats, true, nil
}

// backfillSettled reports whether a recorded pass is finished, which is the one
// question this pass asks before it does anything: the marker is read from the
// machine-local file it is written to, and it is believed only once the gate
// agrees with it. A marker absent means the pass has to run, since it is the
// pass that writes one; a marker present is believed only over counts that are
// empty. That second half is the fix: a marker left by an older, narrower pass
// over a wider set of tables used to short-circuit a database the gate still
// counted rows in, so the pass skipped forever over work it had never done.
func backfillSettled(local, d *DB) (bool, error) {
	if _, ok, err := local.KVGet(originBackfillKVKey); err != nil {
		return false, fmt.Errorf("origin backfill: kv get %s: %w", originBackfillKVKey, err)
	} else if !ok {
		return false, nil
	}
	counts, err := CountEmptyOrigins(d)
	if err != nil {
		return false, fmt.Errorf("origin backfill: counts: %w", mapBusy(err))
	}
	return counts.Empty(), nil
}

// putOriginBackfillStats writes the marker to the machine-local file it is read
// from.
func putOriginBackfillStats(local *DB, stats OriginBackfillStats) error {
	value, err := json.Marshal(stats)
	if err != nil {
		return fmt.Errorf("origin backfill: marshal stats: %w", err)
	}
	if err := local.KVPut(originBackfillKVKey, value); err != nil {
		return fmt.Errorf("origin backfill: kv put %s: %w", originBackfillKVKey, err)
	}
	return nil
}

// settleTwins is the twin rule applied to one table: every stale row whose stamp
// would collide is repointed at the stamped live row the rule says it loses to and
// then dropped, and the ids of the ones left standing come back so the table's
// stamp skips them. It returns the ids still unstamped, in the order the indexes
// gave them, so a halt names the same rows on every run over the same database.
//
// A row gets a transaction of its own, so a row the pass cannot settle -- a
// reference that will not move, or a record the rule declines to touch -- costs
// that one row and nothing else: not the table's clean rows, not the rows already
// settled, and not the tables already stamped. The pass continues past it, which
// is what keeps a halt from presenting as a whole pass that did nothing.
func settleTwins(d *DB, table, origin string, rule twinRule, stats *OriginBackfillStats) (halted []string, err error) {
	seen := map[string]bool{}
	for _, key := range twinKeysFor(table) {
		pairs, qerr := collidingRowPairs(d.sqlDB, table, origin, key)
		if qerr != nil {
			return nil, fmt.Errorf("origin backfill: %s %s: %w", table, key.name, mapBusy(qerr))
		}
		for _, pair := range pairs {
			// A row can collide on more than one of its table's indexes; it is
			// one row either way, and it is settled once.
			if seen[pair.stale] {
				continue
			}
			seen[pair.stale] = true
			if rule == twinHaltForHuman {
				halted = append(halted, pair.stale)
				stats.noteTwin(table, key.name, 0, 1)
				continue
			}
			haltedRow, derr := settleStaleRow(d, table, pair.stale, pair.live)
			if derr != nil {
				return nil, derr
			}
			if haltedRow {
				halted = append(halted, pair.stale)
				stats.noteTwin(table, key.name, 0, 1)
				continue
			}
			stats.noteTwin(table, key.name, 1, 0)
		}
	}
	return halted, nil
}
