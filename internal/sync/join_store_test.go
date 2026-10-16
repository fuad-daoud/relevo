package sync

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// holdR2Untyped stores a complete bucket set with no write stamps of its own,
// the shape a document's applied values leave behind.
func holdR2Untyped(t *testing.T, local Local) {
	t.Helper()
	if err := local.Tx(func(tx *db.Tx) error {
		for _, s := range []struct{ name, value string }{
			{SecretR2Endpoint, r2Fixture.Endpoint},
			{SecretR2Bucket, r2Fixture.Bucket},
			{SecretR2KeyID, r2Fixture.KeyID},
			{SecretR2Secret, r2Fixture.Secret},
		} {
			if err := tx.SecretPut(s.name, []byte(s.value), time.Unix(0, 0).UTC()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("hold the bucket set: %v", err)
	}
}

// TestStoreLeavesCredentialsNoEnableTyped pins the store's boundary on what a
// join owes the fleet: a plan whose bucket set reads the same as the stored
// one carries nothing a person typed into this enable, so it is not written,
// stamped or signalled -- a stamp would answer a peer's document with this
// machine's own edit.
func TestStoreLeavesCredentialsNoEnableTyped(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	holdR2Untyped(t, local)
	e := &Enabler{Request: EnableRequest{Local: local, Shared: shared}}
	if err := e.store(EnablePlan{Remote: joinRemote, R2: r2Fixture}, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("store: %v", err)
	}
	stampsTake(t, shared, local, false)
	signalTake(t, local, false)
}

// TestStoreWritesCredentialsTheEnableChanged pins the other half: a plan the
// caller changed points this machine at another bucket, and that write is a
// person's edit -- written, stamped and signalled -- just as a plain enable's
// is.
func TestStoreWritesCredentialsTheEnableChanged(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	holdR2Untyped(t, local)
	changed := r2Fixture
	changed.Bucket = "another-bucket"
	e := &Enabler{Request: EnableRequest{Local: local, Shared: shared}}
	if err := e.store(EnablePlan{Remote: joinRemote, R2: changed}, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("store: %v", err)
	}
	stampsTake(t, shared, local, true)
	signalTake(t, local, true)
}

// stampsTake asserts the four bucket-credential write stamps exist or not.
func stampsTake(t *testing.T, shared *db.DB, local Local, want bool) {
	t.Helper()
	for _, name := range []string{SecretR2Endpoint, SecretR2Bucket, SecretR2KeyID, SecretR2Secret} {
		_, ok, err := local.ConfigWriteStamp(SecretStampKey(name))
		if err != nil {
			t.Fatalf("write stamp %s: %v", name, err)
		}
		if ok != want {
			t.Errorf("write stamp of %s = %v, want %v", name, ok, want)
		}
	}
}

// signalTake asserts the secrets document is (or is not) signalled.
func signalTake(t *testing.T, local Local, want bool) {
	t.Helper()
	signals, err := local.DocSignals()
	if err != nil {
		t.Fatalf("doc signals: %v", err)
	}
	got := map[string]bool{}
	for _, s := range signals {
		got[s.Doc] = true
	}
	if got[SecretsDocName] != want {
		t.Errorf("the %s document signalled = %v, want %v", SecretsDocName, got[SecretsDocName], want)
	}
}
