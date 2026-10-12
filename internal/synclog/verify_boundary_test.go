package synclog

import (
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

func setBoundary(log *MemTransport, origin string, seq int, tag []byte) {
	log.log.mu.Lock()
	defer log.log.mu.Unlock()
	if log.log.signedFrom == nil {
		log.log.signedFrom = map[string]int{}
		log.log.signedTag = map[string][]byte{}
	}
	log.log.signedFrom[origin] = seq
	log.log.signedTag[origin] = tag
}

func TestReaderRefusesARaisedBoundary(t *testing.T) {
	t.Parallel()
	log, _ := signedLog(t) // m1 signs from 2
	setBoundary(log, "m1", 3, BoundaryTag(keyA, "m1", 2))
	m2, _ := peerFile(t, "m2")

	res, err := importWith(m2, log, keyA)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if reason := stallOf(res, "m1"); !strings.Contains(reason, `boundary of "m1"`) {
		t.Errorf("stall %q does not name the boundary", reason)
	}
	if got := sharedRows(t, m2); got != "" {
		t.Errorf("m2 holds %q: a raised boundary let entries through", got)
	}
}

func TestReaderRefusesAForgedBoundary(t *testing.T) {
	t.Parallel()
	for name, tag := range map[string][]byte{
		"another key's tag": BoundaryTag(keyB, "m1", 2),
		"no tag":            nil,
	} {
		log, _ := signedLog(t)
		setBoundary(log, "m1", 2, tag)
		m2, _ := peerFile(t, "m2")
		res, err := importWith(m2, log, keyA)
		if err != nil || stallOf(res, "m1") == "" {
			t.Errorf("%s: import = %+v, %v; want m1 held", name, res, err)
		}
		if got := sharedRows(t, m2); got != "" {
			t.Errorf("%s: m2 holds %q", name, got)
		}
	}
}

func TestDeletingTheBoundaryAfterAReaderSawItChangesNothing(t *testing.T) {
	t.Parallel()
	log, _ := signedLog(t)
	m2, _ := peerFile(t, "m2")
	// The reader is already past the signed entry, so nothing verifies on this
	// run: the boundary alone has to be remembered.
	if err := m2.Tx(func(tx *db.Tx) error { return tx.SetImportMark("m1", 2) }); err != nil {
		t.Fatal(err)
	}
	if _, err := importWith(m2, log, keyA); err != nil {
		t.Fatalf("first import: %v", err)
	}
	if recs, _ := m2.SignedFroms(); recs["m1"] != 2 {
		t.Fatalf("records = %v, want the verified boundary kept at 2", recs)
	}
	log.log.mu.Lock()
	log.log.signedFrom, log.log.signedTag = nil, nil
	log.log.mu.Unlock()
	appendUnsigned(t, log, `["01C"]`)

	res, err := importWith(m2, log, keyA)
	if err != nil || stallOf(res, "m1") == "" {
		t.Fatalf("import = %+v, %v; want the unsigned entry refused", res, err)
	}
}

func TestRecordSignedLowersAnExistingRecord(t *testing.T) {
	t.Parallel()
	log, _ := signedLog(t)
	log.log.mu.Lock()
	log.log.signedFrom, log.log.signedTag = nil, nil
	log.log.mu.Unlock()
	m2, _ := peerFile(t, "m2")
	if err := m2.LowerSignedFrom("m1", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := importWith(m2, log, keyA); err != nil {
		t.Fatalf("import: %v", err)
	}
	if recs, _ := m2.SignedFroms(); recs["m1"] != 2 {
		t.Errorf("records = %v, want the record lowered to the verified entry at 2", recs)
	}
}

func TestMemTransportRefusesAMispredictedBoundary(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("m1")
	appendUnsigned(t, log, `["u"]`)
	e := syncEntry(t, "m1", "binding", `["s"]`, map[string]any{"id": "s"})
	e.SchemaVersion = 1
	e.MAC = EntryMAC(keyA, e)

	_, err := log.AppendSigned([]Entry{e}, Boundary{Seq: 3, Tag: BoundaryTag(keyA, "m1", 3)})
	if !errors.Is(err, ErrBoundary) {
		t.Fatalf("append = %v, want ErrBoundary", err)
	}
	if state, _ := log.ReadMarks(); state.Heads["m1"] != 1 || state.SignedFrom != nil {
		t.Errorf("state = %+v: a refused append wrote", state)
	}
}

func TestWorkerRefusesASignedBatchWithoutABoundaryOnAnUnsignedOrigin(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("m1")
	e := syncEntry(t, "m1", "binding", `["s"]`, map[string]any{"id": "s"})
	e.SchemaVersion = 1
	e.MAC = EntryMAC(keyA, e)
	if _, err := log.Append([]Entry{e}); !errors.Is(err, ErrBoundary) {
		t.Fatalf("append with no boundary = %v, want ErrBoundary", err)
	}
	if state, _ := log.ReadMarks(); state.Heads["m1"] != 0 {
		t.Errorf("heads = %v: a refused append wrote", state.Heads)
	}
}

func TestExportPredictsTheBoundarySeq(t *testing.T) {
	t.Parallel()
	m1, path := exporterFile(t, "m1")
	log := NewMemTransport("m1")
	seed(t, path, insertBinding("01A", "m1"))
	signedExport(t, m1, log, nil)
	seed(t, path, insertBinding("01B", "m1"), insertBinding("01C", "m1"))
	signedExport(t, m1, log, keyA)

	state, err := log.ReadMarks()
	if err != nil {
		t.Fatal(err)
	}
	if state.SignedFrom["m1"] != 2 {
		t.Fatalf("signed_from = %v, want the first signed seq 2", state.SignedFrom)
	}
	if !VerifyBoundary(keyA, "m1", 2, state.SignedTags["m1"]) {
		t.Error("the stored tag does not bind m1 at 2 to the key")
	}
}

func twoOrigins(t *testing.T) *MemTransport {
	t.Helper()
	log := NewMemTransport("m1")
	for _, origin := range []string{"m1", "m3"} {
		d, path := exporterFile(t, origin)
		seed(t, path, insertBinding("01"+strings.ToUpper(origin), origin))
		signedExport(t, d, log, keyA)
	}
	forgeMAC(log, 1)
	return log
}

func TestABadSignatureHoldsOnlyItsOrigin(t *testing.T) {
	t.Parallel()
	log := twoOrigins(t)
	// m1's entry 1 is forged; m3's seq 1 is the same number on another origin.
	log.log.mu.Lock()
	for n := range log.log.entries {
		if log.log.entries[n].Origin == "m3" {
			log.log.entries[n].MAC = EntryMAC(keyA, log.log.entries[n])
		}
	}
	log.log.mu.Unlock()
	m2, _ := peerFile(t, "m2")

	res, err := importWith(m2, log, keyA)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !strings.Contains(stallOf(res, "m1"), "bad signature") {
		t.Errorf("m1 stall = %q, want the bad signature", stallOf(res, "m1"))
	}
	if stallOf(res, "m3") != "" {
		t.Errorf("m3 was held: %q", stallOf(res, "m3"))
	}
	if got := sharedRows(t, m2); got != `binding ["01M3"]` {
		t.Errorf("m2 holds %q, want only the clean origin's row", got)
	}
}

func TestSnapshotBadSignatureHoldsOnlyItsOrigin(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("m1")
	for _, origin := range []string{"m1", "m3"} {
		d, path := exporterFile(t, origin)
		seed(t, path, insertBinding("01P"+origin, origin))
		signedExport(t, d, log, keyA)
		seed(t, path, `UPDATE binding SET cwd = '/y' WHERE id = '01P`+origin+`'`)
		signedExport(t, d, log, keyA)
		compactTo(t, log, origin, 2, 1)
	}
	forgeMAC(log, 2)
	log.log.mu.Lock()
	for n := range log.log.entries {
		if log.log.entries[n].Origin == "m3" {
			log.log.entries[n].MAC = EntryMAC(keyA, log.log.entries[n])
		}
	}
	log.log.mu.Unlock()
	m2, _ := peerFile(t, "m2")

	res, err := importWith(m2, log, keyA)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if stallOf(res, "m1") == "" || stallOf(res, "m3") != "" {
		t.Errorf("stalls = %+v, want only m1 held", res.Stalled)
	}
	if got := sharedRows(t, m2); got != `binding ["01Pm3"]` {
		t.Errorf("m2 holds %q, want only the clean origin's row", got)
	}
}

// marksCounter counts ReadMarks calls and can fail them. It embeds only the
// plain transport so the boundary path is exercised through Append.
type marksCounter struct {
	*MemTransport
	reads int
	fail  error
}

func (m *marksCounter) ReadMarks() (MarkState, error) {
	m.reads++
	if m.fail != nil {
		return MarkState{}, m.fail
	}
	return m.MemTransport.ReadMarks()
}

func TestExportReadsTheBoundaryOnlyUntilItExists(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertBinding("01A", "m1"))
	counted := &marksCounter{MemTransport: NewMemTransport("m1")}
	e := NewExporter(d, counted)
	e.SetKey(keyA)
	if _, err := e.Export(); err != nil {
		t.Fatalf("export: %v", err)
	}
	first := counted.reads
	seed(t, path, insertBinding("01B", "m1"))
	if _, err := e.Export(); err != nil {
		t.Fatalf("second export: %v", err)
	}
	if first == 0 || counted.reads != first {
		t.Errorf("ReadMarks ran %d times then %d: the boundary should be read once", first, counted.reads)
	}
}

func TestExportFailsWhenTheBoundaryCannotBeRead(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertBinding("01A", "m1"))
	counted := &marksCounter{MemTransport: NewMemTransport("m1"), fail: errors.New("marks unreadable")}
	e := NewExporter(d, counted)
	e.SetKey(keyA)
	if _, err := e.Export(); err == nil {
		t.Fatal("export went on without reading the boundary")
	}
	if has, _ := d.HasOutboxEntries(); !has {
		t.Error("the outbox was cleared")
	}
}

func TestExportAppendsPlainlyWhenTheTransportTakesNoBoundary(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertBinding("01A", "m1"))
	plain := &plainOnly{LogTransport: NewMemTransport("m1")}
	e := NewExporter(d, plain)
	e.SetKey(keyA)
	if _, err := e.Export(); !errors.Is(err, ErrBoundary) {
		t.Fatalf("export = %v, want the log to refuse a signed batch with no boundary", err)
	}
}

// plainOnly hides every method but the interface's own, so the transport takes
// no boundary.
type plainOnly struct{ LogTransport }

func TestMemTransportRefusesABoundaryWithNoTag(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("m1")
	e := syncEntry(t, "m1", "binding", `["s"]`, map[string]any{"id": "s"})
	e.SchemaVersion = 1
	e.MAC = EntryMAC(keyA, e)
	if _, err := log.AppendSigned([]Entry{e}, Boundary{Seq: 1}); !errors.Is(err, ErrBoundary) {
		t.Fatalf("append = %v, want ErrBoundary", err)
	}
}
