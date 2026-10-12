package synclog

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// txDoc is a fake document that applies inside the importer's transaction and
// keeps the revision each apply would leave, so a test reads what the importer
// decided rather than only what survived.
type txDoc struct {
	*fakeDoc
	revisions []string
	refuses   map[string]bool
	// needs maps a key to the key that has to be present before it can land.
	needs map[string]string
}

func newTxDoc(name string) *txDoc {
	return &txDoc{fakeDoc: newFakeDoc(name), refuses: map[string]bool{}, needs: map[string]string{}}
}

func (d *txDoc) ApplyIn(_ *db.Tx, key string, value json.RawMessage, label string) (error, error) {
	if d.refuses[key] {
		return fmt.Errorf("%s is refused", key), nil
	}
	if need := d.needs[key]; need != "" {
		if _, ok := d.items[need]; !ok {
			return fmt.Errorf("%s needs %s", key, need), nil
		}
	}
	if value == nil {
		delete(d.items, key)
	} else {
		d.items[key] = value
	}
	d.revisions = append(d.revisions, fmt.Sprintf("sync:%s %s=%s", label, key, value))
	return nil, nil
}

func (d *txDoc) SupersedeIn(_ *db.Tx, key string, _ json.RawMessage, label, _ string) error {
	d.revisions = append(d.revisions, "sync-superseded:"+label+" "+key)
	return nil
}

// signedDoc appends one doc entry written by origin to the log, signed unless
// key is nil. A nil value is the tombstone.
func signedDoc(t *testing.T, log *MemTransport, doc SharedDoc, origin, key string, stamp Stamp, value string, mac []byte) {
	t.Helper()
	var raw json.RawMessage
	if value != "" {
		raw = json.RawMessage(value)
	}
	e, err := NewDocUpsert(origin, doc, key, 1, stamp, raw, time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewDocUpsert: %v", err)
	}
	writer := keyed{key: mac}
	entries := []Entry{e}
	writer.sign(entries)
	if _, err := writer.appendTo(log.OnLog(origin), origin, entries); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func docImporter(d *db.DB, log *MemTransport, key []byte, docs ...SharedDoc) *Importer {
	imp := NewImporter(d, log.OnLog(d.Origin()))
	imp.SetKey(key)
	imp.UseDocs(docs...)
	return imp
}

func pendingCount(t *testing.T, d *db.DB) int {
	t.Helper()
	rows, err := d.PendingDocs()
	if err != nil {
		t.Fatalf("PendingDocs: %v", err)
	}
	return len(rows)
}

// TestUnsignedDocEntryIsRefusedWhenUnlocked pins that an unlocked reader takes
// a document only from a signed entry, even from an origin that has never
// signed anything: the signature is the only thing that says the entry came
// from a machine holding the key.
func TestUnsignedDocEntryIsRefusedWhenUnlocked(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, nil)
	m2, _ := peerFile(t, "m2")

	res, err := docImporter(m2, log, keyA, doc).Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if reason := stallOf(res, "m1"); !strings.Contains(reason, "doc entry 1") || !strings.Contains(reason, "unsigned") {
		t.Errorf("stall %q does not name the unsigned doc entry", reason)
	}
	if len(doc.revisions) != 0 || markOf(t, m2, "m1") != 0 {
		t.Errorf("revisions %v, mark %d: an unsigned doc entry must apply nothing and leave the mark", doc.revisions, markOf(t, m2, "m1"))
	}
}

// TestBadDocSignatureHoldsTheOrigin pins that a doc entry whose MAC does not
// hold stops its origin for the run with the mark where it was.
func TestBadDocSignatureHoldsTheOrigin(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	forgeMAC(log, 1)
	m2, _ := peerFile(t, "m2")

	res, err := docImporter(m2, log, keyA, doc).Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if reason := stallOf(res, "m1"); !strings.Contains(reason, "bad signature") {
		t.Errorf("stall %q lacks the bad signature sentence", reason)
	}
	if len(doc.revisions) != 0 || markOf(t, m2, "m1") != 0 {
		t.Errorf("revisions %v, mark %d: a forged doc entry must apply nothing", doc.revisions, markOf(t, m2, "m1"))
	}
}

// TestLockedReaderParksDocEntriesAndMovesItsMark pins that a reader with no key
// holds a doc entry whole instead of applying it, and does not stall on it.
func TestLockedReaderParksDocEntriesAndMovesItsMark(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	m2, _ := peerFile(t, "m2")

	if _, err := docImporter(m2, log, nil, doc).Import(); err != nil {
		t.Fatalf("locked import: %v", err)
	}
	if len(doc.revisions) != 0 {
		t.Errorf("revisions %v: a locked reader applied an entry it cannot verify", doc.revisions)
	}
	if got := pendingCount(t, m2); got != 1 {
		t.Errorf("pending = %d, want the one parked entry", got)
	}
	if got := markOf(t, m2, "m1"); got != 1 {
		t.Errorf("mark = %d, want 1: parking moves the mark", got)
	}
}

// TestUnlockDrainsPending pins that parked entries stay unapplied until a key
// arrives and then verify and apply, and that one that does not verify is
// refused and removed rather than applied.
func TestUnlockDrainsPending(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	signedDoc(t, log, doc, "m1", "roles", 6, `{"b":1}`, keyA)
	forgeMAC(log, 2)
	m2, _ := peerFile(t, "m2")
	if _, err := docImporter(m2, log, nil, doc).Import(); err != nil {
		t.Fatalf("locked import: %v", err)
	}
	if len(doc.revisions) != 0 {
		t.Fatalf("revisions %v before the key", doc.revisions)
	}

	res, err := docImporter(m2, log, keyA, doc).DrainPending()
	if err != nil {
		t.Fatalf("DrainPending: %v", err)
	}
	if got, want := strings.Join(doc.revisions, "|"), `sync:m1 policy={"a":1}`; got != want {
		t.Errorf("revisions = %q, want %q", got, want)
	}
	if len(res.Dropped) != 1 || !strings.Contains(res.Dropped[0].Reason, "no valid signature") {
		t.Errorf("dropped = %v, want the forged entry refused", res.Dropped)
	}
	if got := pendingCount(t, m2); got != 0 {
		t.Errorf("pending = %d after the drain, want none", got)
	}
}

// TestKeyedImportDrainsPending pins that every keyed import drains what an
// earlier locked one parked, before it reads the log.
func TestKeyedImportDrainsPending(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	m2, _ := peerFile(t, "m2")
	if _, err := docImporter(m2, log, nil, doc).Import(); err != nil {
		t.Fatalf("locked import: %v", err)
	}

	res, err := docImporter(m2, log, keyA, doc).Import()
	if err != nil {
		t.Fatalf("keyed import: %v", err)
	}
	if res.Applied != 1 || len(doc.revisions) != 1 {
		t.Errorf("applied %d, revisions %v: the keyed import should apply the parked entry once", res.Applied, doc.revisions)
	}
	if got := pendingCount(t, m2); got != 0 {
		t.Errorf("pending = %d, want none", got)
	}
}

// TestNewerStampWinsTieGoesToHigherOrigin pins the ordering rule, and that it
// does not depend on the order the entries arrive in.
func TestNewerStampWinsTieGoesToHigherOrigin(t *testing.T) {
	t.Parallel()
	writes := map[string]func(*testing.T, *MemTransport, SharedDoc){
		"m1": func(t *testing.T, log *MemTransport, doc SharedDoc) {
			signedDoc(t, log, doc, "m1", "tie", 100, `"from-m1"`, keyA)
			signedDoc(t, log, doc, "m1", "newer", 200, `"from-m1"`, keyA)
		},
		"m2": func(t *testing.T, log *MemTransport, doc SharedDoc) {
			signedDoc(t, log, doc, "m2", "tie", 100, `"from-m2"`, keyA)
			signedDoc(t, log, doc, "m2", "newer", 150, `"from-m2"`, keyA)
		},
	}
	for _, order := range [][]string{{"m1", "m2"}, {"m2", "m1"}} {
		doc := newTxDoc("config")
		log := NewMemTransport("m1")
		m3, _ := peerFile(t, "m3")
		for _, from := range order {
			writes[from](t, log, doc)
			if _, err := docImporter(m3, log, keyA, doc).Import(); err != nil {
				t.Fatalf("import after %s: %v", from, err)
			}
		}
		if got := string(doc.items["tie"]); got != `"from-m2"` {
			t.Errorf("order %v: tie = %s, want the higher origin m2", order, got)
		}
		if got := string(doc.items["newer"]); got != `"from-m1"` {
			t.Errorf("order %v: newer = %s, want the higher stamp from m1", order, got)
		}
	}
}

// TestOlderEntryLandsSuperseded pins that an older entry changes nothing and
// says so, and that the entry already applied keeps its place.
func TestOlderEntryLandsSuperseded(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 200, `{"new":true}`, keyA)
	m3, _ := peerFile(t, "m3")
	if _, err := docImporter(m3, log, keyA, doc).Import(); err != nil {
		t.Fatalf("first import: %v", err)
	}
	signedDoc(t, log, doc, "m2", "policy", 100, `{"old":true}`, keyA)
	if _, err := docImporter(m3, log, keyA, doc).Import(); err != nil {
		t.Fatalf("second import: %v", err)
	}
	if got := string(doc.items["policy"]); got != `{"new":true}` {
		t.Errorf("policy = %s, want the newer value kept", got)
	}
	if got, want := strings.Join(doc.revisions, "|"), `sync:m1 policy={"new":true}|sync-superseded:m2 policy`; got != want {
		t.Errorf("revisions = %q, want %q", got, want)
	}
}

// TestFailedMarkTransactionRedeliversWithoutASecondRevision pins the crash
// between the two transactions: the doc half has committed, the mark has not,
// and the redelivery applies nothing twice.
func TestFailedMarkTransactionRedeliversWithoutASecondRevision(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	m2, path := peerFile(t, "m2")
	seed(t, path, `CREATE TRIGGER fail_mark BEFORE INSERT ON sync_import_mark BEGIN SELECT RAISE(ABORT, 'no mark'); END`)

	if _, err := docImporter(m2, log, keyA, doc).Import(); err == nil {
		t.Fatal("import with a failing mark transaction = nil error")
	}
	seed(t, path, `DROP TRIGGER fail_mark`)
	if _, err := docImporter(m2, log, keyA, doc).Import(); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if got := len(doc.revisions); got != 1 {
		t.Errorf("revisions = %v, want exactly one", doc.revisions)
	}
	if string(doc.items["policy"]) != `{"a":1}` || markOf(t, m2, "m1") != 1 {
		t.Errorf("policy %s, mark %d: the redelivery should leave the applied state and move the mark",
			doc.items["policy"], markOf(t, m2, "m1"))
	}
}

// TestSnapshotRoutesDocEntriesToTheDoc pins that a region read up to a floor
// hands its doc entries to the document, once per key, and the rows to the rest.
func TestSnapshotRoutesDocEntriesToTheDoc(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	signedDoc(t, log, doc, "m1", "policy", 6, `{"a":2}`, keyA)
	signedDoc(t, log, doc, "m1", "roles", 7, `{"b":1}`, keyA)
	if _, err := log.Compact("m1", 3); err != nil {
		t.Fatalf("compact: %v", err)
	}
	m2, _ := peerFile(t, "m2")

	res, err := docImporter(m2, log, keyA, doc).Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Snapshots != 1 {
		t.Fatalf("result %+v, want a snapshot", res)
	}
	if string(doc.items["policy"]) != `{"a":2}` || string(doc.items["roles"]) != `{"b":1}` || len(doc.revisions) != 2 {
		t.Errorf("items %v, revisions %v: want the latest entry per key, once", doc.items, doc.revisions)
	}
}

// TestDependentItemsApplyOnRetry pins that an item the document refuses for the
// state it is in lands once the key it depends on has, whatever order the
// exporter sent them in, and that an item that never can is recorded and does
// not stop the others.
func TestDependentItemsApplyOnRetry(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	doc.needs["actor"] = "agent"
	doc.refuses["never"] = true
	log := NewMemTransport("m1")
	e := func(key string, stamp Stamp, value string) Entry {
		entry, err := NewDocUpsert("m1", doc, key, 1, stamp, json.RawMessage(value), time.Now())
		if err != nil {
			t.Fatalf("NewDocUpsert: %v", err)
		}
		return entry
	}
	writer := keyed{key: keyA}
	batch := []Entry{e("actor", 1, `"a"`), e("never", 2, `"n"`), e("agent", 3, `"g"`)}
	writer.sign(batch)
	if _, err := writer.appendTo(log.OnLog("m1"), "m1", batch); err != nil {
		t.Fatalf("append: %v", err)
	}
	m2, _ := peerFile(t, "m2")

	if _, err := docImporter(m2, log, keyA, doc).Import(); err != nil {
		t.Fatalf("import: %v", err)
	}
	if string(doc.items["actor"]) != `"a"` || string(doc.items["agent"]) != `"g"` {
		t.Errorf("items %v: the actor should land once its agent has", doc.items)
	}
	if _, landed := doc.items["never"]; landed {
		t.Error("an item the document always refuses landed")
	}
	if got := strings.Join(doc.revisions, "|"); !strings.Contains(got, "sync-superseded:m1 never") || strings.Contains(got, "sync-superseded:m1 actor") {
		t.Errorf("revisions = %q, want only the item that never lands superseded", got)
	}
}
