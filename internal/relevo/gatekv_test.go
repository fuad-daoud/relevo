package relevo

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/mastermind"
)

// testGateKV returns a real t.TempDir() database for a Runtime's Gates field.
func testGateKV(t *testing.T) db.KV {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// testGates returns a Gates handle and the directory the legacy ledger.json,
// availability.json and history.json are imported from: the kv row and its
// legacy path can then be exercised together.
func testGates(t *testing.T) db.KV {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// testHistoryDB returns the shared file a Runtime's History field binds: the
// one the availability_event and latency_sample rows live in, rather than the
// machine-local file the ledger is a kv row of. It is a second file beside the
// gates one on purpose, because it is a second file in production.
func testHistoryDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.OpenWith(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: "01ORIGIN"})
	if err != nil {
		t.Fatalf("db.OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// testSecretDB returns a real t.TempDir() database, the machine database the
// secret store and the run log live in.
func testSecretDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// testSecrets returns the machine database's secret store.
func testSecrets(t *testing.T) delivery.SecretStore { return db.SecretStore{DB: testSecretDB(t)} }

// testMasterMinds returns a mastermind registry over a fresh temp database.
func testMasterMinds(t *testing.T) *mastermind.DBRegistry {
	t.Helper()
	return &mastermind.DBRegistry{
		KV:  db.TxKV{DB: testSecretDB(t)},
		Now: time.Now,
	}
}

// badJSONKV returns invalid bytes for every key, so a reader hits the decode
// failure corrupt stored bytes can cause.
type badJSONKV struct{}

func (badJSONKV) KVGet(string) ([]byte, bool, error) { return []byte("not json"), true, nil }
func (badJSONKV) KVPut(string, []byte) error         { return nil }
func (badJSONKV) KVDelete(string) error              { return nil }

// failPutKV delegates to an inner KV but fails KVPut for one key, so one
// record's write can be made to fail while another's succeeds.
type failPutKV struct {
	inner db.KV
	key   string
}

func (k failPutKV) KVGet(key string) ([]byte, bool, error) { return k.inner.KVGet(key) }
func (k failPutKV) KVPut(key string, v []byte) error {
	if key == k.key {
		return errors.New("put failed")
	}
	return k.inner.KVPut(key, v)
}
func (k failPutKV) KVDelete(key string) error { return k.inner.KVDelete(key) }

// loadLedger reads a runtime's ledger for assertions.
func loadLedger(t *testing.T, rt Runtime) availability.Ledger {
	t.Helper()
	l, err := availability.LoadLedger(rt.Gates)
	if err != nil {
		t.Fatalf("LoadKV ledger: %v", err)
	}
	return l
}
