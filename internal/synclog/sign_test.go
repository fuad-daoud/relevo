package synclog

import (
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

var (
	keyA = []byte("0123456789abcdef0123456789abcdef")
	keyB = []byte("fedcba9876543210fedcba9876543210")
)

// signedExport exports d's outbox into log with key; a nil key is a locked
// machine.
func signedExport(t *testing.T, d *db.DB, log *MemTransport, key []byte) {
	t.Helper()
	e := NewExporter(d, log.OnLog(d.Origin()))
	e.SetKey(key)
	if _, err := e.Export(); err != nil {
		t.Fatalf("export from %s: %v", d.Origin(), err)
	}
}

func importWith(d *db.DB, log *MemTransport, key []byte) (ImportResult, error) {
	imp := NewImporter(d, log.OnLog(d.Origin()))
	imp.SetKey(key)
	return imp.Import()
}

func TestExportSignsEveryEntryWhenUnlocked(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertBinding("01A", "m1"), insertBinding("01B", "m1"), `DELETE FROM binding WHERE id = '01B'`)
	r := &recording{MemTransport: NewMemTransport("m1")}
	e := exporter(d, r)
	e.SetKey(keyA)
	if _, err := e.Export(); err != nil {
		t.Fatalf("export: %v", err)
	}
	got := r.appended()
	if len(got) == 0 {
		t.Fatal("nothing was appended")
	}
	for _, entry := range got {
		if !VerifyMAC(keyA, entry) {
			t.Errorf("%s %s %s is not signed with the key", entry.Table, entry.PK, entry.Op)
		}
	}
}

func TestExportLeavesEntriesUnsignedWhenLocked(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertBinding("01A", "m1"))
	r := &recording{MemTransport: NewMemTransport("m1")}
	if _, err := exporter(d, r).Export(); err != nil {
		t.Fatalf("export: %v", err)
	}
	for _, entry := range r.appended() {
		if len(entry.MAC) != 0 {
			t.Errorf("%s %s carries a MAC on a locked machine", entry.Table, entry.PK)
		}
	}
}

func TestExportSignsTheRefNotTheBody(t *testing.T) {
	t.Parallel()
	p := newBlobPair()
	a, aPath := exporterFile(t, "A")
	seedBodies(t, aPath)
	e := NewExporter(a, p.link(t, "A"))
	e.SetKey(keyA)
	if _, err := e.Export(); err != nil {
		t.Fatalf("export: %v", err)
	}
	pulled, err := p.log.OnLog("C").Pull(nil)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	refs := 0
	for _, entry := range pulled {
		if !VerifyMAC(keyA, entry) {
			t.Errorf("%s %s does not verify as stored", entry.Table, entry.PK)
		}
		if strings.Contains(string(entry.Body), blobTag) {
			refs++
		}
	}
	if refs == 0 {
		t.Fatal("no stored entry names an object, so the test pins nothing")
	}
}

func TestReconcileReemitsSignedWhenUnlocked(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertBinding("01A", "m1"))
	log := NewMemTransport("m1")
	r := reconciler(d, log)
	r.SetKey(keyA)
	if _, err := r.ReconcileBatch(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	pulled, err := log.OnLog("m2").Pull(nil)
	if err != nil || len(pulled) == 0 {
		t.Fatalf("pull = %d entries, %v", len(pulled), err)
	}
	for _, entry := range pulled {
		if !VerifyMAC(keyA, entry) {
			t.Errorf("%s %s was re-emitted unsigned", entry.Table, entry.PK)
		}
	}
}

func TestExportRefusesWhenLockedAndOwnOriginIsSigned(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	log := NewMemTransport("m1")
	seed(t, path, insertBinding("01A", "m1"))
	signedExport(t, d, log, keyA)
	seed(t, path, insertBinding("01B", "m1"))

	_, err := exporter(d, &recording{MemTransport: log}).Export()
	if !errors.Is(err, ErrLockedSignedOrigin) {
		t.Fatalf("locked export = %v, want ErrLockedSignedOrigin", err)
	}
	if !strings.Contains(err.Error(), "this machine's log is signed; run relevo db sync unlock") {
		t.Errorf("error %q lacks the instruction", err)
	}
	if got := outboxRows(t, d); got == "" {
		t.Error("the outbox was truncated by a refused export")
	}
	pulled, _ := log.OnLog("m2").Pull(nil)
	if len(pulled) != 1 {
		t.Errorf("the log holds %d entries, want only the signed one", len(pulled))
	}
}

type marksDown struct{ *MemTransport }

func (marksDown) ReadMarks() (MarkState, error) { return MarkState{}, errors.New("marks are down") }

func TestLockedExportFailsWhenTheMarksCannotBeRead(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertBinding("01A", "m1"))
	_, err := exporter(d, &recording{MemTransport: NewMemTransport("m1")}).Export()
	if err != nil {
		t.Fatalf("baseline export: %v", err)
	}
	seed(t, path, insertBinding("01B", "m1"))
	e := NewExporter(d, marksDown{NewMemTransport("m1")})
	if _, err := e.Export(); err == nil || errors.Is(err, ErrLockedSignedOrigin) {
		t.Fatalf("export = %v, want the read failure", err)
	}
	if outboxRows(t, d) == "" {
		t.Error("the outbox was truncated")
	}
}

func TestReconcileRefusesWhenLockedAndOwnOriginIsSigned(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	log := NewMemTransport("m1")
	seed(t, path, insertBinding("01A", "m1"))
	signedExport(t, d, log, keyA)
	seed(t, path, insertBinding("01B", "m1"), `DELETE FROM sync_outbox`)

	_, err := reconciler(d, log).ReconcileBatch()
	if !errors.Is(err, ErrLockedSignedOrigin) {
		t.Fatalf("reconcile = %v, want ErrLockedSignedOrigin", err)
	}
}
