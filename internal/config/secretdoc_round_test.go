package config

// The secrets document through a real importer and exporter over a log two
// machines share: what one machine applies and what it then answers the fleet
// with. The Table fixture could not pin these, because the rule they pin is
// about the envelope bytes, and only sealing makes those bytes.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// secretMachine is one machine exchanging the secrets document over a log. Its
// key is the fixture's, so machine A's seal is machine B's seal.
type secretMachine struct {
	d      *db.DB
	s      *Store
	origin string
	log    *synclog.MemTransport
}

func newSecretMachine(t *testing.T, origin string, log *synclog.MemTransport) *secretMachine {
	t.Helper()
	d, err := db.OpenWith(t.TempDir()+"/relevo.db", db.Options{Origin: origin})
	if err != nil {
		t.Fatalf("open %s: %v", origin, err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := relevosync.SetKey(d, []byte(secretDocKey), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetKey on %s: %v", origin, err)
	}
	return &secretMachine{d: d, s: Open(d), origin: origin, log: log}
}

// exportSecrets exports whatever the secrets document is signalled to send.
// The machine holds no other documents, so the count and the doc entries on
// the log answer only about secrets.
func (m *secretMachine) exportSecrets(t *testing.T) int {
	t.Helper()
	e := synclog.NewExporter(m.d, m.log.OnLog(m.origin))
	e.SetKey([]byte(secretDocKey))
	e.UseDocs(m.s.SecretsDoc())
	res, err := e.Export()
	if err != nil {
		t.Fatalf("export from %s: %v", m.origin, err)
	}
	return res.Appended
}

// importSecrets applies what the log holds through the real importer, so the
// bookkeeping an apply leaves behind is the export state the next export is
// compared against.
func (m *secretMachine) importSecrets(t *testing.T) {
	t.Helper()
	imp := synclog.NewImporter(m.d, m.log.OnLog(m.origin))
	imp.SetKey([]byte(secretDocKey))
	imp.UseDocs(m.s.SecretsDoc())
	if _, err := imp.Import(); err != nil {
		t.Fatalf("import into %s: %v", m.origin, err)
	}
}

// sentSecrets is the doc-entry keys the log holds for this machine's origin.
func (m *secretMachine) sentSecrets(t *testing.T, other string) []string {
	t.Helper()
	entries, err := m.log.OnLog(other).Pull(nil)
	if err != nil {
		t.Fatalf("pull %s's entries off the log: %v", m.origin, err)
	}
	var out []string
	for _, e := range entries {
		if e.Origin != m.origin {
			continue
		}
		name, ok := synclog.DocName(e.Table)
		if !ok || name != Secrets {
			continue
		}
		// The primary key of a doc entry is the item key as a one-element
		// array, which is what makes it readable without the document itself.
		var pk []string
		if err := json.Unmarshal([]byte(e.PK), &pk); err != nil || len(pk) != 1 {
			t.Fatalf("read %s's %s entry key: %v", m.origin, e.Table, err)
		}
		out = append(out, pk[0])
	}
	return out
}

// TestAppliedSecretsEntriesExportNothing pins what the digest half of an apply
// is for: a machine that applied a peer's entries, whose document is signalled
// afterwards for an unrelated reason, exports nothing back. The envelope its
// document re-seals is the one the apply recorded, so the export state reads
// the same digest for every key.
func TestAppliedSecretsEntriesExportNothing(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("a")
	a := newSecretMachine(t, "a", log)
	b := newSecretMachine(t, "b", log)

	if err := a.s.PutSecret(SecretTypesafe, canarySecret); err != nil {
		t.Fatalf("PutSecret on A: %v", err)
	}
	if err := a.s.PutSecret(relevosync.SecretR2Bucket, []byte("bucket-one")); err != nil {
		t.Fatalf("PutSecret on A: %v", err)
	}
	a.exportSecrets(t)
	b.importSecrets(t)
	if err := b.d.SignalDoc(Secrets); err != nil {
		t.Fatalf("signal the secrets document on B: %v", err)
	}

	if n := b.exportSecrets(t); n != 0 {
		t.Errorf("B sent %d entries after applying the fleet's document, want none", n)
	}
	if got := b.sentSecrets(t, "a"); len(got) != 0 {
		t.Errorf("B's export put %v on the log, want no secrets entries at all", got)
	}
}

// TestAnUnrelatedStampedWriteDoesNotReExportTheWaitingValue pins the
// lost-update shape: B holds an applied typesafe it has not pulled a newer
// one over, and an unrelated stamped write on its side signals its document.
// The export carries that one name and not typesafe, because re-exporting the
// applied value under this machine's stamp would beat the newer entry in the
// wings and spread a stale value through the fleet as an edit.
func TestAnUnrelatedStampedWriteDoesNotReExportTheWaitingValue(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("a")
	a := newSecretMachine(t, "a", log)
	b := newSecretMachine(t, "b", log)

	if err := a.s.PutSecret(SecretTypesafe, canarySecret); err != nil {
		t.Fatalf("PutSecret on A: %v", err)
	}
	a.exportSecrets(t)
	b.importSecrets(t)

	if err := b.s.PutSecret(relevosync.SecretR2Bucket, []byte("bucket-b")); err != nil {
		t.Fatalf("PutSecret on B: %v", err)
	}
	if n := b.exportSecrets(t); n != 1 {
		t.Errorf("B sent %d entries after its own stamped write, want exactly the one", n)
	}
	got := b.sentSecrets(t, "a")
	if len(got) != 1 || got[0] != relevosync.SecretR2Bucket {
		t.Errorf("B's export named %v, want exactly %s and not typesafe", got, relevosync.SecretR2Bucket)
	}
}
