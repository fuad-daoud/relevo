package availability

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The one-time move of the two history documents into the tables that replaced
// them.
//
// Until schema 29 both histories were one rewritten kv document per machine, in
// the machine-local file. They are now one row per observation in the shared
// file, so a peer can hold the window and an append no longer rewrites it. This
// is the pass that takes what the documents hold and writes it as rows, once,
// behind a local marker, and then deletes the documents.
//
// Migrated rows are ordinary rows: own origin, a fresh ULID each, the same at
// the document carried, and therefore exported like any other history. Nothing
// about a migrated row marks it as migrated.

// availabilityKey is the retired kv row the availability document lived in.
// MigrateHistoryDocuments decodes it once and deletes it; nothing reads or
// writes it after that.
const availabilityKey = "availability"

// historyRowsMarker is the local kv row that records the documents have moved.
// It is local, like every other run-once marker, because two machines sharing
// it would each believe the other had moved its own documents -- and a machine's
// documents are its own, so there is nothing for a peer to move.
//
// The name follows the run-once markers already in the file
// (mirror-dedupe.v1, zstd-compress.v1): the migration it belongs to, then a
// version.
const historyRowsMarker = "sync.history_rows.v1"

// MigratedHistory is what one migration pass moved, and what a second pass
// moved: nothing.
type MigratedHistory struct {
	Events  int
	Samples int
}

// MigrateHistoryDocuments moves the availability and latency kv documents into
// availability_event and latency_sample, then deletes them.
//
// local is the machine-local file the documents live in; shared is the shared
// file the rows go into and the one the outbox belongs to. origin is this
// machine's installation id, stamped on every migrated row, so a migrated row
// exports and is refused on exactly the rule a row written today is.
//
// It runs where the runtime opens both files, before any reader or writer of
// the histories, so the daemon, every verb and `db sync enable`/join all see
// the rows rather than the documents. Running it twice is a no-op: the marker
// says the move happened, and a document already deleted is not read again.
//
// The order is rows, then the marker, then the deletes. Rows before the marker
// means the marker never claims a move that did not happen -- a pass that failed
// halfway leaves the documents in place and is retried. The window between the
// row write and the document delete is the one a crash can lose into, and it
// costs a duplicate window rather than a lost one: the retried pass reads the
// document again and writes it again.
func MigrateHistoryDocuments(local db.KV, shared *db.DB, origin string) (MigratedHistory, error) {
	if local == nil || shared == nil {
		return MigratedHistory{}, nil
	}
	if _, done, err := local.KVGet(historyRowsMarker); err != nil {
		return MigratedHistory{}, fmt.Errorf("read history migration marker: %w", err)
	} else if done {
		return MigratedHistory{}, nil
	}

	var moved MigratedHistory
	events, had, err := readHistoryDoc(local)
	if err != nil {
		return MigratedHistory{}, err
	}
	if had {
		moved.Events, err = insertEventRows(shared, origin, events)
		if err != nil {
			return MigratedHistory{}, err
		}
	}
	samples, had, err := readLatencyDoc(local)
	if err != nil {
		return MigratedHistory{}, err
	}
	if had {
		moved.Samples, err = insertSampleRows(shared, origin, samples)
		if err != nil {
			return MigratedHistory{}, err
		}
	}

	if err := local.KVPut(historyRowsMarker, []byte("1")); err != nil {
		return MigratedHistory{}, fmt.Errorf("write history migration marker: %w", err)
	}
	for _, key := range []string{availabilityKey, latencyKey} {
		if err := local.KVDelete(key); err != nil {
			return MigratedHistory{}, fmt.Errorf("delete %s document: %w", key, err)
		}
	}
	return moved, nil
}

// readHistoryDoc decodes the retired availability document. An absent document
// is not an error: a machine that recorded nothing has nothing to move, and one
// already migrated has none left.
func readHistoryDoc(local db.KV) (History, bool, error) {
	data, ok, err := local.KVGet(availabilityKey)
	if err != nil || !ok {
		return History{}, false, err
	}
	var h History
	if err := json.Unmarshal(data, &h); err != nil {
		return History{}, false, fmt.Errorf("decode availability document: %w", err)
	}
	return h, true, nil
}

// readLatencyDoc decodes the retired latency document, under the same rule
// readHistoryDoc decodes the availability one.
func readLatencyDoc(local db.KV) (LatencyHistory, bool, error) {
	data, ok, err := local.KVGet(latencyKey)
	if err != nil || !ok {
		return LatencyHistory{}, false, err
	}
	var h LatencyHistory
	if err := json.Unmarshal(data, &h); err != nil {
		return LatencyHistory{}, false, fmt.Errorf("decode latency document: %w", err)
	}
	return h, true, nil
}

// insertEventRows writes every event of one document as its own row, oldest
// first. The order is the document's own and the ids are derived, so the rows
// read back exactly as they read before: same values, same sequence, same
// identities on every pass.
func insertEventRows(shared *db.DB, origin string, h History) (int, error) {
	events := append([]Event(nil), h.Events...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].At.Before(events[j].At) })
	for _, ev := range events {
		id, err := migratedRowID(origin, db.AvailabilityEventTable, db.UnixMS(ev.At), ev)
		if err != nil {
			return 0, err
		}
		row := db.AvailabilityEvent{
			ID:       id,
			AtMS:     db.UnixMS(ev.At),
			Kind:     string(ev.Kind),
			Provider: ev.Provider,
			Account:  ev.Account,
			Token:    ev.Token,
			Source:   ev.Source,
			Binding:  ev.Binding,
			Note:     ev.Note,
		}
		if !ev.Since.IsZero() {
			since := db.UnixMS(ev.Since)
			row.SinceMS = &since
		}
		if err := shared.InsertAvailabilityEvent(origin, row); err != nil {
			return 0, err
		}
	}
	return len(events), nil
}

// insertSampleRows writes every sample of one document as its own row, oldest
// first, under the same rule insertEventRows writes events.
func insertSampleRows(shared *db.DB, origin string, h LatencyHistory) (int, error) {
	samples := append([]Sample(nil), h.Samples...)
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].At.Before(samples[j].At) })
	for _, s := range samples {
		id, err := migratedRowID(origin, db.LatencySampleTable, db.UnixMS(s.At), s)
		if err != nil {
			return 0, err
		}
		if err := shared.InsertLatencySample(origin, db.LatencySample{
			ID:      id,
			AtMS:    db.UnixMS(s.At),
			Token:   s.Token,
			Host:    s.Host,
			TTFTMS:  s.TTFTMS,
			TotalMS: s.TotalMS,
			Err:     s.Err,
		}); err != nil {
			return 0, err
		}
	}
	return len(samples), nil
}

// crockford is the Crockford base32 alphabet ULIDs are written in: no I, L, O
// or U. db holds the same alphabet for NewID; it is spelled out again here
// because encodeULID is not exported and this package must not reach into db
// for one identifier.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// migratedRowID is the identity one migrated row is written under: a ULID whose
// 48-bit time part is the row's own at_ms, and whose remaining 80 bits are the
// head of the sha256 over the origin, the table name, and the entry's canonical
// json -- the entry re-encoded from the struct the document decoded into, not
// the bytes the document happened to be written in.
//
// Deriving it rather than minting it is what makes the move repeatable. A pass
// that crashes between the row writes and the marker is retried, and the retry
// has to write the rows that are already there rather than a second copy of the
// window under new identities; with the insert an INSERT OR IGNORE, the retry
// leaves the tables exactly as the crashed pass left them. The same document
// migrated twice yields the same ids.
//
// The byte layout is db.NewID's -- six bytes of big-endian milliseconds, then
// the ten bytes the hash supplies in place of the random ones.
func migratedRowID(origin, table string, atMS int64, entry any) (string, error) {
	canonical, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("canonical json for the %s row at %d: %w", table, atMS, err)
	}
	sum := sha256.Sum256([]byte(origin + table + string(canonical)))

	var id [16]byte
	ms := uint64(atMS)
	id[0] = byte(ms >> 40)
	id[1] = byte(ms >> 32)
	id[2] = byte(ms >> 24)
	id[3] = byte(ms >> 16)
	id[4] = byte(ms >> 8)
	id[5] = byte(ms)
	copy(id[6:], sum[:10])

	return encodeMigratedULID(id), nil
}

// encodeMigratedULID writes the 16 bytes as the 26 characters a ULID is, the
// same layout db.NewID's encoder uses.
func encodeMigratedULID(id [16]byte) string {
	var out [26]byte

	out[0] = crockford[(id[0]&224)>>5]
	out[1] = crockford[id[0]&31]
	out[2] = crockford[(id[1]&248)>>3]
	out[3] = crockford[((id[1]&7)<<2)|((id[2]&192)>>6)]
	out[4] = crockford[(id[2]&62)>>1]
	out[5] = crockford[((id[2]&1)<<4)|((id[3]&240)>>4)]
	out[6] = crockford[((id[3]&15)<<1)|((id[4]&128)>>7)]
	out[7] = crockford[(id[4]&124)>>2]
	out[8] = crockford[((id[4]&3)<<3)|((id[5]&224)>>5)]
	out[9] = crockford[id[5]&31]
	out[10] = crockford[(id[6]&248)>>3]
	out[11] = crockford[((id[6]&7)<<2)|((id[7]&192)>>6)]
	out[12] = crockford[(id[7]&62)>>1]
	out[13] = crockford[((id[7]&1)<<4)|((id[8]&240)>>4)]
	out[14] = crockford[((id[8]&15)<<1)|((id[9]&128)>>7)]
	out[15] = crockford[(id[9]&124)>>2]
	out[16] = crockford[((id[9]&3)<<3)|((id[10]&224)>>5)]
	out[17] = crockford[id[10]&31]
	out[18] = crockford[(id[11]&248)>>3]
	out[19] = crockford[((id[11]&7)<<2)|((id[12]&192)>>6)]
	out[20] = crockford[(id[12]&62)>>1]
	out[21] = crockford[((id[12]&1)<<4)|((id[13]&240)>>4)]
	out[22] = crockford[((id[13]&15)<<1)|((id[14]&128)>>7)]
	out[23] = crockford[(id[14]&124)>>2]
	out[24] = crockford[((id[14]&3)<<3)|((id[15]&224)>>5)]
	out[25] = crockford[id[15]&31]

	return string(out[:])
}
