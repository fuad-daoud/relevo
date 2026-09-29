package serve

import (
	"crypto/x509"
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

func testSecretStore(t *testing.T) SecretStore {
	t.Helper()
	return SecretStore{DB: testServeDB(t)}
}

func requireDNS(t *testing.T, cert *x509.Certificate, name string) {
	t.Helper()
	for _, d := range cert.DNSNames {
		if d == name {
			return
		}
	}
	t.Errorf("missing DNS SAN %s in %v", name, cert.DNSNames)
}

func requireIP(t *testing.T, cert *x509.Certificate, ipStr string) {
	t.Helper()
	target := net.ParseIP(ipStr)
	for _, ip := range cert.IPAddresses {
		if ip.Equal(target) {
			return
		}
	}
	t.Errorf("missing IP SAN %s in %v", ipStr, cert.IPAddresses)
}

func TestInitTLSAndLoad(t *testing.T) {
	secrets := testSecretStore(t)
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	hosts := []string{"example.com", "10.0.0.1"}

	fp, err := InitTLS(secrets, hosts, now)
	if err != nil {
		t.Fatalf("InitTLS: %v", err)
	}
	if fp == "" {
		t.Fatal("expected non-empty fingerprint")
	}

	keyPEM, ok, err := secrets.SecretGet(tlsKeySecret)
	if err != nil || !ok || len(keyPEM) == 0 {
		t.Fatalf("serve.tls.key secret = (len %d, ok %v, err %v), want present", len(keyPEM), ok, err)
	}
	crtPEM, ok, err := secrets.SecretGet(tlsCertSecret)
	if err != nil || !ok || len(crtPEM) == 0 {
		t.Fatalf("serve.tls.cert secret = (len %d, ok %v, err %v), want present", len(crtPEM), ok, err)
	}

	cert, err := LoadTLS(secrets)
	if err != nil {
		t.Fatalf("LoadTLS: %v", err)
	}
	if cert.Leaf == nil {
		t.Fatal("cert.Leaf is nil")
	}

	if !cert.Leaf.NotBefore.Equal(now) {
		t.Errorf("NotBefore = %v, want %v", cert.Leaf.NotBefore, now)
	}
	if wantAfter := now.AddDate(10, 0, 0); !cert.Leaf.NotAfter.Equal(wantAfter) {
		t.Errorf("NotAfter = %v, want %v", cert.Leaf.NotAfter, wantAfter)
	}

	for _, name := range []string{"example.com", "localhost"} {
		requireDNS(t, cert.Leaf, name)
	}
	for _, ip := range []string{"10.0.0.1", "127.0.0.1"} {
		requireIP(t, cert.Leaf, ip)
	}
	if hn, err := os.Hostname(); err == nil && hn != "" {
		if net.ParseIP(hn) != nil {
			requireIP(t, cert.Leaf, hn)
		} else {
			requireDNS(t, cert.Leaf, hn)
		}
	}

	certFP, err := Fingerprint(secrets)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	leafFP := FingerprintOf(cert.Leaf.Raw)
	if certFP != leafFP {
		t.Errorf("Fingerprint = %q, FingerprintOf(leaf.Raw) = %q", certFP, leafFP)
	}
	if fp != leafFP {
		t.Errorf("InitTLS returned fp = %q, want %q", fp, leafFP)
	}
}

func TestInitTLSRefusesOverwrite(t *testing.T) {
	secrets := testSecretStore(t)
	now := time.Now()

	if _, err := InitTLS(secrets, nil, now); err != nil {
		t.Fatalf("first InitTLS: %v", err)
	}

	if _, err := InitTLS(secrets, nil, now); !errors.Is(err, ErrTLSExists) {
		t.Fatalf("second InitTLS err = %v, want ErrTLSExists", err)
	}
}
