package synclog

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestImportFailsWhenTheLocalFileFailsAMidApplyWrite pins that a doc half that
// cannot record what it applied is an error with the mark where it was, so the
// batch is read again.
func TestImportFailsWhenTheLocalFileFailsAMidApplyWrite(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		fault  string
		want   string
		locked bool
	}{
		"applied state unreadable":  {`DROP TABLE shared_applied`, "doc applied", false},
		"applied state unwritable":  {`CREATE TRIGGER f BEFORE INSERT ON shared_applied BEGIN SELECT RAISE(ABORT, 'no applied'); END`, "no applied", false},
		"export state unwritable":   {`CREATE TRIGGER f BEFORE INSERT ON shared_export_state BEGIN SELECT RAISE(ABORT, 'no export'); END`, "no export", false},
		"parked entry unwritable":   {`CREATE TRIGGER f BEFORE INSERT ON shared_pending BEGIN SELECT RAISE(ABORT, 'no park'); END`, "no park", true},
		"clock position unwritable": {`CREATE TRIGGER f BEFORE INSERT ON kv BEGIN SELECT RAISE(ABORT, 'no clock'); END`, "no clock", true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			doc := newTxDoc("config")
			log := NewMemTransport("m1")
			signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
			m2, path := peerFile(t, "m2")
			seed(t, path, c.fault)
			key := keyA
			if c.locked {
				key = nil
			}

			if _, err := docImporter(m2, log, key, doc).Import(); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("import = %v, want the failure %q reported", err, c.want)
			}
			if got := markOf(t, m2, "m1"); got != 0 {
				t.Errorf("mark = %d, want the batch left to be read again", got)
			}
		})
	}
}

// TestImportDropsADocEntryNamingNoKnownDoc pins that an entry for a doc this
// build has no reader for is dropped with the reason, and the batch before it
// is not undone.
func TestImportDropsADocEntryNamingNoKnownDoc(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	ghost := newTxDoc("ghost")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	signedDoc(t, log, ghost, "m1", "policy", 6, `{"a":1}`, keyA)
	m2, _ := peerFile(t, "m2")

	res, err := docImporter(m2, log, keyA, doc).Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if reason := droppedReason(res, "m1"); !strings.Contains(reason, "no shared doc named") {
		t.Errorf("dropped %q, want the unknown doc named", reason)
	}
	if len(doc.revisions) != 1 {
		t.Errorf("revisions %v, want only the known doc's entry applied", doc.revisions)
	}
}

// TestDrainKeepsTheParkedEntriesWhenTheyCannotBeApplied pins that a drain whose
// transaction fails unparks nothing.
func TestDrainKeepsTheParkedEntriesWhenTheyCannotBeApplied(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	m2, path := peerFile(t, "m2")
	parkRaw(t, m2, "m1", 1, marshalEntry(t, pulled(t, log)[0]))
	seed(t, path, `CREATE TRIGGER f BEFORE INSERT ON shared_applied BEGIN SELECT RAISE(ABORT, 'no applied'); END`)

	if _, err := docImporter(m2, log, keyA, doc).DrainPending(); err == nil || !strings.Contains(err.Error(), "no applied") {
		t.Fatalf("DrainPending = %v, want the failure reported", err)
	}
	if got := pendingCount(t, m2); got != 1 {
		t.Errorf("pending = %d, want the entry kept for the next drain", got)
	}
}

// TestExportReportsALocalFileThatFailsItsBookkeeping pins each store failure of
// a doc export as an error that keeps the signal, so the keys are sent again.
func TestExportReportsALocalFileThatFailsItsBookkeeping(t *testing.T) {
	cases := map[string]struct {
		fault string
		want  string
	}{
		"signals unreadable":       {`DROP TABLE shared_outbox`, "export docs"},
		"export state unreadable":  {`DROP TABLE shared_export_state`, "export state"},
		"applied state unwritable": {`CREATE TRIGGER f BEFORE INSERT ON shared_applied BEGIN SELECT RAISE(ABORT, 'no applied'); END`, "no applied"},
		"export state unwritable":  {`CREATE TRIGGER f BEFORE INSERT ON shared_export_state BEGIN SELECT RAISE(ABORT, 'no export'); END`, "no export"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			doc := newFakeDoc("config")
			registerDoc(t, doc)
			doc.items["policy"] = json.RawMessage(`{"a":1}`)
			d, path := exporterFile(t, "m1")
			r := &recording{MemTransport: NewMemTransport("m1")}
			dirty(t, path)
			seed(t, path, c.fault)

			_, err := docExporter(d, r, keyA).Export()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("export = %v, want an error naming %q", err, c.want)
			}
		})
	}
}

// TestImportFailsWhenASupersededItemCannotBeRecorded pins that an item no pass
// could land is an error when its applied state cannot be written, with the
// mark where it was.
func TestImportFailsWhenASupersededItemCannotBeRecorded(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	doc.refuses["never"] = true
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "never", 5, `"n"`, keyA)
	m2, path := peerFile(t, "m2")
	seed(t, path, `CREATE TRIGGER f BEFORE INSERT ON shared_applied BEGIN SELECT RAISE(ABORT, 'no applied'); END`)

	if _, err := docImporter(m2, log, keyA, doc).Import(); err == nil || !strings.Contains(err.Error(), "no applied") {
		t.Fatalf("import = %v, want the failure reported", err)
	}
	if got := markOf(t, m2, "m1"); got != 0 {
		t.Errorf("mark = %d, want the batch left to be read again", got)
	}
}

// TestDrainFailsWhenTheLocalFileFailsAfterVerifying pins that a store failure
// while unparking, or while moving the clock, is an error and keeps what was
// parked.
func TestDrainFailsWhenTheLocalFileFailsAfterVerifying(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		fault string
		want  string
		bad   bool
	}{
		"applied entry cannot be unparked": {`CREATE TRIGGER f BEFORE DELETE ON shared_pending BEGIN SELECT RAISE(ABORT, 'no unpark'); END`, "no unpark", false},
		"refused entry cannot be unparked": {`CREATE TRIGGER f BEFORE DELETE ON shared_pending BEGIN SELECT RAISE(ABORT, 'no unpark'); END`, "no unpark", true},
		"clock cannot move":                {`CREATE TRIGGER f BEFORE INSERT ON kv BEGIN SELECT RAISE(ABORT, 'no clock'); END`, "no clock", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			doc := newTxDoc("config")
			log := NewMemTransport("m1")
			signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
			m2, path := peerFile(t, "m2")
			text := marshalEntry(t, pulled(t, log)[0])
			if c.bad {
				text = "{not json"
			}
			parkRaw(t, m2, "m1", 1, text)
			seed(t, path, c.fault)

			if _, err := docImporter(m2, log, keyA, doc).DrainPending(); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("DrainPending = %v, want the failure %q reported", err, c.want)
			}
			if got := pendingCount(t, m2); got != 1 {
				t.Errorf("pending = %d, want the entry kept", got)
			}
		})
	}
}

func TestRegisterRefusesANilDoc(t *testing.T) {
	t.Parallel()
	if err := RegisterSharedDoc(nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("RegisterSharedDoc(nil) = %v, want ErrInvalid", err)
	}
}
