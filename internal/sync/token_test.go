package sync

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// tokenFixture is the value a test hands the token verbs and then hunts for
// across every surface the value could leak through.
const tokenFixture = "eyJhbGciOiJIUzI1NiJ9.sync-token-fixture.signature"

var tokenNow = time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)

// openSplit opens a shared database and the local file beside it, the pair
// every machine-local row is written through.
func openSplit(t *testing.T) (sharedPath string, shared, local Local) {
	t.Helper()
	sharedPath = filepath.Join(t.TempDir(), "relevo.db")
	shared, err := db.OpenSplit(sharedPath, db.Options{})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local, err = LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	return sharedPath, shared, local
}

// TestTokenNeverLeavesMachine pins the blast radius of one token: it is stored
// in the local file and is in no byte of the shared file, which is the file
// that syncs. The local file is read too, so the shared-file assertion cannot
// pass because the value was never written anywhere.
func TestTokenNeverLeavesMachine(t *testing.T) {
	t.Parallel()

	sharedPath, shared, local := openSplit(t)

	if err := SetToken(local, []byte(tokenFixture), tokenNow); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	value, ok, err := ReadToken(local)
	if err != nil {
		t.Fatalf("ReadToken: %v", err)
	}
	if !ok || string(value) != tokenFixture {
		t.Fatalf("ReadToken = %q, %v; want the stored token", value, ok)
	}
	if err := DeleteToken(local); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}
	if _, ok, err := ReadToken(local); err != nil {
		t.Fatalf("ReadToken after delete: %v", err)
	} else if ok {
		t.Fatal("ReadToken after delete returned a value")
	}
	if err := DeleteToken(local); err != nil {
		t.Fatalf("DeleteToken on an absent token: %v", err)
	}
	if err := SetToken(local, []byte(tokenFixture), tokenNow); err != nil {
		t.Fatalf("SetToken: %v", err)
	}

	localPath := db.SplitPath(sharedPath)
	if err := shared.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	sharedBytes, err := os.ReadFile(sharedPath)
	if err != nil {
		t.Fatalf("read the shared file: %v", err)
	}
	if bytes.Contains(sharedBytes, []byte(tokenFixture)) {
		t.Error("the token value is in the shared file")
	}
	localBytes, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read the local file: %v", err)
	}
	if !bytes.Contains(localBytes, []byte(tokenFixture)) {
		t.Error("the token value is not in the local file, so the shared-file check is vacuous")
	}
}

// TestTokenReachesNoClientCall pins the third surface the value could cross: a
// client is handed a context and nothing else, so a token has no argument to
// travel in, and driving a whole tick's calls leaves no trace of one in what
// the client recorded.
func TestTokenReachesNoClientCall(t *testing.T) {
	t.Parallel()

	_, _, local := openSplit(t)
	if err := SetToken(local, []byte(tokenFixture), tokenNow); err != nil {
		t.Fatalf("SetToken: %v", err)
	}

	f := &Fake{}
	var client SyncClient = f
	ctx := t.Context()
	if err := client.Push(ctx); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if _, err := client.Pull(ctx); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if _, err := client.Stats(ctx); err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if err := client.Checkpoint(ctx); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	for _, name := range f.Calls {
		if strings.Contains(name, tokenFixture) {
			t.Errorf("a recorded client call carries the token value: %q", name)
		}
	}
}

// TestTokenNeverReachesAnError pins the other half of the blast radius: a store
// that refuses a write says which secret it refused and never hands the value
// back, because every refusal here ends up in a log line.
func TestTokenNeverReachesAnError(t *testing.T) {
	t.Parallel()

	_, shared, local := openSplit(t)

	refusals := []error{
		SetToken(local, []byte("   "), tokenNow),
		SetToken(local, nil, tokenNow),
	}
	if err := shared.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	refusals = append(refusals,
		SetToken(local, []byte(tokenFixture), tokenNow),
		DeleteToken(local),
	)
	if _, _, err := ReadToken(local); err == nil {
		t.Error("ReadToken on a closed file returned no error")
	} else {
		refusals = append(refusals, err)
	}

	for _, err := range refusals {
		if err == nil {
			t.Fatal("a refused write returned no error")
		}
		if bytes.Contains([]byte(err.Error()), []byte(tokenFixture)) {
			t.Errorf("an error carries the token value: %v", err)
		}
	}

	// A refusal from inside the transaction is the one that has the value in
	// hand when it fails, so it is the one a leak would come out of.
	sharedPath, _, local := openSplit(t)
	if err := dropSecretTable(t, db.SplitPath(sharedPath)); err != nil {
		t.Fatalf("drop the secret table: %v", err)
	}
	inside := []error{
		SetToken(local, []byte(tokenFixture), tokenNow),
		DeleteToken(local),
	}
	for _, err := range inside {
		if err == nil {
			t.Fatal("a write to a file with no secret table returned no error")
		}
		if bytes.Contains([]byte(err.Error()), []byte(tokenFixture)) {
			t.Errorf("an error from inside the transaction carries the token value: %v", err)
		}
	}
}

// dropSecretTable removes the table the token lives in, so a write fails with
// the value already in hand.
func dropSecretTable(t *testing.T, path string) error {
	t.Helper()
	pool, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("db.OpenRaw: %v", err)
	}
	defer func() { _ = pool.Close() }()
	_, err = pool.Exec(`DROP TABLE secret`)
	return err
}
