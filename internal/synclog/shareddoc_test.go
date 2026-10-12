package synclog

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeDoc is a shared doc that records what it was asked to apply, so a test
// can show that nothing reached it rather than only that the parse refused.
type fakeDoc struct {
	name    string
	items   map[string]json.RawMessage
	applied []string
	values  map[string]json.RawMessage
	err     error
}

func newFakeDoc(name string) *fakeDoc {
	return &fakeDoc{name: name, items: map[string]json.RawMessage{}, values: map[string]json.RawMessage{}}
}

func (f *fakeDoc) Name() string { return f.name }

func (f *fakeDoc) Items() (map[string]json.RawMessage, error) { return f.items, f.err }

func (f *fakeDoc) Apply(key string, value json.RawMessage) error {
	f.applied = append(f.applied, key)
	f.values[key] = value
	return f.err
}

// registerDoc registers a fake doc for the duration of one test. Registration
// is undone afterwards so two tests naming the same document cannot leak a
// handler into each other.
func registerDoc(t *testing.T, doc *fakeDoc) {
	t.Helper()
	if err := RegisterSharedDoc(doc); err != nil {
		t.Fatalf("RegisterSharedDoc(%s): %v", doc.name, err)
	}
	t.Cleanup(func() { delete(sharedDocs, doc.name) })
}

// docEntryWith builds a well-formed doc entry and then overwrites one field,
// so a refusal test changes only the thing it is about.
func docEntryWith(t *testing.T, doc *fakeDoc, key string, stamp Stamp) Entry {
	t.Helper()
	entry, err := NewDocUpsert("origin-a", doc, key, 1, stamp, json.RawMessage(`{"listen":8080}`),
		time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewDocUpsert: %v", err)
	}
	return entry
}

// TestUnknownSharedDocIsRefused pins that a doc table naming a document this
// build has no reader for is refused before anything is parsed out of it. The
// name arrived from another machine, so it is the one field a peer controls and
// the one that has to be checked first: a reader that applied an entry against
// a document it could not name would be writing into whatever handler it had.
func TestUnknownSharedDocIsRefused(t *testing.T) {
	doc := newFakeDoc("config")
	registerDoc(t, doc)

	entry, err := NewDocUpsert("origin-a", doc, "policy", 1, 7,
		json.RawMessage(`{}`), time.Now())
	if err != nil {
		t.Fatalf("NewDocUpsert: %v", err)
	}

	parsed, err := ParseDocEntry(entry)
	if err != nil {
		t.Fatalf("ParseDocEntry on a registered doc: %v", err)
	}
	if parsed.Name != "config" {
		t.Fatalf("parsed name = %q, want config", parsed.Name)
	}

	// The handler goes away, which is what a build without this doc looks like
	// to an entry that names it.
	delete(sharedDocs, "config")

	_, err = ParseDocEntry(entry)
	if err == nil {
		t.Fatal("ParseDocEntry accepted a doc no handler is registered for, want a refusal")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("refusal = %v, want it to wrap ErrInvalid", err)
	}
}

// TestDocTableThatNamesNoDocIsRefused pins that the prefix on its own is not a
// document. `doc:` says an entry was about a doc without saying which, and
// resolving it by looking up the empty name would report an unknown document
// for an entry that is malformed rather than one this build lacks.
func TestDocTableThatNamesNoDocIsRefused(t *testing.T) {
	entry := docEntryWith(t, newFakeDoc("config"), "policy", 1)
	entry.Table = DocPrefix

	_, err := ParseDocEntry(entry)
	if err == nil {
		t.Fatal("ParseDocEntry accepted a doc table naming no document, want a refusal")
	}
	if !strings.Contains(err.Error(), "names no doc") {
		t.Errorf("refusal = %v, want it to name the missing document", err)
	}
}

// TestDocKeyThatIsNotOneStringIsRefused pins the primary key's shape. A doc
// entry is addressed by one item key, so an array naming two items, a bare
// string and an empty key all name something this machine has no way to point
// at, and applying the value to the wrong item is the failure the check exists
// to stop.
func TestDocKeyThatIsNotOneStringIsRefused(t *testing.T) {
	doc := newFakeDoc("config")
	registerDoc(t, doc)

	for _, pk := range []string{`["a","b"]`, `"a"`, `[]`, `[""]`, `{}`, `["a"] junk`, `null`} {
		entry := docEntryWith(t, doc, "policy", 1)
		entry.PK = pk

		if _, err := ParseDocEntry(entry); err == nil {
			t.Errorf("pk %s was accepted, want a refusal", pk)
		}
	}
}

// TestDocBodyThatIsNotAnObjectIsRefused pins the body's shape. The body is the
// only place the stamp and the value travel, so anything that is not the agreed
// object carries fields no check covers -- including, in the worst case, a
// stamp this reader would order by and a value it would not have validated.
func TestDocBodyThatIsNotAnObjectIsRefused(t *testing.T) {
	doc := newFakeDoc("config")
	registerDoc(t, doc)

	for _, body := range []string{`null`, `"text"`, `7`, `[1,2]`, `true`, ``, `{} {"a":1}`} {
		entry := docEntryWith(t, doc, "policy", 1)
		entry.Body = json.RawMessage(body)

		if _, err := ParseDocEntry(entry); err == nil {
			t.Errorf("body %q was accepted, want a refusal", body)
		}
	}
}

// TestDocBodyWithAFieldNoCheckCoversIsRefused pins that a body carrying an
// extra field is refused rather than read on the two fields it knows. A peer
// that added a field is a peer whose ordering rule this build cannot apply, and
// applying the entry on the fields it does recognise would apply it on terms
// the writer did not use.
func TestDocBodyWithAFieldNoCheckCoversIsRefused(t *testing.T) {
	doc := newFakeDoc("config")
	registerDoc(t, doc)

	entry := docEntryWith(t, doc, "policy", 1)
	entry.Body = json.RawMessage(`{"stamp":"1","value":{},"precedence":"wins"}`)

	if _, err := ParseDocEntry(entry); err == nil {
		t.Fatal("ParseDocEntry accepted a body with an extra field, want a refusal")
	}
}

// TestDocEntryWithBadStampIsRefused pins the stamp's shape, which is the field
// every ordering decision is made from. A stamp that is not one canonical
// decimal number has no position: read leniently it would order against stamps
// that are not on the same scale, and read leniently twice the same entry would
// parse into two different positions and apply twice.
func TestDocEntryWithBadStampIsRefused(t *testing.T) {
	doc := newFakeDoc("config")
	registerDoc(t, doc)

	for _, stamp := range []string{``, `abc`, `-1`, `1.5`, ` 1`, `1 `, `01`, `+1`, `0x10`, `1e3`, `18446744073709551616`} {
		entry := docEntryWith(t, doc, "policy", 1)
		entry.Body = json.RawMessage(`{"stamp":` + mustQuote(t, stamp) + `,"value":{}}`)

		if _, err := ParseDocEntry(entry); err == nil {
			t.Errorf("stamp %q was accepted, want a refusal", stamp)
		}
	}
}

// TestDocEntryWhoseStampIsNotTextIsRefused pins that the stamp travels as text.
// Reading it as a number would mean re-encoding it on every hop, and two
// encodings of one stamp would not share a signature.
func TestDocEntryWhoseStampIsNotTextIsRefused(t *testing.T) {
	doc := newFakeDoc("config")
	registerDoc(t, doc)

	entry := docEntryWith(t, doc, "policy", 1)
	entry.Body = json.RawMessage(`{"stamp":1,"value":{}}`)

	if _, err := ParseDocEntry(entry); err == nil {
		t.Fatal("ParseDocEntry accepted a numeric stamp, want a refusal")
	}
}

// TestDocBodyMissingAStampOrValueIsRefused pins that both fields are required.
// A body with no value cannot say whether the item is set or removed, and one
// with no stamp cannot be ordered at all.
func TestDocBodyMissingAStampOrValueIsRefused(t *testing.T) {
	doc := newFakeDoc("config")
	registerDoc(t, doc)

	for _, body := range []string{`{}`, `{"stamp":"1"}`, `{"value":{}}`} {
		entry := docEntryWith(t, doc, "policy", 1)
		entry.Body = json.RawMessage(body)

		if _, err := ParseDocEntry(entry); err == nil {
			t.Errorf("body %s was accepted, want a refusal", body)
		}
	}
}

// TestDocDeleteIsAnUpsertCarryingANullValue pins the shape a doc removal takes.
// It is an upsert whose value is null, never an OpDelete, because a delete
// carries no body and the stamp has to reach the reader that removes the key;
// without it a reader could not tell a removal older than the value it holds
// from one newer than it.
func TestDocDeleteIsAnUpsertCarryingANullValue(t *testing.T) {
	doc := newFakeDoc("config")
	registerDoc(t, doc)

	entry, err := NewDocDelete("origin-a", doc, "policy", 1, 42,
		time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewDocDelete: %v", err)
	}
	if entry.Op != OpUpsert {
		t.Errorf("a doc delete is op %q, want %q", entry.Op, OpUpsert)
	}
	if entry.Table != DocPrefix+"config" {
		t.Errorf("table = %q, want %q", entry.Table, DocPrefix+"config")
	}
	if entry.PK != `["policy"]` {
		t.Errorf("pk = %q, want the one-element key array", entry.PK)
	}

	parsed, err := ParseDocEntry(entry)
	if err != nil {
		t.Fatalf("ParseDocEntry: %v", err)
	}
	if parsed.Stamp != 42 {
		t.Errorf("stamp = %d, want 42; the removal carries its own position", parsed.Stamp)
	}
	if string(parsed.Value) != "null" {
		t.Errorf("value = %s, want the JSON null that means removed", parsed.Value)
	}
}

// TestDocEntryCarryingAnOpDeleteIsRefused pins that a hand-built entry naming
// OpDelete is refused rather than read as a removal: the shape that removes a
// doc key is the null value, and an op the reader honours instead would apply a
// rule this build has not checked.
func TestDocEntryCarryingAnOpDeleteIsRefused(t *testing.T) {
	doc := newFakeDoc("config")
	registerDoc(t, doc)

	entry := docEntryWith(t, doc, "policy", 1)
	entry.Op = OpDelete

	if _, err := ParseDocEntry(entry); err == nil {
		t.Fatal("ParseDocEntry accepted an op delete as a doc entry, want a refusal")
	}
}

// TestSharedTableRefusesDocTables pins the routing boundary. SharedTable is
// what a table name reaches SQL through, and a doc entry's table is not a
// shared table, so a caller that has not taken the prefix off must be told no.
func TestSharedTableRefusesDocTables(t *testing.T) {
	if !IsDocTable(DocPrefix + "config") {
		t.Error("IsDocTable said no to a doc table")
	}
	if _, ok := SharedTable(DocPrefix + "config"); ok {
		t.Error("SharedTable answered for a doc table; doc entries are not shared tables")
	}
	if IsDocTable("repo") {
		t.Error("IsDocTable said yes to a shared table")
	}
}

// TestRegisterRefusesADuplicateDocName pins that two handlers for one document
// cannot both be registered. Whichever was registered last would decide
// silently which half of what a peer sent reached the document.
func TestRegisterRefusesADuplicateDocName(t *testing.T) {
	first := newFakeDoc("dup")
	registerDoc(t, first)

	second := newFakeDoc("dup")
	if err := RegisterSharedDoc(second); err == nil {
		t.Error("RegisterSharedDoc took a second handler for a registered name, want a refusal")
		delete(sharedDocs, "dup")
	}
}

// TestRegisterRefusesAnUnusableDocName pins the names a document may not take.
// An empty name gives `doc:` on its own, which names nothing, and a name with
// a separator leaves a reader unable to tell which word is the document's.
func TestRegisterRefusesAnUnusableDocName(t *testing.T) {
	for _, name := range []string{"", "a b", "a/b", "a:b", "a\tb"} {
		doc := newFakeDoc(name)
		if err := RegisterSharedDoc(doc); err == nil {
			t.Errorf("RegisterSharedDoc took the name %q, want a refusal", name)
			delete(sharedDocs, name)
		}
	}
}

// mustQuote renders text as a JSON string, so a refusal test can write the
// stamp it means rather than a hand-built body.
func mustQuote(t *testing.T, text string) string {
	t.Helper()
	encoded, err := json.Marshal(text)
	if err != nil {
		t.Fatalf("marshal %q: %v", text, err)
	}
	return string(encoded)
}
