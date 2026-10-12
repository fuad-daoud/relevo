package synclog

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// stampedDoc is a fake doc that also says when each key was written.
type stampedDoc struct {
	*fakeDoc
	at map[string]time.Time
}

func (s *stampedDoc) ItemUpdatedAt(key string) (time.Time, bool, error) {
	at, ok := s.at[key]
	return at, ok, nil
}

// dirty writes a config_doc row, so the trigger records the signal the way a
// real config write does.
func dirty(t *testing.T, path string) {
	t.Helper()
	seed(t, path, `INSERT OR REPLACE INTO config_doc (name, body, updated_at) VALUES ('policy', '{}', '2026-10-12T00:00:00.000Z')`)
}

func docExporter(d *db.DB, r *recording, key []byte) *Exporter {
	e := exporter(d, r)
	e.SetKey(key)
	return e
}

func docShape(r *recording) string {
	var out []string
	for _, e := range r.appended() {
		if !IsDocTable(e.Table) {
			continue
		}
		parsed, err := ParseDocEntry(e)
		if err != nil {
			out = append(out, "bad:"+err.Error())
			continue
		}
		out = append(out, parsed.Key+"="+string(parsed.Value))
	}
	return strings.Join(out, "|")
}

// TestExporterEmitsOneEntryPerChangedKey pins that an export sends a signed
// entry for each key that differs from the last export and for each key the
// doc no longer holds, and that the second export with nothing changed sends
// nothing.
func TestExporterEmitsOneEntryPerChangedKey(t *testing.T) {
	doc := newFakeDoc("config")
	registerDoc(t, doc)
	doc.items["policy"] = json.RawMessage(`{"a":1}`)
	doc.items["roles"] = json.RawMessage(`{"b":1}`)
	d, path := exporterFile(t, "m1")
	r := &recording{MemTransport: NewMemTransport("m1")}
	dirty(t, path)

	if _, err := docExporter(d, r, keyA).Export(); err != nil {
		t.Fatalf("first export: %v", err)
	}
	if got, want := docShape(r), `policy={"a":1}|roles={"b":1}`; got != want {
		t.Fatalf("first export = %q, want %q", got, want)
	}
	for _, e := range r.appended() {
		if !VerifyMAC(keyA, e) {
			t.Errorf("%s %s is not signed", e.Table, e.PK)
		}
	}

	r.batches = nil
	dirty(t, path)
	if _, err := docExporter(d, r, keyA).Export(); err != nil {
		t.Fatalf("second export: %v", err)
	}
	if got := docShape(r); got != "" {
		t.Fatalf("second export with nothing changed = %q, want empty", got)
	}

	doc.items["policy"] = json.RawMessage(`{"a":2}`)
	delete(doc.items, "roles")
	dirty(t, path)
	if _, err := docExporter(d, r, keyA).Export(); err != nil {
		t.Fatalf("third export: %v", err)
	}
	if got, want := docShape(r), `policy={"a":2}|roles=null`; got != want {
		t.Fatalf("third export = %q, want %q", got, want)
	}
	if signals, err := d.DocSignals(); err != nil || len(signals) != 0 {
		t.Fatalf("signals after export = %v, %v; want none", signals, err)
	}
}

// TestFirstExportStampsFromUpdatedAt pins that a key never exported takes the
// time the doc was written, not the clock, and that the next change outranks it.
func TestFirstExportStampsFromUpdatedAt(t *testing.T) {
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	doc := &stampedDoc{fakeDoc: newFakeDoc("config"), at: map[string]time.Time{"policy": old}}
	registerDoc(t, doc.fakeDoc)
	sharedDocs["config"] = doc
	doc.items["policy"] = json.RawMessage(`{"a":1}`)
	d, path := exporterFile(t, "m1")
	r := &recording{MemTransport: NewMemTransport("m1")}
	dirty(t, path)

	if _, err := docExporter(d, r, keyA).Export(); err != nil {
		t.Fatalf("export: %v", err)
	}
	first, err := ParseDocEntry(r.appended()[0])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if want := stampAt(old, 0); first.Stamp != want {
		t.Fatalf("first stamp = %d, want %d, the time the doc was written", first.Stamp, want)
	}

	doc.items["policy"] = json.RawMessage(`{"a":2}`)
	dirty(t, path)
	if _, err := docExporter(d, r, keyA).Export(); err != nil {
		t.Fatalf("second export: %v", err)
	}
	second, err := ParseDocEntry(r.appended()[1])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if second.Stamp <= first.Stamp {
		t.Fatalf("second stamp %d does not outrank the first %d", second.Stamp, first.Stamp)
	}
}

// TestAppliedEntryDoesNotReexport pins that a value the export state already
// holds the digest of, as an apply leaves it, is not sent back out.
func TestAppliedEntryDoesNotReexport(t *testing.T) {
	doc := newFakeDoc("config")
	registerDoc(t, doc)
	value := json.RawMessage(`{"a":1}`)
	doc.items["policy"] = value
	d, path := exporterFile(t, "m1")
	digest, err := ItemDigest(value)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	seed(t, path, `INSERT INTO shared_export_state (doc, key, origin, stamp, digest) VALUES ('config', 'policy', 'm2', '9', '`+digest+`')`)
	r := &recording{MemTransport: NewMemTransport("m1")}
	dirty(t, path)

	if _, err := docExporter(d, r, keyA).Export(); err != nil {
		t.Fatalf("export: %v", err)
	}
	if got := docShape(r); got != "" {
		t.Fatalf("an applied value was exported again: %q", got)
	}
}

// TestLockedMachineExportsNoDocEntries pins that a machine with no key sends no
// doc entry and keeps the signal for the export that has one.
func TestLockedMachineExportsNoDocEntries(t *testing.T) {
	doc := newFakeDoc("config")
	registerDoc(t, doc)
	doc.items["policy"] = json.RawMessage(`{"a":1}`)
	d, path := exporterFile(t, "m1")
	r := &recording{MemTransport: NewMemTransport("m1")}
	dirty(t, path)

	if _, err := docExporter(d, r, nil).Export(); err != nil {
		t.Fatalf("locked export: %v", err)
	}
	if got := docShape(r); got != "" {
		t.Fatalf("a locked machine exported %q", got)
	}
	if signals, err := d.DocSignals(); err != nil || len(signals) != 1 {
		t.Fatalf("signals = %v, %v; want the one kept", signals, err)
	}

	if _, err := docExporter(d, r, keyA).Export(); err != nil {
		t.Fatalf("unlocked export: %v", err)
	}
	if got := docShape(r); got != `policy={"a":1}` {
		t.Fatalf("unlocked export = %q, want the kept key", got)
	}
}

// TestReconcileIgnoresDocTables pins that a doc entry in this origin's head is
// not proposed for removal as a row the file no longer holds.
func TestReconcileIgnoresDocTables(t *testing.T) {
	doc := newFakeDoc("config")
	registerDoc(t, doc)
	doc.items["policy"] = json.RawMessage(`{"a":1}`)
	d, path := exporterFile(t, "m1")
	r := &recording{MemTransport: NewMemTransport("m1")}
	dirty(t, path)
	if _, err := docExporter(d, r, keyA).Export(); err != nil {
		t.Fatalf("export: %v", err)
	}
	head, err := r.Head("m1")
	if err != nil || len(head) != 1 || !IsDocTable(head[0].Table) {
		t.Fatalf("head = %v, %v; want the one doc entry", head, err)
	}

	rec := &recording{MemTransport: r.MemTransport}
	rc := reconciler(d, rec)
	rc.SetKey(keyA)
	got, err := rc.Reconcile()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got.Deletes != 0 || len(rec.appended()) != 0 {
		t.Fatalf("reconcile = %+v appended %v; want nothing for a doc entry", got, rec.shape())
	}
}
