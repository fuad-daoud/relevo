package delivery

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// baseTime is the instant the routing tests' fixed clocks report.
var baseTime = time.Unix(1757000000, 0).UTC()

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
func testSecrets(t *testing.T) SecretStore { return db.SecretStore{DB: testSecretDB(t)} }

// testClaims returns a KVClaims over a fresh temp database and the database it
// writes to. A claim's fake pid must not depend on which pids exist on the test
// machine, so the store treats every pid as alive.
func testClaims(t *testing.T) (*KVClaims, *db.DB) {
	t.Helper()
	d := testSecretDB(t)
	return &KVClaims{KV: db.TxKV{DB: d}, Alive: alwaysAlive}, d
}
