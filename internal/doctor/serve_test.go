package doctor

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

func makeTestCertPEM(t *testing.T, notBefore, notAfter time.Time) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "relevo serve",
		},
		NotBefore: notBefore,
		NotAfter:  notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	block := &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: der,
	}
	return string(pem.EncodeToMemory(block))
}

// serveTestDB opens a fresh machine database for the serve checks. keyOK sets
// the serve.tls.key secret, which is what turns the checks on; certPEM seeds
// serve.tls.cert; clients seeds the serve.clients row.
func serveTestDB(t *testing.T, keyOK bool, certPEM, clients string) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if err := d.Tx(func(tx *db.Tx) error {
		if keyOK {
			if err := tx.SecretPut("serve.tls.key", []byte("test-key"), time.Now().UTC()); err != nil {
				return err
			}
		}
		if certPEM != "" {
			if err := tx.SecretPut("serve.tls.cert", []byte(certPEM), time.Now().UTC()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed secrets: %v", err)
	}
	if clients != "" {
		if err := d.KVPut("serve.clients", []byte(clients)); err != nil {
			t.Fatalf("seed clients: %v", err)
		}
	}
	return d
}

func TestServeChecksNoServerKey(t *testing.T) {
	env := &fakeEnv{
		existingFiles: map[string]bool{},
		fileContents:  map[string]string{},
	}
	now := time.Now()
	checks := ServeChecks(env, serveTestDB(t, false, "", ""), "/fake/serve", now)
	if len(checks) != 0 {
		t.Fatalf("ServeChecks without the serve.tls.key secret returned %d checks, want 0", len(checks))
	}
}

// TestServeChecksCertificate pins the certificate row's four outcomes: valid,
// expiring within 30 days, expired, and unreadable (bad PEM).
func TestServeChecksCertificate(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	serveRoot := "/fake/serve"
	env := &fakeEnv{existingFiles: map[string]bool{serveRoot: true}}

	tests := []struct {
		name       string
		certPEM    string
		wantSev    Severity
		wantDetail string
	}{
		{name: "valid certificate", certPEM: makeTestCertPEM(t, now.Add(-time.Hour), now.Add(365*24*time.Hour)), wantSev: SevOK, wantDetail: "valid"},
		{name: "expiring within 30d", certPEM: makeTestCertPEM(t, now.Add(-time.Hour), now.Add(15*24*time.Hour)), wantSev: SevWarn, wantDetail: "expires"},
		{name: "expired certificate", certPEM: makeTestCertPEM(t, now.Add(-48*time.Hour), now.Add(-24*time.Hour)), wantSev: SevFail, wantDetail: "expired"},
		{name: "unreadable certificate", certPEM: "not a pem", wantSev: SevFail, wantDetail: "unreadable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checks := ServeChecks(env, serveTestDB(t, true, tc.certPEM, ""), serveRoot, now)
			c := findCheck(Report{Checks: checks}, "serve", "certificate")
			if c == nil {
				t.Fatal("missing serve: certificate check")
			}
			if c.Severity != tc.wantSev {
				t.Errorf("severity = %v, want %v", c.Severity, tc.wantSev)
			}
			if !strings.Contains(c.Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want containing %q", c.Detail, tc.wantDetail)
			}
		})
	}
}

func TestServeChecksClients(t *testing.T) {
	now := time.Now()
	serveRoot := "/fake/serve"

	t.Run("multiple enrolled clients", func(t *testing.T) {
		clientsJSON := `[
			{"id": "id1", "label": "client1"},
			{"id": "id2", "label": "client2"}
		]`
		env := &fakeEnv{
			existingFiles: map[string]bool{serveRoot: true},
		}
		checks := ServeChecks(env, serveTestDB(t, true, "", clientsJSON), serveRoot, now)
		c := findCheck(Report{Checks: checks}, "serve", "clients")
		if c == nil {
			t.Fatal("missing serve: clients check")
		}
		if c.Severity != SevOK {
			t.Errorf("severity = %v, want SevOK", c.Severity)
		}
		if c.Detail != "2 enrolled" {
			t.Errorf("detail = %q, want '2 enrolled'", c.Detail)
		}
	})

	t.Run("none enrolled", func(t *testing.T) {
		env := &fakeEnv{
			existingFiles: map[string]bool{serveRoot: true},
		}
		checks := ServeChecks(env, serveTestDB(t, true, "", "[]"), serveRoot, now)
		c := findCheck(Report{Checks: checks}, "serve", "clients")
		if c == nil {
			t.Fatal("missing serve: clients check")
		}
		if c.Severity != SevWarn {
			t.Errorf("severity = %v, want SevWarn", c.Severity)
		}
		if !strings.Contains(c.Detail, "none enrolled") {
			t.Errorf("detail = %q, want containing 'none enrolled'", c.Detail)
		}
	})

	t.Run("parse error", func(t *testing.T) {
		env := &fakeEnv{
			existingFiles: map[string]bool{serveRoot: true},
		}
		// Valid JSON of the wrong shape: the kv row is validated on write, so
		// a malformed document is one that is not the client array.
		checks := ServeChecks(env, serveTestDB(t, true, "", `{"not":"a client list"}`), serveRoot, now)
		c := findCheck(Report{Checks: checks}, "serve", "clients")
		if c == nil {
			t.Fatal("missing serve: clients check")
		}
		if c.Severity != SevFail {
			t.Errorf("severity = %v, want SevFail", c.Severity)
		}
		if !strings.Contains(c.Detail, "parse error") {
			t.Errorf("detail = %q, want containing 'parse error'", c.Detail)
		}
	})
}

func TestServeChecksState(t *testing.T) {
	now := time.Now()
	serveRoot := "/fake/serve"

	t.Run("state writable", func(t *testing.T) {
		env := &fakeEnv{
			existingFiles: map[string]bool{serveRoot: true},
		}
		checks := ServeChecks(env, serveTestDB(t, true, "", ""), serveRoot, now)
		c := findCheck(Report{Checks: checks}, "serve", "state")
		if c == nil {
			t.Fatal("missing serve: state check")
		}
		if c.Severity != SevOK {
			t.Errorf("severity = %v, want SevOK", c.Severity)
		}
		if !strings.Contains(c.Detail, "writable") {
			t.Errorf("detail = %q, want containing 'writable'", c.Detail)
		}
	})

	t.Run("serveRoot stat fails", func(t *testing.T) {
		env := &fakeEnv{
			// serveRoot missing
			existingFiles: map[string]bool{},
		}
		checks := ServeChecks(env, serveTestDB(t, true, "", ""), serveRoot, now)
		c := findCheck(Report{Checks: checks}, "serve", "state")
		if c == nil {
			t.Fatal("missing serve: state check")
		}
		if c.Severity != SevFail {
			t.Errorf("severity = %v, want SevFail", c.Severity)
		}
	})

	t.Run("probe fails", func(t *testing.T) {
		env := &fakeEnv{
			existingFiles: map[string]bool{serveRoot: true},
			probeErr:      errors.New("permission denied"),
		}
		checks := ServeChecks(env, serveTestDB(t, true, "", ""), serveRoot, now)
		c := findCheck(Report{Checks: checks}, "serve", "state")
		if c == nil {
			t.Fatal("missing serve: state check")
		}
		if c.Severity != SevFail {
			t.Errorf("severity = %v, want SevFail", c.Severity)
		}
		if !strings.Contains(c.Detail, "permission denied") && !strings.Contains(c.Detail, "not writable") {
			t.Errorf("detail = %q, want containing error", c.Detail)
		}
	})
}
