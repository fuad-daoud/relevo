package synclog

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

var takeoverAt = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func putRecord(t *testing.T, d *db.DB, id, name string) {
	t.Helper()
	err := d.Tx(func(tx *db.Tx) error {
		_, err := tx.RecordPut(db.Record{ID: id, Owner: "", Name: name, State: "active", Round: 1, CWD: "/x", JSON: "{}", CreatedAt: takeoverAt})
		return err
	})
	if err != nil {
		t.Fatalf("put record: %v", err)
	}
}

func putFile(t *testing.T, d *db.DB, record, name string) {
	t.Helper()
	err := d.Tx(func(tx *db.Tx) error {
		return tx.RoundFilePut(record, name, 1, []byte("x"), takeoverAt, takeoverAt)
	})
	if err != nil {
		t.Fatalf("put round file %s: %v", name, err)
	}
}

func keyedExporter(d *db.DB, log *MemTransport) *Exporter {
	e := NewExporter(d, log)
	e.now = syncClock
	e.SetKey(handoverKey)
	return e
}

func keyedImporter(d *db.DB, log *MemTransport) *Importer {
	imp := NewImporter(d, log)
	imp.SetKey(handoverKey)
	return imp
}

// takeOver is what the verb does on the taker: flip the roots, fence the old
// writer at its import mark and queue the control entry.
func takeOver(t *testing.T, d *db.DB, name, oldWriter string, upto int) {
	t.Helper()
	err := d.Tx(func(tx *db.Tx) error {
		roots, _, _, found, err := tx.ForeignRoots("", name)
		if err != nil || !found {
			t.Fatalf("ForeignRoots = (%t, %v)", found, err)
		}
		if err := tx.TakeOver(roots, oldWriter, upto); err != nil {
			return err
		}
		if err := tx.SetWriterAfter(roots.RecordID, oldWriter, upto); err != nil {
			return err
		}
		body, _ := json.Marshal(Takeover{RecordID: roots.RecordID, OldWriter: oldWriter, Upto: upto, Taker: tx.Origin()})
		return tx.QueueCtl(db.CtlItem{Table: CtlTakeover, PK: TakeoverPK(roots.RecordID, oldWriter), Body: body})
	})
	if err != nil {
		t.Fatalf("take over: %v", err)
	}
}

func TestTakeoverBodyIsCheckedAgainstItsEntry(t *testing.T) {
	t.Parallel()
	good := Takeover{RecordID: "r", OldWriter: "m1", Upto: 4, Taker: "m2"}
	e, err := NewTakeover("m2", good, 31, takeoverAt)
	if err != nil {
		t.Fatalf("NewTakeover: %v", err)
	}
	if e.After == nil || e.After.Origin != "m1" || e.After.Seq != 4 {
		t.Errorf("After = %+v, want the fence entry (m1, 4)", e.After)
	}
	if got, err := DecodeTakeover(e); err != nil || got != good {
		t.Errorf("DecodeTakeover = (%+v, %v), want the body back", got, err)
	}
	if _, err := NewTakeover("m3", good, 31, takeoverAt); err == nil {
		t.Error("NewTakeover accepted a taker that is not the entry's origin")
	}
	if _, err := NewTakeover("m1", Takeover{RecordID: "r", OldWriter: "m1", Taker: "m1"}, 31, takeoverAt); err == nil {
		t.Error("NewTakeover accepted a machine taking its own binding")
	}
	forged := e
	forged.Origin = "m3"
	if _, err := DecodeTakeover(forged); err == nil {
		t.Error("DecodeTakeover accepted a body whose taker is not the entry's origin")
	}
	wrongKey := e
	wrongKey.PK = TakeoverPK("other", "m1")
	if _, err := DecodeTakeover(wrongKey); err == nil {
		t.Error("DecodeTakeover accepted a key that does not name the body's root")
	}
}

// Two machines converge after a takeover, and the machine that lost the binding
// learns it: the returning old writer applies the takeover, exports nothing for
// the binding, counts the change it had not exported as fenced and keeps it.
func TestTakeoverConvergesAndTheReturningMachineReportsItsFencedChanges(t *testing.T) {
	t.Parallel()
	m1, _ := peerFile(t, "m1")
	m2, _ := peerFile(t, "m2")
	log := NewMemTransport("m1")

	putRecord(t, m1, "rec1", "w")
	putFile(t, m1, "rec1", "f1")
	if _, err := keyedExporter(m1, log.OnLog("m1")).Export(); err != nil {
		t.Fatalf("m1 export: %v", err)
	}
	if _, err := keyedImporter(m2, log.OnLog("m2")).Import(); err != nil {
		t.Fatalf("m2 import: %v", err)
	}
	mark, _, err := m2.ImportMark("m1")
	if err != nil || mark != 2 {
		t.Fatalf("m2's mark of m1 = (%d, %v), want 2", mark, err)
	}

	putFile(t, m1, "rec1", "f9") // m1 writes after the partition and has not exported yet
	takeOver(t, m2, "w", "m1", mark)
	putFile(t, m2, "rec1", "f3")
	if _, err := keyedExporter(m2, log.OnLog("m2")).Export(); err != nil {
		t.Fatalf("m2 export: %v", err)
	}

	first, _ := log.OnLog("m2").PullRange("m2", 0, 100)
	if len(first) < 2 || first[0].Table != CtlTakeover {
		t.Fatalf("m2's log = %v, want the takeover entry first", tablesOf(first))
	}
	if len(first[0].MAC) == 0 || first[0].After == nil {
		t.Errorf("takeover entry unsigned or without After: %+v", first[0])
	}

	res, err := keyedImporter(m1, log.OnLog("m1")).Import()
	if err != nil || len(res.Held) != 0 || len(res.Dropped) != 0 {
		t.Fatalf("m1 import = (%+v, %v), want it to apply the takeover cleanly", res, err)
	}
	if got := columnOf(t, m1, `SELECT writer FROM binding_record WHERE id = 'rec1'`); got != "m2" {
		t.Fatalf("m1 sees writer %q, want m2", got)
	}
	if have := sharedRows(t, m1); !strings.Contains(have, `round_file ["rec1","f3"]`) {
		t.Fatalf("m1 lacks the taker's new file: %s", have)
	}

	before, _ := log.OnLog("m1").PullRange("m1", 0, 100)
	if _, err := keyedExporter(m1, log.OnLog("m1")).Export(); err != nil {
		t.Fatalf("m1 second export: %v", err)
	}
	after, _ := log.OnLog("m1").PullRange("m1", 0, 100)
	if len(after) != len(before) {
		t.Fatalf("the returning machine appended %d entries for a binding it lost", len(after)-len(before))
	}
	fenced, err := m1.FencedSummary()
	if err != nil || len(fenced) != 1 || fenced[0].Name != "w" || fenced[0].Count < 1 {
		t.Fatalf("FencedSummary = (%+v, %v), want the unexported change counted", fenced, err)
	}
	if have := sharedRows(t, m1); !strings.Contains(have, `round_file ["rec1","f9"]`) {
		t.Errorf("the fenced change was deleted locally: %s", have)
	}
	if kind, found, _ := m1.BindingLeftByName("", "w"); !found || kind.Kind != db.LeftTakeover {
		t.Errorf("m1 records the binding as %+v, want left by takeover", kind)
	}
}

func tablesOf(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Table
	}
	return out
}

// An entry of the old writer above the fence is refused by an importer that has
// the takeover, and the refusal moves the mark so it is not offered again.
func TestTakeoverRefusesTheOldWritersEntryAboveTheFence(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m3")
	know := knownVersion(t, peer)
	log := NewMemTransport("m1")
	root := map[string]any{"id": "b1", "owner": "", "name": "b1", "state": "open", "round": 1, "cwd": "/x", "record_json": "{}",
		"created_at": "t", "updated_at": "t", "origin": "m1", "writer": "m1"}
	file := func(name string) map[string]any {
		return map[string]any{"record_id": "b1", "name": name, "round": 1, "body": []byte{1}, "bytes": 1, "sha256": "s", "mtime": "t", "sealed_at": "t"}
	}
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", "binding_record", `["b1"]`, OpUpsert, know, root, nil))
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", "round_file", `["b1","f1"]`, OpUpsert, know, file("f1"), nil))

	take, err := NewTakeover("m2", Takeover{RecordID: "b1", OldWriter: "m1", Upto: 2, Taker: "m2"}, know, takeoverAt)
	if err != nil {
		t.Fatalf("NewTakeover: %v", err)
	}
	take.MAC = EntryMAC(handoverKey, take)
	appendSignedBatch(t, log.OnLog("m2"), handoverKey, take)

	if res, err := keyedImporter(peer, log.OnLog("m3")).Import(); err != nil || len(res.Dropped) != 0 || len(res.Held) != 0 {
		t.Fatalf("first import = (%+v, %v)", res, err)
	}
	if got := columnOf(t, peer, `SELECT writer FROM binding_record WHERE id = 'b1'`); got != "m2" {
		t.Fatalf("writer after the takeover = %q, want m2", got)
	}
	if upto, _, _, found, _ := peer.Term("b1", "m1"); !found || upto != 2 {
		t.Fatalf("term of m1 = (%d, %t), want a fence at 2", upto, found)
	}

	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", "round_file", `["b1","f9"]`, OpUpsert, know, file("f9"), nil))
	res, err := keyedImporter(peer, log.OnLog("m3")).Import()
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if len(res.Dropped) != 1 || !strings.Contains(res.Dropped[0].Reason, "writer moved") {
		t.Fatalf("Dropped = %+v, want the entry above the fence refused as writer moved", res.Dropped)
	}
	if have := sharedRows(t, peer); strings.Contains(have, `["b1","f9"]`) {
		t.Errorf("the fenced entry landed: %s", have)
	}
}

// A locked machine keeps the control entry queued instead of appending an
// unsigned one that every unlocked reader would refuse.
func TestTakeoverEntryStaysQueuedWhileLocked(t *testing.T) {
	t.Parallel()
	m2, _ := peerFile(t, "m2")
	log := NewMemTransport("m2")
	err := m2.Tx(func(tx *db.Tx) error {
		body, _ := json.Marshal(Takeover{RecordID: "r", OldWriter: "m1", Upto: 0, Taker: "m2"})
		return tx.QueueCtl(db.CtlItem{Table: CtlTakeover, PK: TakeoverPK("r", "m1"), Body: body})
	})
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	locked := NewExporter(m2, log)
	locked.now = syncClock
	if n, err := locked.exportCtl(); err != nil || n != 0 {
		t.Fatalf("locked exportCtl = (%d, %v), want nothing appended", n, err)
	}
	if pending, _ := m2.PendingCtl(); len(pending) != 1 {
		t.Fatalf("pending after a locked export = %d, want 1", len(pending))
	}
	if n, err := keyedExporter(m2, log).exportCtl(); err != nil || n != 1 {
		t.Fatalf("unlocked exportCtl = (%d, %v), want 1", n, err)
	}
	if pending, _ := m2.PendingCtl(); len(pending) != 0 {
		t.Fatalf("pending after the append = %d, want 0", len(pending))
	}
}

// After a hand-over, the old writer's reconcile never proposes a delete for a
// row it handed over, even once the new writer deleted it.
func TestReconcileSkipsRowsOfAHandedOverBinding(t *testing.T) {
	t.Parallel()
	m1, _ := peerFile(t, "m1")
	log := NewMemTransport("m1")
	putRecord(t, m1, "rec1", "w")
	putFile(t, m1, "rec1", "f1")
	if _, err := keyedExporter(m1, log).Export(); err != nil {
		t.Fatalf("export: %v", err)
	}
	if err := m1.Tx(func(tx *db.Tx) error { _, err := tx.HandOver("", "w", "m2", takeoverAt); return err }); err != nil {
		t.Fatalf("HandOver: %v", err)
	}
	// The new writer deleted the binding: the importer removes the root and,
	// through the cascade, its children.
	if err := m1.Tx(func(tx *db.Tx) error { return tx.ExchangeDelete("binding_record", `["rec1"]`) }); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := m1.Tx(func(tx *db.Tx) error { return tx.DiscardOutboxSince("binding_record", `["rec1"]`, 0) }); err != nil {
		t.Fatalf("discard: %v", err)
	}
	rec := NewReconciler(m1, log)
	rec.now = syncClock
	rec.SetKey(handoverKey)
	res, err := rec.Reconcile()
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Deletes != 0 || res.Upserts != 0 {
		t.Fatalf("Reconcile = %+v, want it to append nothing for a binding it handed over", res)
	}
}

// A takeover back by the machine that was fenced keeps its old fence and
// records the takeover entry as a regrant: the entries it wrote while fenced
// stay refused, and the ones after the regrant are valid.
func TestTakeoverBackRecordsTheRegrantAndKeepsPartitionEraEntriesFenced(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m3")
	know := knownVersion(t, peer)
	log := NewMemTransport("m1")
	root := map[string]any{"id": "b1", "owner": "", "name": "b1", "state": "open", "round": 1, "cwd": "/x", "record_json": "{}",
		"created_at": "t", "updated_at": "t", "origin": "m1", "writer": "m1"}
	file := func(name string) map[string]any {
		return map[string]any{"record_id": "b1", "name": name, "round": 1, "body": []byte{1}, "bytes": 1, "sha256": "s", "mtime": "t", "sealed_at": "t"}
	}
	signedTakeover := func(taker, old string, upto int) Entry {
		e, err := NewTakeover(taker, Takeover{RecordID: "b1", OldWriter: old, Upto: upto, Taker: taker}, know, takeoverAt)
		if err != nil {
			t.Fatalf("NewTakeover: %v", err)
		}
		e.MAC = EntryMAC(handoverKey, e)
		return e
	}
	importAll := func() ImportResult {
		res, err := keyedImporter(peer, log.OnLog("m3")).Import()
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		return res
	}

	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", "binding_record", `["b1"]`, OpUpsert, know, root, nil))
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", "round_file", `["b1","f1"]`, OpUpsert, know, file("f1"), nil))
	appendSignedBatch(t, log.OnLog("m2"), handoverKey, signedTakeover("m2", "m1", 2)) // m2 takes it, fencing m1 at 2
	importAll()

	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", "round_file", `["b1","f9"]`, OpUpsert, know, file("f9"), nil)) // seq 3, fenced
	if res := importAll(); len(res.Dropped) != 1 {
		t.Fatalf("Dropped = %+v, want the partition-era entry refused", res.Dropped)
	}

	appendSignedBatch(t, log, handoverKey, signedTakeover("m1", "m2", 1)) // seq 4: m1 takes it back
	importAll()
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", "round_file", `["b1","f10"]`, OpUpsert, know, file("f10"), nil)) // seq 5
	if res := importAll(); len(res.Dropped) != 0 {
		t.Fatalf("Dropped = %+v, want the entry after the regrant accepted", res.Dropped)
	}

	upto, ro, rs, found, err := peer.Term("b1", "m1")
	if err != nil || !found || upto != 2 || ro != "m1" || rs != 4 {
		t.Fatalf("m1's term = (%d, %q, %d, %t, %v), want the fence at 2 with the regrant (m1, 4)", upto, ro, rs, found, err)
	}
	term := &Term{RootID: "b1", Writer: "m1", Upto: upto, RegrantOrigin: ro, RegrantSeq: rs}
	if EntryValid(term, Entry{Origin: "m1", Seq: 3}) || !EntryValid(term, Entry{Origin: "m1", Seq: 5}) {
		t.Error("the term does not keep seq 3 fenced and accept seq 5")
	}
	if got := columnOf(t, peer, `SELECT writer FROM binding_record WHERE id = 'b1'`); got != "m1" {
		t.Errorf("writer = %q, want m1 after taking it back", got)
	}
	have := sharedRows(t, peer)
	if strings.Contains(have, `["b1","f9"]`) || !strings.Contains(have, `["b1","f10"]`) {
		t.Errorf("rows = %s, want f10 and not f9", have)
	}
}

func TestApplyEntryRefusesAnUnknownControlEntryAndAMalformedTakeover(t *testing.T) {
	t.Parallel()
	d, _ := peerFile(t, "m3")
	unknown := Entry{Origin: "m2", Table: "ctl:other", PK: `["x"]`, Op: OpUpsert, Body: []byte(`{}`), At: takeoverAt}
	bad := Entry{Origin: "m2", Table: CtlTakeover, PK: `["x"]`, Op: OpUpsert, Body: []byte(`{`), At: takeoverAt}
	staleAfter, err := NewTakeover("m2", Takeover{RecordID: "r", OldWriter: "m1", Upto: 5, Taker: "m2"}, 31, takeoverAt)
	if err != nil {
		t.Fatalf("NewTakeover: %v", err)
	}
	staleAfter.After = &Ref{Origin: "m1", Seq: 4}
	for name, e := range map[string]Entry{"unknown ctl table": unknown, "malformed body": bad, "fence the After does not name": staleAfter} {
		err := d.Tx(func(tx *db.Tx) error { _, err := applyEntry(tx, e, nil, takeoverAt); return err })
		if err == nil || !strings.Contains(err.Error(), ErrInvalid.Error()) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

// A takeover with no fence names no entry of the old writer, so it carries no
// After and an importer applies it at once.
func TestTakeoverWithoutAFenceCarriesNoAfter(t *testing.T) {
	t.Parallel()
	d, _ := peerFile(t, "m3")
	e, err := NewTakeover("m2", Takeover{RecordID: "r", OldWriter: "m1", Taker: "m2"}, 31, takeoverAt)
	if err != nil || e.After != nil {
		t.Fatalf("NewTakeover = (%+v, %v), want no After", e.After, err)
	}
	err = d.Tx(func(tx *db.Tx) error { _, err := applyEntry(tx, e, nil, takeoverAt); return err })
	if err != nil {
		t.Fatalf("applyEntry: %v", err)
	}
	if upto, _, _, found, _ := d.Term("r", "m1"); !found || upto != 0 {
		t.Errorf("term = (%d, %t), want a fence at 0 for a root this machine never held", upto, found)
	}
	for _, bad := range []Takeover{
		{OldWriter: "m1", Taker: "m2"},
		{RecordID: "r", Taker: "m2"},
		{RecordID: "r", OldWriter: "m1", Upto: -1, Taker: "m2"},
	} {
		if _, err := NewTakeover("m2", bad, 31, takeoverAt); err == nil {
			t.Errorf("NewTakeover accepted %+v", bad)
		}
	}
}

// A compacted log read as a snapshot holds the origin while an entry in it
// waits for another origin's entry this machine has not applied, and the hold
// names both origins.
func TestSnapshotHoldsWhileAnEntryWaitsForAnotherOrigin(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m3")
	know := knownVersion(t, peer)
	log := NewMemTransport("m2")
	file := map[string]any{"record_id": "b1", "name": "f1", "round": 1, "body": []byte{1}, "bytes": 1, "sha256": "s", "mtime": "t", "sealed_at": "t"}
	after := &Ref{Origin: "m1", Seq: 5}
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m2", "round_file", `["b1","f1"]`, OpUpsert, know, file, after))
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m2", "round_file", `["b1","f1"]`, OpUpsert, know, file, after))
	compactTo(t, log, "m2", 2, 1)

	res, err := keyedImporter(peer, log.OnLog("m3")).Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Snapshots != 0 || len(res.Held) != 1 || res.Held[0].Origin != "m2" || !strings.Contains(res.Held[0].Reason, "waiting for m1 at seq 5") {
		t.Fatalf("import = %+v, want m2 held waiting for m1 at seq 5 and no snapshot applied", res)
	}
}

// A Turso token alone must not move a binding: an origin that has never signed
// may still append unsigned history, but an unsigned takeover from it is
// refused, and the binding keeps its writer.
func TestAnUnsignedTakeoverFromAnOriginThatNeverSignedIsRefused(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m3")
	know := knownVersion(t, peer)
	log := NewMemTransport("m1")
	root := map[string]any{"id": "b1", "owner": "", "name": "b1", "state": "open", "round": 1, "cwd": "/x", "record_json": "{}",
		"created_at": "t", "updated_at": "t", "origin": "m1", "writer": "m1"}
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", "binding_record", `["b1"]`, OpUpsert, know, root, nil))

	take, err := NewTakeover("m2", Takeover{RecordID: "b1", OldWriter: "m1", Upto: 1, Taker: "m2"}, know, takeoverAt)
	if err != nil {
		t.Fatalf("NewTakeover: %v", err)
	}
	appendSignedBatch(t, log.OnLog("m2"), nil, take)

	res, err := keyedImporter(peer, log.OnLog("m3")).Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := columnOf(t, peer, `SELECT writer FROM binding_record WHERE id = 'b1'`); got != "m1" {
		t.Fatalf("writer = %q after an unsigned takeover, want m1 kept", got)
	}
	if _, _, _, found, _ := peer.Term("b1", "m1"); found {
		t.Fatalf("an unsigned takeover raised a fence on m1")
	}
	if stalled := fmt.Sprint(res.Stalled); !strings.Contains(stalled, "requires a signature") {
		t.Fatalf("Stalled = %s, want the takeover held as unsigned", stalled)
	}
}

// When a returning writer applies a takeover, its own entries for that binding
// that it already exported above the fence are recorded as fenced, while entries
// for another binding are not counted and nothing is deleted locally.
func TestTakeoverRecordsExportedEntriesAboveFenceAsFenced(t *testing.T) {
	t.Parallel()
	m1, _ := peerFile(t, "m1")
	m2, _ := peerFile(t, "m2")
	log := NewMemTransport("m1")

	putRecord(t, m1, "rec1", "hw")
	putRecord(t, m1, "rec2", "other")
	putFile(t, m1, "rec1", "f1")
	putFile(t, m1, "rec2", "f2")
	if _, err := keyedExporter(m1, log.OnLog("m1")).Export(); err != nil {
		t.Fatalf("m1 export: %v", err)
	}
	if _, err := keyedImporter(m2, log.OnLog("m2")).Import(); err != nil {
		t.Fatalf("m2 import: %v", err)
	}
	mark, _, err := m2.ImportMark("m1")
	if err != nil || mark == 0 {
		t.Fatalf("m2's mark of m1 = (%d, %v), want non-zero mark", mark, err)
	}

	// m1 writes changes to both bindings above N and exports them.
	putFile(t, m1, "rec1", "f-hw-above-n")
	putFile(t, m1, "rec2", "f-other-above-n")
	if _, err := keyedExporter(m1, log.OnLog("m1")).Export(); err != nil {
		t.Fatalf("m1 second export: %v", err)
	}

	// m2 takes over hw at fence mark N and exports the takeover.
	takeOver(t, m2, "hw", "m1", mark)
	if _, err := keyedExporter(m2, log.OnLog("m2")).Export(); err != nil {
		t.Fatalf("m2 export: %v", err)
	}

	// m1 applies the takeover.
	res, err := keyedImporter(m1, log.OnLog("m1")).Import()
	if err != nil || len(res.Held) != 0 || len(res.Dropped) != 0 {
		t.Fatalf("m1 import = (%+v, %v), want clean takeover application", res, err)
	}

	// Verify m1's status reports 1 fenced change for hw, and other is not counted.
	fenced, err := m1.FencedSummary()
	if err != nil {
		t.Fatalf("FencedSummary: %v", err)
	}
	if len(fenced) != 1 {
		t.Fatalf("FencedSummary len = %d, want 1: %+v", len(fenced), fenced)
	}
	if fenced[0].Name != "hw" || fenced[0].Count != 1 {
		t.Errorf("FencedSummary[0] = %+v, want Name 'hw' and Count 1", fenced[0])
	}

	// Verify nothing was deleted locally.
	rows := sharedRows(t, m1)
	if !strings.Contains(rows, `round_file ["rec1","f-hw-above-n"]`) {
		t.Errorf("local change to hw was deleted: %s", rows)
	}
	if !strings.Contains(rows, `round_file ["rec2","f-other-above-n"]`) {
		t.Errorf("local change to other was deleted: %s", rows)
	}
}
