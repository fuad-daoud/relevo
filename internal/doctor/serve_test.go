package doctor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
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
	checks := ServeChecks(env, serveTestDB(t, false, "", ""), "/fake/serve", now, "none", "", false)
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
			checks := ServeChecks(env, serveTestDB(t, true, tc.certPEM, ""), serveRoot, now, "none", "", false)
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
		checks := ServeChecks(env, serveTestDB(t, true, "", clientsJSON), serveRoot, now, "none", "", false)
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
		checks := ServeChecks(env, serveTestDB(t, true, "", "[]"), serveRoot, now, "none", "", false)
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
		checks := ServeChecks(env, serveTestDB(t, true, "", `{"not":"a client list"}`), serveRoot, now, "none", "", false)
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

// TestServeChecksIsolation pins the serve isolation row's rule table: none is
// OK with at most one active client and a Warn with two, user is refused
// without root, an unknown value Fails naming serve.isolation, and a broken
// client registry Fails rather than reading as a clean none. The container
// row's prerequisites are pinned separately.
func TestServeChecksIsolation(t *testing.T) {
	now := time.Now()
	serveRoot := "/fake/serve"
	env := &fakeEnv{existingFiles: map[string]bool{serveRoot: true}}
	const fix = `relevo config set policy.serve '{"isolation":"none"}'`

	cases := []struct {
		name        string
		isolation   string
		clients     string
		wantSev     Severity
		wantDetail  string
		wantFix     string
		detailExact bool
	}{
		{name: "none with no clients", isolation: "none", clients: "[]", wantSev: SevOK, wantDetail: "none", detailExact: true},
		{name: "none with one client", isolation: "none", clients: `[{"id":"id1","label":"c1"}]`, wantSev: SevOK, wantDetail: "none", detailExact: true},
		{name: "none with two clients", isolation: "none", clients: `[{"id":"id1","label":"c1"},{"id":"id2","label":"c2"}]`, wantSev: SevWarn, wantDetail: "2 active clients share one unix user", detailExact: true},
		{name: "user fails", isolation: "user", clients: "[]", wantSev: SevFail, wantDetail: "serve.isolation=user", wantFix: fix},
		{name: "unknown fails", isolation: "host", clients: "[]", wantSev: SevFail, wantDetail: "serve.isolation:", wantFix: fix},
		{name: "broken registry fails", isolation: "none", clients: `{"not":"a client list"}`, wantSev: SevFail, wantDetail: "serve.clients unreadable", wantFix: "fix the serve.clients row in the database"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checks := ServeChecks(env, serveTestDB(t, true, "", tc.clients), serveRoot, now, tc.isolation, "", false)
			c := findCheck(Report{Checks: checks}, "serve", "isolation")
			if c == nil {
				t.Fatal("missing serve: isolation check")
			}
			if c.Severity != tc.wantSev {
				t.Errorf("severity = %v, want %v", c.Severity, tc.wantSev)
			}
			if tc.detailExact {
				if c.Detail != tc.wantDetail {
					t.Errorf("detail = %q, want %q", c.Detail, tc.wantDetail)
				}
			} else if !strings.Contains(c.Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want containing %q", c.Detail, tc.wantDetail)
			}
			if c.Fix != tc.wantFix {
				t.Errorf("fix = %q, want %q", c.Fix, tc.wantFix)
			}
		})
	}
}

// TestServeChecksIsolationContainer pins the container isolation row, one row
// per prerequisite: podman absent, the configured image absent, and the OK row.
// The two probes are injected so the row is checked without a container
// runtime.
func TestServeChecksIsolationContainer(t *testing.T) {
	now := time.Now()
	serveRoot := "/fake/serve"
	env := &fakeEnv{existingFiles: map[string]bool{serveRoot: true}}

	origLook, origImage := containerPodmanLookPath, containerImageExists
	t.Cleanup(func() { containerPodmanLookPath, containerImageExists = origLook, origImage })

	const image = "relevo-builder:local"
	cases := []struct {
		name       string
		lookErr    error
		imageErr   error
		wantSev    Severity
		wantDetail string
		wantFix    string
	}{
		{name: "podman absent", lookErr: errors.New("not found"), wantSev: SevFail, wantDetail: "podman not found", wantFix: "install podman"},
		{name: "image absent", imageErr: errors.New("no such image"), wantSev: SevFail, wantDetail: "not found", wantFix: "dist/Containerfile"},
		{name: "ok", wantSev: SevOK, wantDetail: "isolation=container"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			containerPodmanLookPath = func(string) (string, error) {
				if tc.lookErr != nil {
					return "", tc.lookErr
				}
				return "/usr/bin/podman", nil
			}
			containerImageExists = func(context.Context, string) error { return tc.imageErr }
			checks := ServeChecks(env, serveTestDB(t, true, "", "[]"), serveRoot, now, "container", image, false)
			c := findCheck(Report{Checks: checks}, "serve", "isolation")
			if c == nil {
				t.Fatal("missing serve: isolation check")
			}
			if c.Severity != tc.wantSev {
				t.Errorf("severity = %v, want %v (detail %q)", c.Severity, tc.wantSev, c.Detail)
			}
			if !strings.Contains(c.Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want containing %q", c.Detail, tc.wantDetail)
			}
			if tc.wantFix != "" && !strings.Contains(c.Fix, tc.wantFix) {
				t.Errorf("fix = %q, want containing %q", c.Fix, tc.wantFix)
			}
		})
	}
}

func TestServeChecksState(t *testing.T) {
	now := time.Now()
	serveRoot := "/fake/serve"

	t.Run("state writable", func(t *testing.T) {
		env := &fakeEnv{
			existingFiles: map[string]bool{serveRoot: true},
		}
		checks := ServeChecks(env, serveTestDB(t, true, "", ""), serveRoot, now, "none", "", false)
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
		checks := ServeChecks(env, serveTestDB(t, true, "", ""), serveRoot, now, "none", "", false)
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
		checks := ServeChecks(env, serveTestDB(t, true, "", ""), serveRoot, now, "none", "", false)
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

// TestServeChecksIsolationUser pins the user-mode isolation row, one row per
// prerequisite: not root, a client with no unix_user, a user missing on this
// host, an owner root with the wrong mode and with the wrong owner, and the OK
// (and shared-logins Warn) rows. The lookup, stat and euid seams are injected
// so the row is checked without root, a real account or a real chown.
func TestServeChecksIsolationUser(t *testing.T) {
	now := time.Now()
	serveRoot := t.TempDir()
	env := &fakeEnv{existingFiles: map[string]bool{serveRoot: true}}
	const uid, gid = uint32(1001), uint32(1002)
	// A well-formed ClientID: the owner-root check derives the bindings/<hex>
	// directory through ClientID.Dir, so the id must decode.
	clientID := "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	withUser := `[{"id":"` + clientID + `","label":"alice","unix_user":"alice"}]`
	noUser := `[{"id":"` + clientID + `","label":"alice"}]`

	origEUID, origLookup, origStat := tenantEUID, tenantLookup, tenantStat
	t.Cleanup(func() { tenantEUID, tenantLookup, tenantStat = origEUID, origLookup, origStat })

	okLookup := func(string) (uint32, uint32, error) { return uid, gid, nil }
	okStat := func(string) (uint32, uint32, os.FileMode, error) { return 0, gid, 0o710, nil }

	cases := []struct {
		name       string
		euid       int
		clients    string
		lookup     func(string) (uint32, uint32, error)
		stat       func(string) (uint32, uint32, os.FileMode, error)
		shared     bool
		wantSev    Severity
		wantDetail string
	}{
		{name: "not root", euid: 1000, clients: withUser, lookup: okLookup, stat: okStat, wantSev: SevFail, wantDetail: "requires root"},
		{name: "no declared user", euid: 0, clients: noUser, lookup: okLookup, stat: okStat, wantSev: SevFail, wantDetail: "no unix user"},
		{name: "missing user", euid: 0, clients: withUser, lookup: func(string) (uint32, uint32, error) { return 0, 0, errors.New("no such user") }, stat: okStat, wantSev: SevFail, wantDetail: "not on this host"},
		{name: "owner root wrong mode", euid: 0, clients: withUser, lookup: okLookup, stat: func(string) (uint32, uint32, os.FileMode, error) { return 0, gid, 0o755, nil }, wantSev: SevFail, wantDetail: "chmod 0710"},
		{name: "owner root wrong owner", euid: 0, clients: withUser, lookup: okLookup, stat: func(string) (uint32, uint32, os.FileMode, error) { return 1234, gid, 0o710, nil }, wantSev: SevFail, wantDetail: "chown root"},
		{name: "ok", euid: 0, clients: withUser, lookup: okLookup, stat: okStat, wantSev: SevOK, wantDetail: "scopes=off (isolation=user)"},
		{name: "shared logins warn", euid: 0, clients: withUser, lookup: okLookup, stat: okStat, shared: true, wantSev: SevWarn, wantDetail: "shared logins on"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tenantEUID = func() int { return tc.euid }
			tenantLookup = tc.lookup
			tenantStat = tc.stat
			checks := ServeChecks(env, serveTestDB(t, true, "", tc.clients), serveRoot, now, "user", "", tc.shared)
			c := findCheck(Report{Checks: checks}, "serve", "isolation")
			if c == nil {
				t.Fatal("missing serve: isolation check")
			}
			if c.Severity != tc.wantSev {
				t.Errorf("severity = %v, want %v (detail %q)", c.Severity, tc.wantSev, c.Detail)
			}
			if !strings.Contains(c.Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want containing %q", c.Detail, tc.wantDetail)
			}
		})
	}
}
