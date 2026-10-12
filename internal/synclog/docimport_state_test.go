package synclog

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// farAhead is a stamp no wall clock reaches during a test, so a clock that was
// not moved past it issues stamps below it.
func farAhead() Stamp { return stampAt(time.Now().Add(10*365*24*time.Hour), 0) }

func nextStamp(t *testing.T, d *db.DB) Stamp {
	t.Helper()
	got, err := NewHybridClock(d.LocalOrSelf()).Now()
	if err != nil {
		t.Fatalf("Now: %v", err)
	}
	return got
}

func exportBack(t *testing.T, d *db.DB, path string, doc SharedDoc) string {
	t.Helper()
	r := &recording{MemTransport: NewMemTransport(d.Origin())}
	e := exporter(d, r)
	e.SetKey(keyA)
	e.UseDocs(doc)
	dirty(t, path)
	if _, err := e.Export(); err != nil {
		t.Fatalf("export: %v", err)
	}
	return docShape(r)
}

// TestImportedUpsertIsNotExportedBack pins that an applied entry leaves the
// export state holding what it applied, so this machine's next export finds
// nothing of that key to send.
func TestImportedUpsertIsNotExportedBack(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	m2, path := peerFile(t, "m2")

	if _, err := docImporter(m2, log, keyA, doc).Import(); err != nil {
		t.Fatalf("import: %v", err)
	}
	if string(doc.items["policy"]) != `{"a":1}` {
		t.Fatalf("policy = %s, want the entry applied", doc.items["policy"])
	}
	if got := exportBack(t, m2, path, doc); got != "" {
		t.Errorf("export after an import sent %q, want nothing", got)
	}
}

// TestImportedTombstoneIsNotExportedBack pins the same for a removal of a key
// this machine had exported: it leaves the export state, so the exporter sees
// no key to delete.
func TestImportedTombstoneIsNotExportedBack(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	doc.items["policy"] = json.RawMessage(`{"a":1}`)
	digest, err := ItemDigest(doc.items["policy"])
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 6, "", keyA)
	m2, path := peerFile(t, "m2")
	seed(t, path, `INSERT INTO shared_export_state (doc, key, origin, stamp, digest) VALUES ('config', 'policy', 'm2', '3', '`+digest+`')`)

	if _, err := docImporter(m2, log, keyA, doc).Import(); err != nil {
		t.Fatalf("import tombstone: %v", err)
	}
	if _, held := doc.items["policy"]; held {
		t.Fatal("the tombstone left the key in the doc")
	}
	state, err := m2.DocExportState("config")
	if err != nil || len(state) != 0 {
		t.Fatalf("export state = %v, %v; want the removed key forgotten", state, err)
	}
	if got := exportBack(t, m2, path, doc); got != "" {
		t.Errorf("export after a tombstone import sent %q, want nothing", got)
	}
}

// TestImportMovesTheClockPastAnAppliedStamp pins that the next stamp this
// machine issues outranks an entry it applied.
func TestImportMovesTheClockPastAnAppliedStamp(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	stamp := farAhead()
	signedDoc(t, log, doc, "m1", "policy", stamp, `{"a":1}`, keyA)
	m2, _ := peerFile(t, "m2")

	if _, err := docImporter(m2, log, keyA, doc).Import(); err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := nextStamp(t, m2); got <= stamp {
		t.Errorf("next stamp %d does not outrank the applied %d", got, stamp)
	}
}

// TestImportMovesTheClockPastASupersededStamp pins that an entry which loses to
// the one applied still moves the clock: the machine has seen the stamp.
func TestImportMovesTheClockPastASupersededStamp(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	loser := farAhead()
	signedDoc(t, log, doc, "m1", "policy", loser, `{"old":true}`, keyA)
	m2, path := peerFile(t, "m2")
	seed(t, path, `INSERT INTO shared_applied (doc, key, origin, stamp) VALUES ('config', 'policy', 'm9', '`+(loser+1000).String()+`')`)

	if _, err := docImporter(m2, log, keyA, doc).Import(); err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := strings.Join(doc.revisions, "|"); got != "sync-superseded:m1 policy" {
		t.Fatalf("revisions = %q, want the entry superseded", got)
	}
	if got := nextStamp(t, m2); got <= loser {
		t.Errorf("next stamp %d does not outrank the superseded %d", got, loser)
	}
}

// TestLockedImportMovesTheClockPastAParkedStamp pins that a locked reader that
// only parks an entry still moves the clock past its stamp.
func TestLockedImportMovesTheClockPastAParkedStamp(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	stamp := farAhead()
	signedDoc(t, log, doc, "m1", "policy", stamp, `{"a":1}`, keyA)
	m2, _ := peerFile(t, "m2")

	if _, err := docImporter(m2, log, nil, doc).Import(); err != nil {
		t.Fatalf("locked import: %v", err)
	}
	if got := pendingCount(t, m2); got != 1 {
		t.Fatalf("pending = %d, want the parked entry", got)
	}
	if got := nextStamp(t, m2); got <= stamp {
		t.Errorf("next stamp %d does not outrank the parked %d", got, stamp)
	}
}

func droppedReason(res ImportResult, origin string) string {
	for _, d := range res.Dropped {
		if d.Origin == origin {
			return d.Reason
		}
	}
	return ""
}

// parkRaw parks text as the entry of (origin, seq), as a locked import would
// have left it.
func parkRaw(t *testing.T, d *db.DB, origin string, seq int, text string) {
	t.Helper()
	if err := d.LocalOrSelf().Tx(func(tx *db.Tx) error {
		return tx.ParkDoc(origin, seq, text, "2026-10-12T09:00:00Z")
	}); err != nil {
		t.Fatalf("park: %v", err)
	}
}

// pulled is the entries the log holds for origin, as the importer would read
// them.
func pulled(t *testing.T, log *MemTransport) []Entry {
	t.Helper()
	entries, err := log.OnLog("reader").Pull(map[string]int{})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	return entries
}

func marshalEntry(t *testing.T, e Entry) string {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// TestDrainMovesTheClockPastEveryParkedStamp pins that applying parked entries
// moves the clock itself, not only the import that parked them.
func TestDrainMovesTheClockPastEveryParkedStamp(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	stamp := farAhead()
	signedDoc(t, log, doc, "m1", "policy", stamp, `{"a":1}`, keyA)
	m2, _ := peerFile(t, "m2")
	parkRaw(t, m2, "m1", 1, marshalEntry(t, pulled(t, log)[0]))

	res, err := docImporter(m2, log, keyA, doc).DrainPending()
	if err != nil || res.Applied != 1 {
		t.Fatalf("DrainPending = %+v, %v; want the one entry applied", res, err)
	}
	if got := nextStamp(t, m2); got <= stamp {
		t.Errorf("next stamp %d does not outrank the drained %d", got, stamp)
	}
}

// TestDrainDropsParkedEntriesThatCannotBeTrusted pins each reason a parked
// entry is refused and removed instead of applied.
func TestDrainDropsParkedEntriesThatCannotBeTrusted(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	ghost := newTxDoc("ghost")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	signedDoc(t, log, ghost, "m1", "policy", 6, `{"a":1}`, keyA)
	entries := pulled(t, log)
	m2, _ := peerFile(t, "m2")
	parkRaw(t, m2, "m1", 1, marshalEntry(t, entries[0]))
	parkRaw(t, m2, "m1", 2, "{not json")
	parkRaw(t, m2, "m1", 3, marshalEntry(t, entries[0]))
	unknownDoc := entries[1]
	unknownDoc.Seq = 4
	parkRaw(t, m2, "m1", 4, marshalEntry(t, unknownDoc))
	forged := entries[0]
	forged.Seq = 5
	forged.MAC = append([]byte(nil), forged.MAC...)
	forged.MAC[0] ^= 0xff
	parkRaw(t, m2, "m1", 5, marshalEntry(t, forged))

	res, err := docImporter(m2, log, keyA, doc).DrainPending()
	if err != nil {
		t.Fatalf("DrainPending: %v", err)
	}
	reasons := map[int]string{}
	for _, d := range res.Dropped {
		reasons[d.Seq] = d.Reason
	}
	want := map[int]string{2: "cannot be read", 3: "names another entry", 4: "no shared doc named", 5: "no valid signature"}
	for seq, part := range want {
		if !strings.Contains(reasons[seq], part) {
			t.Errorf("seq %d reason %q, want it to say %q", seq, reasons[seq], part)
		}
	}
	if res.Applied != 1 || len(doc.revisions) != 1 {
		t.Errorf("applied %d, revisions %v; want only the sound entry", res.Applied, doc.revisions)
	}
	if got := pendingCount(t, m2); got != 0 {
		t.Errorf("pending = %d, want every entry settled", got)
	}
}

// TestDrainWithoutAKeyLeavesTheParkedEntries pins that a locked machine drains
// nothing and removes nothing.
func TestDrainWithoutAKeyLeavesTheParkedEntries(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	m2, _ := peerFile(t, "m2")
	parkRaw(t, m2, "m1", 1, "{}")

	res, err := docImporter(m2, NewMemTransport("m1"), nil, doc).DrainPending()
	if err != nil || res.Applied != 0 || len(res.Dropped) != 0 {
		t.Fatalf("DrainPending = %+v, %v; want nothing done", res, err)
	}
	if got := pendingCount(t, m2); got != 1 {
		t.Errorf("pending = %d, want the entry kept", got)
	}
}

// TestDrainReportsAStoreThatCannotListTheParkedEntries pins that a failing
// store is an error and not an empty queue.
func TestDrainReportsAStoreThatCannotListTheParkedEntries(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	m2, path := peerFile(t, "m2")
	seed(t, path, `DROP TABLE shared_pending`)

	if _, err := docImporter(m2, NewMemTransport("m1"), keyA, doc).DrainPending(); err == nil ||
		!strings.Contains(err.Error(), "pending docs") {
		t.Fatalf("DrainPending = %v, want a pending docs error", err)
	}
}

// TestCorruptAppliedStampDropsTheBatch pins that an applied stamp which does
// not parse drops the batch and never read as zero, which would apply anything.
func TestCorruptAppliedStampDropsTheBatch(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	m2, path := peerFile(t, "m2")
	seed(t, path, `INSERT INTO shared_applied (doc, key, origin, stamp) VALUES ('config', 'policy', 'm9', 'garbage')`)

	res, err := docImporter(m2, log, keyA, doc).Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if reason := droppedReason(res, "m1"); !strings.Contains(reason, "applied stamp") {
		t.Fatalf("dropped %q, want an applied stamp error", reason)
	}
	if len(doc.revisions) != 0 {
		t.Errorf("revisions %v: nothing may apply over a stamp that cannot be read", doc.revisions)
	}
}

// TestDocThatCannotApplyInATransactionDropsTheBatch pins that a document
// without ApplyIn is refused rather than applied outside the importer's
// transaction.
func TestDocThatCannotApplyInATransactionDropsTheBatch(t *testing.T) {
	t.Parallel()
	doc := newFakeDoc("config")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	m2, _ := peerFile(t, "m2")

	res, err := docImporter(m2, log, keyA, doc).Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if reason := droppedReason(res, "m1"); !strings.Contains(reason, "cannot apply inside a transaction") {
		t.Fatalf("dropped %q, want the doc refused", reason)
	}
	if len(doc.applied) != 0 {
		t.Errorf("applied %v: the doc was applied outside the transaction", doc.applied)
	}
}

func TestDocValueTurnsNullIntoNil(t *testing.T) {
	t.Parallel()
	if got := docValue(json.RawMessage(" null\n")); got != nil {
		t.Errorf("docValue(null) = %q, want nil", got)
	}
	if got := docValue(json.RawMessage(`{"a":null}`)); string(got) != `{"a":null}` {
		t.Errorf("docValue(object) = %q, want it unchanged", got)
	}
}
