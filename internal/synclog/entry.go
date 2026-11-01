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

// ErrInvalid reports an entry, key or body of the wrong shape. It is a refusal,
// not a failure: the value came from an uncontrolled body, and dropping it is correct.
var ErrInvalid = errors.New("invalid sync log entry")

// ErrBlobMissing reports a body the store does not hold. It is a sentinel because
// a missing body and an unreachable bucket need opposite handling: the first
// latches the origin, the second may pass on retry.
var ErrBlobMissing = errors.New("synclog: the body is not in the blob store")

// Op is what an entry asks the importer to do with its row.
type Op string

const (
	OpUpsert Op = "upsert"
	OpDelete Op = "delete"
)

// Ref points at one entry in an origin's log.
type Ref struct {
	Origin string
	Seq    int
}

// Entry is one append to the log: what one installation recorded about one
// shared row, at one position in its own sequence. Seq and Batch belong to the
// transport, not the writer: self-numbering would reuse numbers after a backup
// restore, so the transport assigns Seq past the held high and Batch as the
// append's first Seq, marking atomic batch ends for readers.
type Entry struct {
	Origin string
	Seq    int
	// Batch is the Seq of the append's first entry: shared Batch arrived and applies
	// together.
	Batch int
	Table string
	// PK is the row's primary key as the json_array text the outbox records.
	PK string
	Op Op
	// SchemaVersion is the writer's schema version, telling a reader whether it
	// understands the entry.
	SchemaVersion int
	// Body is the row as JSON, column name to value, empty for a delete naming
	// only its key.
	Body json.RawMessage
	// At is when the entry was written.
	At time.Time
	// MAC is the HMAC-SHA256 over the entry's canonical encoding, empty pre-key. It
	// covers every field above except Seq and Batch, assigned after signing.
	MAC []byte
	// After points at an entry in another origin's log that this entry depends on.
	After *Ref
}

// WithAfter returns a copy of e with After set.
func (e Entry) WithAfter(after *Ref) Entry {
	if after == nil {
		e.After = nil
		return e
	}
	r := *after
	e.After = &r
	return e
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

// NewDelete builds the entry that removes a row. It carries no body: the importer
// deletes by key, so a body could only disagree with it.
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

// NewDocUpsert builds the entry that writes one doc key's value: an ordinary
// upsert on `doc:<name>` carrying stamp and value, told by table name where read.
func NewDocUpsert(origin string, doc SharedDoc, key string, schemaVersion int, stamp Stamp, value json.RawMessage, at time.Time) (Entry, error) {
	return newDocEntry(origin, doc, key, schemaVersion, stamp, value, at)
}

// NewDocDelete builds the entry that removes one doc key: an upsert with JSON null
// value, since a bodyless delete could not carry the stamp telling whether the
// removal is newer than held, and a delete carrying a body is refused.
func NewDocDelete(origin string, doc SharedDoc, key string, schemaVersion int, stamp Stamp, at time.Time) (Entry, error) {
	return newDocEntry(origin, doc, key, schemaVersion, stamp, nil, at)
}

// newDocEntry builds the one doc entry shape, nil value as removal.
func newDocEntry(origin string, doc SharedDoc, key string, schemaVersion int, stamp Stamp, value json.RawMessage, at time.Time) (Entry, error) {
	if doc == nil {
		return Entry{}, fmt.Errorf("synclog: doc entry names no doc: %w", ErrInvalid)
	}
	name := doc.Name()
	if err := checkDocName(name); err != nil {
		return Entry{}, err
	}
	pk, err := DocPK(key)
	if err != nil {
		return Entry{}, err
	}
	body, err := json.Marshal(docBody{Stamp: stamp.String(), Value: value})
	if err != nil {
		return Entry{}, fmt.Errorf("synclog: doc entry %s %s: %w: %w", name, key, err, ErrInvalid)
	}
	e := Entry{
		Origin:        origin,
		Table:         DocPrefix + name,
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

// checkShape refuses a shapeless entry at build time: unknown op, bodied delete, or
// bodiless upsert. A misshapen transport entry is already serialized onto a pipe.
func checkShape(e Entry) error {
	if e.After != nil && (e.After.Origin == "" || e.After.Seq <= 0) {
		return fmt.Errorf("synclog: entry %s %s has invalid after ref: %w", e.Table, e.PK, ErrInvalid)
	}
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

// SharedTable returns the shared table named tbl and whether one carries that name.
// Only a db-classified name may reach SQL.
func SharedTable(tbl string) (db.SharedTable, bool) {
	for _, s := range db.SharedTables {
		if s.Name == tbl {
			return s, true
		}
	}
	return db.SharedTable{}, false
}

// SharedTablesInOrder returns the shared tables' names in db order: parents before
// children, so a walk meets a parent before its needer.
func SharedTablesInOrder() []string {
	names := make([]string, 0, len(db.SharedTables))
	for _, s := range db.SharedTables {
		names = append(names, s.Name)
	}
	return names
}

// KeyOf returns the columns tbl keys its rows by, in key order, and the values
// pk names. The pk JSON is parsed to bind values, so no carried body becomes SQL.
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

// decodeKey parses the key text into its values. A non-array key is refused, not
// guessed: it would match no row or the wrong one.
func decodeKey(pk string) ([]any, error) {
	dec := json.NewDecoder(strings.NewReader(pk))
	// Numbers are read as written, not float64, so a wide key binds exactly as its
	// column's integer.
	dec.UseNumber()
	var raw []any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("synclog: key %s is not a JSON array: %w", pk, ErrInvalid)
	}
	// The decoder stops at the array end and would read past it, so trailing text
	// would parse as the prefix. A key is its whole text or not a key.
	if err := expectEOF(dec); err != nil {
		return nil, fmt.Errorf("synclog: key %s has text after its values: %w", pk, ErrInvalid)
	}
	return raw, nil
}

// expectEOF reports whether a decoder consumed all input: a second value or
// trailing byte is a body beyond its claimed shape.
func expectEOF(dec *json.Decoder) error {
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	return nil
}

// keyValue maps one decoded key value onto the driver's bind type: an integral
// JSON number binds as its column's integer, since a real compares as a real
// against INTEGER.
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
