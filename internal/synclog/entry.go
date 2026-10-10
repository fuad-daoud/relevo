// Package synclog is the sync exchange: the entries a machine appends for the
// rows it owns, the bodies they carry, and the transport they travel over.
package synclog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// ErrInvalid reports an entry, a key or a body that does not have the shape the
// log is defined in. It is a refusal rather than a failure: the offending value
// came from a body this machine does not control, and dropping it is correct.
var ErrInvalid = errors.New("invalid sync log entry")

// ErrBlobMissing reports a body the store does not hold. It is a sentinel
// because a missing body and a bucket that could not be reached call for
// opposite handling: the first latches the origin and names the round, since the
// object an entry points at is not there and a retry will not make it appear,
// and the second is a fault a later attempt can still get past.
var ErrBlobMissing = errors.New("synclog: the body is not in the blob store")

// Op is what an importer is asked to do with the row an entry names.
type Op string

const (
	// OpUpsert writes the row's body onto its primary key.
	OpUpsert Op = "upsert"
	// OpDelete removes the row by its primary key.
	OpDelete Op = "delete"
)

// Entry is one append to the log: what one installation recorded about one
// shared row, at one position in that installation's own sequence.
//
// Seq and Batch belong to the transport, not to the writer: a machine that
// named its own sequence numbers would reuse them after a restore from a
// backup, so the transport assigns Seq as one past the highest it holds for the
// origin and Batch as the Seq of the first entry of the append that wrote this
// one. A reader can then tell where one atomic batch ends.
type Entry struct {
	// Origin is the installation that owns the row the entry names.
	Origin string
	// Seq is the entry's position in that origin's log.
	Seq int
	// Batch is the Seq of the first entry of the append that wrote this entry,
	// so entries sharing a Batch arrived together and are applied together.
	Batch int
	// Table is the shared table the row is in.
	Table string
	// PK is the row's primary key as the json_array text the outbox records.
	PK string
	// Op is the write the entry asks for.
	Op Op
	// SchemaVersion is the writer's schema version, so a reader can tell an
	// entry it understands from one written by a newer binary.
	SchemaVersion int
	// Body is the row as JSON, column name to value. It is empty for a delete,
	// which names a row by its key and carries nothing else.
	Body json.RawMessage
	// At is when the entry was written.
	At time.Time
}

// NewUpsert builds the entry that writes a row's state.
func NewUpsert(origin, table, pk string, schemaVersion int, body json.RawMessage, at time.Time) (Entry, error) {
	e := Entry{
		Origin:        origin,
		Table:         table,
		PK:            pk,
		Op:            OpUpsert,
		SchemaVersion: schemaVersion,
		Body:          body,
		At:            at,
	}
	if err := checkShape(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// NewDelete builds the entry that removes a row. It carries no body: the
// importer deletes by key, so a body would be a second answer to a question the
// key already answers, and one that could disagree with it.
func NewDelete(origin, table, pk string, schemaVersion int, at time.Time) (Entry, error) {
	e := Entry{
		Origin:        origin,
		Table:         table,
		PK:            pk,
		Op:            OpDelete,
		SchemaVersion: schemaVersion,
		At:            at,
	}
	if err := checkShape(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// checkShape refuses an entry the log has no room for: an unknown op, a delete
// carrying a body, or an upsert carrying none. Both directions are refused at
// the point the entry is built, because an entry that reaches a transport with
// the wrong shape has already been serialized onto a pipe.
func checkShape(e Entry) error {
	switch e.Op {
	case OpUpsert:
		if len(e.Body) == 0 {
			return fmt.Errorf("synclog: upsert %s %s has no body: %w", e.Table, e.PK, ErrInvalid)
		}
	case OpDelete:
		if len(e.Body) != 0 {
			return fmt.Errorf("synclog: delete %s %s carries a body: %w", e.Table, e.PK, ErrInvalid)
		}
	default:
		return fmt.Errorf("synclog: entry %s %s has op %q: %w", e.Table, e.PK, e.Op, ErrInvalid)
	}
	return nil
}

// SharedTable returns the shared table named tbl, and whether any shared table
// carries that name. A table name reaches SQL, and the only names allowed to is
// the one the db package has already classified as shared.
func SharedTable(tbl string) (db.SharedTable, bool) {
	for _, s := range db.SharedTables {
		if s.Name == tbl {
			return s, true
		}
	}
	return db.SharedTable{}, false
}

// SharedTablesInOrder returns the shared tables' names in the order the db
// package lists them: parents before children, so a walk in this order meets a
// parent before anything that needs it.
func SharedTablesInOrder() []string {
	names := make([]string, 0, len(db.SharedTables))
	for _, s := range db.SharedTables {
		names = append(names, s.Name)
	}
	return names
}

// KeyOf returns the columns tbl keys its rows by, in key order, and the values
// pk names for them. The pk text is JSON and is parsed, never read: a value comes
// back as a value to bind, so nothing a body carries can become SQL.
func KeyOf(tbl, pk string) ([]string, []any, error) {
	shared, ok := SharedTable(tbl)
	if !ok {
		return nil, nil, fmt.Errorf("synclog: key: %q is not a shared table: %w", tbl, ErrInvalid)
	}
	raw, err := decodeKey(pk)
	if err != nil {
		return nil, nil, err
	}
	if len(raw) != len(shared.PrimaryKey) {
		return nil, nil, fmt.Errorf("synclog: key: %s %s names %d values, want %d: %w",
			tbl, pk, len(raw), len(shared.PrimaryKey), ErrInvalid)
	}
	values := make([]any, len(raw))
	for i, v := range raw {
		values[i] = keyValue(v)
	}
	return shared.PrimaryKey, values, nil
}

// decodeKey parses the key text into its values. A key that is not a JSON array
// is refused rather than guessed at: a key of the wrong shape would either match
// no row or match a row the entry never named.
func decodeKey(pk string) ([]any, error) {
	dec := json.NewDecoder(strings.NewReader(pk))
	// Numbers are read as written rather than through float64, so a key wider
	// than a float holds exactly and binds as the integer its column is.
	dec.UseNumber()
	var raw []any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("synclog: key %s is not a JSON array: %w", pk, ErrInvalid)
	}
	// The decoder stops at the end of the array and would otherwise read past it,
	// so a key with anything appended parses as its own prefix. A key is the
	// whole of its text or it is not a key.
	if err := expectEOF(dec); err != nil {
		return nil, fmt.Errorf("synclog: key %s has text after its values: %w", pk, ErrInvalid)
	}
	return raw, nil
}

// expectEOF reports whether a decoder has consumed all of its input. A second
// value, or a trailing byte, is a body carrying more than the shape it claims.
func expectEOF(dec *json.Decoder) error {
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	return nil
}

// keyValue maps one decoded key value onto the type the driver binds. A JSON
// number arrives as written; an integral one is bound as the integer its column
// holds, because binding a real for an INTEGER column compares as a real.
func keyValue(v any) any {
	n, ok := v.(json.Number)
	if !ok {
		return v
	}
	if i, err := n.Int64(); err == nil {
		return i
	}
	f, err := n.Float64()
	if err != nil {
		return n.String()
	}
	return f
}
