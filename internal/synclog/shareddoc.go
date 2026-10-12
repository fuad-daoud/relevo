package synclog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// DocPrefix marks a table name as naming a synced document rather than a shared
// table. A doc entry is an ordinary log entry that happens to name a document,
// so it rides the transport, the sequence numbering and the signature a row
// already had, and nothing about the log's shape changes to carry one.
const DocPrefix = "doc:"

// SharedDoc is one document that syncs as whole keys rather than as rows.
//
// The three methods are the whole contract, and each answers for one machine:
// Items is what this machine would send, Apply is what it does with one key it
// was sent, and Name is the word the entry's table carries. A doc's body is
// attacker-controlled until its signature has been checked, which is why Apply
// takes a single key and a single value and validates the result rather than
// merging a body wholesale.
type SharedDoc interface {
	// Name is the document's name, which appears in the entry's table as
	// `doc:<name>`.
	Name() string
	// Items returns the document's current state as one JSON value per key
	// that is to sync. A key absent from the map is a key the document no
	// longer holds, and is exported as a delete.
	Items() (map[string]json.RawMessage, error)
	// Apply writes one key's value, or removes the key when value is nil. It
	// must be idempotent: an entry delivered twice leaves the same document.
	Apply(key string, value json.RawMessage) error
}

// sharedDocs is the registry of the documents a reader and a writer can name.
// It is a package-level table rather than a parameter because the doc a table
// name resolves to has to be the same answer for the importer, the exporter
// and the reconciler, whichever of them is the first to be asked.
var sharedDocs = map[string]SharedDoc{}

// RegisterSharedDoc makes doc the handler for `doc:<name>`. A name already
// registered is refused rather than replaced: two handlers for one document
// would each apply half of what a peer sent, and whichever was registered last
// would decide silently which half.
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

// checkDocName refuses a document name that could not survive the round trip
// through a table name and back. An empty name would give `doc:` on its own,
// and a name carrying a separator would leave a reader unable to tell which
// word is the document's.
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

// IsDocTable reports whether a table name names a doc entry. SharedTable has to
// keep refusing these, so the importer routes on the prefix before it asks
// whether a table is shared.
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

// SharedDocFor returns the handler registered for the document an entry's table
// names.
//
// A doc table naming no document and one naming a document this build has no
// reader for are refused apart, because they are apart: the first is an entry
// that never said what it was about, and the second is bytes this machine
// cannot interpret. Applying either would be acting on a name it has no reader
// for, and the second is the one another machine could write.
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

// DocEntry is one doc entry read off the wire and checked for shape, before any
// of it reaches a document. Everything here has been through another machine,
// so each field is checked rather than trusted: an entry that reaches Apply is
// one whose document, key, operation, stamp and value shape all agreed.
type DocEntry struct {
	// Doc is the handler the entry's table named.
	Doc SharedDoc
	// Name is the document's name.
	Name string
	// Key is the item key the entry writes, without its `doc:` table.
	Key string
	// Stamp is the position the write was made at, which is what decides
	// whether an arriving entry is newer than what this machine holds.
	Stamp Stamp
	// Origin is the installation that made the write.
	Origin string
	// Value is the item's new value, or nil for a delete. A nil value is the
	// tombstone, never an absent body: the body always carries one or the
	// other, so the stamp travels with a delete too.
	Value json.RawMessage
}

// docBody is the body every doc entry carries: the position the write was made
// at, and the value it wrote. A delete is an upsert whose value is JSON null,
// because the stamp has to reach the reader that removes the key and a delete
// body carrying no stamp could not say whether the removal is newer than what
// the reader holds.
//
// The stamp travels as text and not as a number because the body is signed as
// written and re-encoded on the way through: a number would have to be read
// and written back in one canonical form or two encodings of the same stamp
// would not share a signature.
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
// body's two fields. The two are hashed apart because a doc body carries an
// arbitrary JSON value where a row body carries storable columns, so the row
// encoder has nothing to say about one.
func entryHash(e Entry) (string, error) {
	if IsDocTable(e.Table) {
		return docBodyHash(e.Body)
	}
	return BodyHash(e.Body)
}

// docBodyHash digests a doc body by its two fields re-encoded in one order, so
// two entries naming the same stamp and the same value in a different order are
// one item rather than two. It reads the body through the shape check first, so
// a body this machine would refuse never reaches the digest.
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

// DocPK is the log's primary key for one doc item: the item key as a
// single-element json_array, so a doc entry carries the one-column key every
// other entry does and a reader parses one shape for both.
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

// ParseDocEntry reads a doc entry off the wire and refuses every shape this
// machine has no reader for: a table that names no document, a document that is
// not registered, a primary key that is not one string, an operation that is
// not an upsert, a body that is not an object carrying exactly a stamp and a
// value, and a stamp that is not a canonical decimal number.
//
// Each refusal is a separate check because each answers a different attack. A
// table or key of the wrong shape names something this machine would look up
// and misread; a body that is not the agreed object carries fields no check
// covers, including the stamp the reader would order by; and a stamp of the
// wrong shape would order wrongly against everything already applied.
func ParseDocEntry(e Entry) (DocEntry, error) {
	doc, err := SharedDocFor(e.Table)
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

// docKey reads the one-element key array a doc entry is addressed by. A key of
// any other shape names no item: an array of two names two items, and a bare
// string names none, so both would apply a write this machine cannot point at.
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

// decodeDocBody reads the stamp and value out of a doc entry's body.
//
// The body is decoded into a map first so an unknown field is a refusal rather
// than a field this build ignores: a peer that added a field this build has no
// reader for is a peer whose ordering rule this build cannot apply, and
// dropping the field quietly would apply the entry on the wrong terms.
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
