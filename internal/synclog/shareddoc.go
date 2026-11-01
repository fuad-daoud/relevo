package synclog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// DocPrefix marks a table name as a synced document rather than a shared table.
// A doc entry is an ordinary log entry naming a document, so it rides the
// transport, numbering and signature unchanged.
const DocPrefix = "doc:"

// SharedDoc is one document that syncs as whole keys rather than as rows. Items
// is what this machine would send, Apply what it does with one sent key, Name the
// word the entry's table carries. A doc's body is attacker-controlled until its
// signature is checked, so Apply takes one key and value and validates rather
// than merging wholesale.
type SharedDoc interface {
	// Name is the document's name, in the entry's table as `doc:<name>`.
	Name() string
	// Items returns the document's current state as one JSON value per key to sync;
	// an absent key is a delete.
	Items() (map[string]json.RawMessage, error)
	// Apply writes one key's value, or removes the key when value is nil. It must
	// be idempotent.
	Apply(key string, value json.RawMessage) error
}

// sharedDocs is the registry the importer, exporter and reconciler all resolve
// through, so one table name is one answer whichever asks first.
var sharedDocs = map[string]SharedDoc{}

// RegisterSharedDoc makes doc the handler for `doc:<name>`. A taken name is
// refused rather than replaced: two handlers would each apply half of what a
// peer sent, the last registered deciding silently.
func RegisterSharedDoc(doc SharedDoc) error {
	if doc == nil {
		return fmt.Errorf("synclog: shared doc is nil: %w", ErrInvalid)
	}
	name := doc.Name()
	if err := checkDocName(name); err != nil {
		return err
	}
	if _, taken := sharedDocs[name]; taken {
		return fmt.Errorf("synclog: shared doc %s is already registered: %w", name, ErrInvalid)
	}
	sharedDocs[name] = doc
	return nil
}

// checkDocName refuses a document name that could not survive the table-name round
// trip: empty gives bare `doc:`, a separator leaves the word unreadable.
func checkDocName(name string) error {
	if name == "" {
		return fmt.Errorf("synclog: shared doc name is empty: %w", ErrInvalid)
	}
	if strings.ContainsAny(name, ": \t\n/") {
		return fmt.Errorf("synclog: shared doc name %q carries a separator: %w", name, ErrInvalid)
	}
	return nil
}

// SharedDocNames returns every registered document's name, sorted.
func SharedDocNames() []string {
	names := make([]string, 0, len(sharedDocs))
	for name := range sharedDocs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// IsDocTable reports whether a table name names a doc entry. SharedTable keeps
// refusing these, so the importer routes on the prefix before asking whether a
// table is shared.
func IsDocTable(tbl string) bool {
	return strings.HasPrefix(tbl, DocPrefix)
}

// DocName returns the document a table name names, and whether it named one at
// all.
func DocName(tbl string) (string, bool) {
	if !IsDocTable(tbl) {
		return "", false
	}
	return strings.TrimPrefix(tbl, DocPrefix), true
}

// SharedDocFor returns the handler for the document an entry's table names. A
// docless table and an unregistered document are refused apart: the first never
// said what it was about, the second is bytes this machine cannot interpret
// that another machine could write.
func SharedDocFor(tbl string) (SharedDoc, error) {
	name, ok := DocName(tbl)
	if !ok {
		return nil, fmt.Errorf("synclog: table %q does not carry the %q prefix: %w", tbl, DocPrefix, ErrInvalid)
	}
	if name == "" {
		return nil, fmt.Errorf("synclog: table %q names no doc: %w", tbl, ErrInvalid)
	}
	doc, known := sharedDocs[name]
	if !known {
		return nil, fmt.Errorf("synclog: no shared doc named %q (known: %v): %w",
			name, strings.Join(SharedDocNames(), ", "), ErrInvalid)
	}
	return doc, nil
}

// DocEntry is one wire doc entry checked for shape before reaching a document.
// Everything here passed through another machine, so each field is checked: an
// entry reaching Apply has agreeing document, key, operation, stamp and value.
type DocEntry struct {
	// Doc is the handler the entry's table named.
	Doc SharedDoc
	// Name is the document's name.
	Name string
	// Key is the item key the entry writes, without its `doc:` table.
	Key string
	// Stamp is the position the write was made at, deciding whether an arriving
	// entry is newer than what this machine holds.
	Stamp  Stamp
	Origin string
	// Value is the item's new value, or nil for a delete. Nil is the tombstone,
	// never an absent body, so the stamp travels with a delete too.
	Value json.RawMessage
}

// docBody is the body every doc entry carries: write position and value. A delete
// is an upsert with JSON null value, so the stamp reaches the remover and tells
// whether the removal is newer than held.
// The stamp travels as text, not a number: the signed body is re-encoded in transit,
// and two encodings of one numeric stamp would not share a signature.
type docBody struct {
	Stamp string          `json:"stamp"`
	Value json.RawMessage `json:"value"`
}

// docWrite is a doc body after its stamp has been checked.
type docWrite struct {
	Stamp Stamp
	Value json.RawMessage
}

// entryHash is the head digest for one upsert: a row body's columns, or a doc
// body's two fields, hashed apart since the row encoder says nothing about a
// doc's arbitrary JSON value.
func entryHash(e Entry) (string, error) {
	if IsDocTable(e.Table) {
		return docBodyHash(e.Body)
	}
	return BodyHash(e.Body)
}

// docBodyHash digests a doc body by its two fields in one order, so same stamp and
// value in different order is one item. It shape-checks first, so a refusable body
// never reaches the digest.
func docBodyHash(raw json.RawMessage) (string, error) {
	write, err := decodeDocBody(raw)
	if err != nil {
		return "", err
	}
	canonical, err := json.Marshal(docBody{Stamp: write.Stamp.String(), Value: write.Value})
	if err != nil {
		return "", fmt.Errorf("synclog: doc body hash: %w: %w", err, ErrInvalid)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// DocPK is the log's primary key for one doc item: the key as a single-element
// json_array, so a doc entry carries the one-column key every entry does.
func DocPK(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("synclog: doc key is empty: %w", ErrInvalid)
	}
	encoded, err := json.Marshal([]string{key})
	if err != nil {
		return "", fmt.Errorf("synclog: doc key %s: %w: %w", key, err, ErrInvalid)
	}
	return string(encoded), nil
}

// ParseDocEntry reads a wire doc entry and refuses every unreadable shape: a
// docless table, an unregistered document, a non-single-string key, a non-upsert
// op, a body that is not exactly stamp and value, and a non-canonical stamp.
// Each refusal is a separate check answering a different attack: a misshapen table
// or key would be looked up and misread, an off-shape body carries unchecked fields
// including the ordering stamp, and a misshapen stamp would order wrongly.
func ParseDocEntry(e Entry) (DocEntry, error) {
	return parseDocEntry(SharedDocFor, e)
}

// parseDocEntry is ParseDocEntry against a chosen document lookup, so a reader
// bound to its own documents refuses what they do not name.
func parseDocEntry(lookup func(string) (SharedDoc, error), e Entry) (DocEntry, error) {
	doc, err := lookup(e.Table)
	if err != nil {
		return DocEntry{}, err
	}
	key, err := docKey(e.PK)
	if err != nil {
		return DocEntry{}, fmt.Errorf("synclog: doc %s %s: %w", e.Table, e.PK, err)
	}
	if e.Op != OpUpsert {
		return DocEntry{}, fmt.Errorf("synclog: doc %s %s has op %q; a doc delete is an upsert whose value is null: %w",
			e.Table, e.PK, e.Op, ErrInvalid)
	}
	body, err := decodeDocBody(e.Body)
	if err != nil {
		return DocEntry{}, fmt.Errorf("synclog: doc %s %s: %w", e.Table, e.PK, err)
	}
	return DocEntry{
		Doc:    doc,
		Name:   doc.Name(),
		Key:    key,
		Stamp:  body.Stamp,
		Origin: e.Origin,
		Value:  body.Value,
	}, nil
}

// docKey reads the one-element key array a doc entry is addressed by. Any other
// shape names no item and would apply a write with no address.
func docKey(pk string) (string, error) {
	var values []string
	if err := json.Unmarshal([]byte(pk), &values); err != nil {
		return "", fmt.Errorf("pk is not a JSON array of strings: %w: %w", ErrInvalid, err)
	}
	if len(values) != 1 || values[0] == "" {
		return "", fmt.Errorf("pk names %d keys, want exactly one: %w", len(values), ErrInvalid)
	}
	return values[0], nil
}

// decodeDocBody reads the stamp and value out of a doc entry's body, decoded as a
// map first so an unknown field refuses: a peer adding a field this build cannot
// read has an ordering rule this build cannot apply.
func decodeDocBody(raw json.RawMessage) (docWrite, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return docWrite{}, fmt.Errorf("body is not a JSON object: %w: %w", ErrInvalid, err)
	}
	allowed := map[string]bool{"stamp": true, "value": true}
	for name := range fields {
		if !allowed[name] {
			return docWrite{}, fmt.Errorf("body carries unknown field %q: %w", name, ErrInvalid)
		}
	}
	stampText, hasStamp := fields["stamp"]
	value, hasValue := fields["value"]
	if !hasStamp || !hasValue {
		return docWrite{}, fmt.Errorf("body must carry a stamp and a value: %w", ErrInvalid)
	}
	var text string
	if err := json.Unmarshal(stampText, &text); err != nil {
		return docWrite{}, fmt.Errorf("body stamp is not text: %w: %w", ErrInvalid, err)
	}
	stamp, err := ParseStamp(text)
	if err != nil {
		return docWrite{}, err
	}
	return docWrite{Stamp: stamp, Value: value}, nil
}
