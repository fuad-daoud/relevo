package synclog

import (
	"strings"
	"testing"
)

// signedLog is m1 writing 01A unsigned (seq 1) and then 01B signed (seq 2).
func signedLog(t *testing.T) (*MemTransport, string) {
	t.Helper()
	m1, path := exporterFile(t, "m1")
	log := NewMemTransport("m1")
	seed(t, path, insertBinding("01A", "m1"))
	signedExport(t, m1, log, nil)
	seed(t, path, insertBinding("01B", "m1"))
	signedExport(t, m1, log, keyA)
	return log, path
}

// stallOf is the reason an import held origin, or "" when it did not.
func stallOf(res ImportResult, origin string) string {
	for _, s := range res.Stalled {
		if s.Origin == origin {
			return s.Reason
		}
	}
	return ""
}

// forgeMAC flips the stored MAC of the entry at seq, as a remote writer would.
func forgeMAC(log *MemTransport, seq int) {
	log.log.mu.Lock()
	defer log.log.mu.Unlock()
	for n := range log.log.entries {
		if log.log.entries[n].Seq == seq {
			mac := append([]byte(nil), log.log.entries[n].MAC...)
			mac[0] ^= 0xff
			log.log.entries[n].MAC = mac
		}
	}
}

func appendUnsigned(t *testing.T, log *MemTransport, pk string) {
	t.Helper()
	e := syncEntry(t, "m1", "binding", pk, map[string]any{"id": "x"})
	e.SchemaVersion = 1
	if _, err := log.OnLog("m1").Append([]Entry{e}); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func TestImportRefusesABadMACAndHoldsTheOrigin(t *testing.T) {
	t.Parallel()
	m1, path := exporterFile(t, "m1")
	log := NewMemTransport("m1")
	seed(t, path, insertBinding("01A", "m1"))
	signedExport(t, m1, log, keyA)
	forgeMAC(log, 1)
	m2, _ := peerFile(t, "m2")

	res, err := importWith(m2, log, keyA)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if reason := stallOf(res, "m1"); !strings.Contains(reason, `entry 1 from "m1" has a bad signature`) {
		t.Errorf("stall %q lacks the sentence", reason)
	}
	if got := sharedRows(t, m2); got != "" {
		t.Errorf("m2 holds %q after a refusal", got)
	}
	if markOf(t, m2, "m1") != 0 {
		t.Error("the mark moved over a refused entry")
	}
}

func TestImportRefusesUnsignedAtOrAboveSignedFrom(t *testing.T) {
	t.Parallel()
	log, _ := signedLog(t)
	appendUnsigned(t, log, `["01C"]`)
	m2, _ := peerFile(t, "m2")

	res, err := importWith(m2, log, keyA)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if reason := stallOf(res, "m1"); !strings.Contains(reason, `entry 3 from "m1" is unsigned but "m1" signs from 2`) {
		t.Errorf("stall %q lacks the sentence", reason)
	}
	if got := markOf(t, m2, "m1"); got != 2 {
		t.Errorf("mark = %d, want the two entries before the refusal applied", got)
	}
}

func TestImportAppliesUnsignedBelowSignedFrom(t *testing.T) {
	t.Parallel()
	log, _ := signedLog(t)
	m2, _ := peerFile(t, "m2")
	if _, err := importWith(m2, log, keyA); err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := sharedRows(t, m2); got != `binding ["01A"]|binding ["01B"]` {
		t.Errorf("m2 holds %q, want both rows", got)
	}
}

func TestImportAppliesUnsignedFromAnOriginThatNeverSigned(t *testing.T) {
	t.Parallel()
	m1, path := exporterFile(t, "m1")
	log := NewMemTransport("m1")
	seed(t, path, insertBinding("01A", "m1"))
	signedExport(t, m1, log, nil)
	m2, _ := peerFile(t, "m2")
	if _, err := importWith(m2, log, keyA); err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := sharedRows(t, m2); got != `binding ["01A"]` {
		t.Errorf("m2 holds %q", got)
	}
	if recs, _ := m2.SignedFroms(); len(recs) != 0 {
		t.Errorf("records = %v, want none", recs)
	}
}

func TestImportRecordsLocalSignedFromOnTheFirstVerifiedEntry(t *testing.T) {
	t.Parallel()
	log, _ := signedLog(t)
	m2, _ := peerFile(t, "m2")
	if _, err := importWith(m2, log, keyA); err != nil {
		t.Fatalf("import: %v", err)
	}
	if recs, _ := m2.SignedFroms(); recs["m1"] != 2 {
		t.Errorf("records = %v, want m1 at 2", recs)
	}
}

func TestSignedFromTakesTheLowerOfRemoteAndLocal(t *testing.T) {
	t.Parallel()
	log, _ := signedLog(t) // the remote says m1 signs from 2
	m2, _ := peerFile(t, "m2")
	if err := m2.LowerSignedFrom("m1", 1); err != nil {
		t.Fatalf("seed the record: %v", err)
	}
	res, err := importWith(m2, log, keyA)
	if err != nil || !strings.Contains(stallOf(res, "m1"), `"m1" signs from 1`) {
		t.Fatalf("import = %+v, %v; want the unsigned entry 1 refused under the local record", res, err)
	}
	if err := m2.LowerSignedFrom("m1", 5); err != nil {
		t.Fatal(err)
	}
	if recs, _ := m2.SignedFroms(); recs["m1"] != 1 {
		t.Errorf("record = %v, a record must never be raised", recs)
	}
}

func TestARemoteThatDropsSignedFromCannotSwitchCheckingOff(t *testing.T) {
	t.Parallel()
	log, _ := signedLog(t)
	m2, _ := peerFile(t, "m2")
	if _, err := importWith(m2, log, keyA); err != nil {
		t.Fatalf("first import: %v", err)
	}
	log.log.mu.Lock()
	log.log.signedFrom = nil
	log.log.signedTag = nil
	log.log.mu.Unlock()
	appendUnsigned(t, log, `["01C"]`)

	res, err := importWith(m2, log, keyA)
	if err != nil || stallOf(res, "m1") == "" {
		t.Fatalf("import = %+v, %v; want the unsigned entry refused", res, err)
	}
}

func TestLockedReaderImportsWithoutVerifying(t *testing.T) {
	t.Parallel()
	m1, path := exporterFile(t, "m1")
	log := NewMemTransport("m1")
	seed(t, path, insertBinding("01A", "m1"))
	signedExport(t, m1, log, keyA)
	m2, _ := peerFile(t, "m2")
	if _, err := importWith(m2, log, nil); err != nil {
		t.Fatalf("locked import: %v", err)
	}
	if got := sharedRows(t, m2); got != `binding ["01A"]` {
		t.Errorf("m2 holds %q", got)
	}
	if recs, _ := m2.SignedFroms(); len(recs) != 0 {
		t.Errorf("a locked reader wrote records %v", recs)
	}
}

func TestSnapshotRegionRefusesABadMAC(t *testing.T) {
	t.Parallel()
	m1, path := exporterFile(t, "m1")
	log := NewMemTransport("m1")
	seed(t, path, insertBinding("01P", "m1"))
	signedExport(t, m1, log, keyA)
	seed(t, path, `UPDATE binding SET cwd = '/y' WHERE id = '01P'`)
	signedExport(t, m1, log, keyA)
	compactTo(t, log, "m1", 2, 1)
	m2, _ := peerFile(t, "m2")

	res, err := importWith(m2, log, keyB)
	if err != nil || stallOf(res, "m1") == "" {
		t.Fatalf("snapshot import = %+v, %v; want the region refused", res, err)
	}
	if got := sharedRows(t, m2); got != "" {
		t.Errorf("m2 holds %q after a refused region", got)
	}
}

func TestCompactionKeepsSurvivorMACs(t *testing.T) {
	t.Parallel()
	signed := syncEntry(t, "m1", "binding", `["a"]`, map[string]any{"id": "a"})
	signed.Seq = 1
	later := signed
	later.Seq = 2
	later.MAC = EntryMAC(keyA, later)
	signed.MAC = EntryMAC(keyA, signed)
	plain := signed
	plain.MAC = nil
	plainLater := later
	plainLater.MAC = nil
	withMACs := compactDrops([]Entry{signed, later}, 2)
	without := compactDrops([]Entry{plain, plainLater}, 2)
	if len(withMACs) != 1 || !withMACs[1] || len(without) != 1 || !without[1] {
		t.Fatalf("drops = %v and %v, want seq 1 only in both", withMACs, without)
	}

	m1, path := exporterFile(t, "m1")
	log := NewMemTransport("m1")
	seed(t, path, insertBinding("01A", "m1"))
	signedExport(t, m1, log, keyA)
	seed(t, path, `UPDATE binding SET cwd = '/y' WHERE id = '01A'`)
	signedExport(t, m1, log, keyA)
	compactTo(t, log, "m1", 2, 1)
	kept, err := log.OnLog("m2").PullRange("m1", 0, 2)
	if err != nil || len(kept) != 1 || !VerifyMAC(keyA, kept[0]) {
		t.Fatalf("survivors = %+v, %v; want one with its MAC intact", kept, err)
	}
}

func TestVerificationHappensBeforeAnyBodyFetch(t *testing.T) {
	t.Parallel()
	p := newBlobPair()
	a, aPath := exporterFile(t, "A")
	seedBodies(t, aPath)
	e := NewExporter(a, p.link(t, "A"))
	e.SetKey(keyA)
	if _, err := e.Export(); err != nil {
		t.Fatalf("export: %v", err)
	}
	c, _ := exporterFile(t, "C")
	imp := NewImporter(c, p.link(t, "C"))
	imp.SetKey(keyB)
	res, err := imp.Import()
	if err != nil || stallOf(res, "A") == "" {
		t.Fatalf("import = %+v, %v; want origin A held", res, err)
	}
	if p.mover.Gets != 0 {
		t.Errorf("%d bodies were fetched before the refusal", p.mover.Gets)
	}
}
