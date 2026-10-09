package serve

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The TLS certificate is this machine's own: after the split it lives in the
// machine-local file, and a serve admin that keeps reading the shared handle
// reports the certificate missing on a machine that has one.

// TestTheTLSSecretStoreReadsTheMachineLocalFile pins the routing on a split
// pair: the store built over the shared handle reads and writes the local file,
// and the shared file answers with nothing.
func TestTheTLSSecretStoreReadsTheMachineLocalFile(t *testing.T) {
	shared, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: "01ORIGIN"})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })

	const secret, value = "serve.tls.cert", "local-only-cert"
	secrets := SecretStore{DB: shared}

	if err := secrets.SecretPut(secret, []byte(value), time.Now().UTC()); err != nil {
		t.Fatalf("SecretPut: %v", err)
	}
	if got, ok, err := shared.SecretGet(secret); err != nil {
		t.Fatalf("SecretGet on the shared handle: %v", err)
	} else if ok {
		t.Errorf("a certificate written through the store is readable on the shared file: %q", got)
	}
	got, ok, err := secrets.SecretGet(secret)
	if err != nil || !ok || string(got) != value {
		t.Errorf("read back through the store = %q (present %t, err %v), want %q", got, ok, err, value)
	}
	local, ok, err := shared.Local().SecretGet(secret)
	if err != nil || !ok || string(local) != value {
		t.Errorf("the local file's certificate = %q (present %t, err %v), want the stored value", local, ok, err)
	}
}

// TestFingerprintReadsTheMachineLocalFile pins the surface the serve admin
// verb actually calls: a fingerprint over a split pair is computed from the
// local file's certificate, and reports the absent one when that file has none.
func TestFingerprintReadsTheMachineLocalFile(t *testing.T) {
	shared, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: "01ORIGIN"})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })

	if _, err := Fingerprint(SecretStore{DB: shared}); !errors.Is(err, ErrNoCertificate) {
		t.Fatalf("Fingerprint over an empty pair = %v, want ErrNoCertificate", err)
	}

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	want, err := InitTLS(SecretStore{DB: shared}, []string{"127.0.0.1"}, now)
	if err != nil {
		t.Fatalf("InitTLS: %v", err)
	}

	got, err := Fingerprint(SecretStore{DB: shared})
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if got != want {
		t.Errorf("Fingerprint = %q, want the local file's %q", got, want)
	}
	if _, ok, err := shared.SecretGet(tlsCertSecret); err != nil {
		t.Fatalf("SecretGet on the shared handle: %v", err)
	} else if ok {
		t.Error("the certificate InitTLS wrote is readable on the shared file")
	}
	if loaded, err := LoadTLS(SecretStore{DB: shared}); err != nil {
		t.Fatalf("LoadTLS: %v", err)
	} else if len(loaded.Certificate) == 0 {
		t.Error("LoadTLS returned no certificate from the local file")
	}
}
