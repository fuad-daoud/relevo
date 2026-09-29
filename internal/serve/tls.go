package serve

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

var ErrTLSExists = errors.New("server key or certificate already exists")

const (
	tlsKeySecret  = "serve.tls.key"
	tlsCertSecret = "serve.tls.cert"
)

// SecretStore is the secret surface the TLS helpers read and write: the DB's
// table.
type SecretStore struct {
	DB *db.DB
}

func (s SecretStore) SecretGet(name string) ([]byte, bool, error) {
	if s.DB == nil {
		return nil, false, nil
	}
	return s.DB.SecretGet(name)
}

func (s SecretStore) SecretPut(name string, value []byte, now time.Time) error {
	return s.DB.Tx(func(t *db.Tx) error { return t.SecretPut(name, value, now) })
}

// FingerprintOf returns "sha256:" + lower-case hex of sha256(der).
func FingerprintOf(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func Fingerprint(secrets SecretStore) (string, error) {
	data, ok, err := secrets.SecretGet(tlsCertSecret)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("no server certificate")
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "", errors.New("no PEM certificate found")
	}
	return FingerprintOf(block.Bytes), nil
}

func addSANs(t *x509.Certificate, hosts []string) {
	allHosts := append([]string(nil), hosts...)
	allHosts = append(allHosts, "localhost", "127.0.0.1")
	if hn, err := os.Hostname(); err == nil && hn != "" {
		allHosts = append(allHosts, hn)
	}

	dnsSeen := make(map[string]bool)
	ipSeen := make(map[string]bool)
	for _, h := range allHosts {
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			if s := ip.String(); !ipSeen[s] {
				ipSeen[s] = true
				t.IPAddresses = append(t.IPAddresses, ip)
			}
			continue
		}
		if !dnsSeen[h] {
			dnsSeen[h] = true
			t.DNSNames = append(t.DNSNames, h)
		}
	}
}

func selfSignedCert(priv *ecdsa.PrivateKey, hosts []string, now time.Time) (certPEM, certDER []byte, err error) {
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, nil, err
	}

	template := x509.Certificate{
		SerialNumber:          serialNumber,
		Subject:               pkix.Name{CommonName: "relevo serve"},
		NotBefore:             now,
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	addSANs(&template, hosts)

	certDER, err = x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return certPEM, certDER, nil
}

// InitTLS generates a server key and self-signed certificate into the secrets.
// If either is already set it returns ErrTLSExists without modifying either.
func InitTLS(secrets SecretStore, hosts []string, now time.Time) (string, error) {
	if _, ok, err := secrets.SecretGet(tlsKeySecret); err != nil {
		return "", err
	} else if ok {
		return "", ErrTLSExists
	}
	if _, ok, err := secrets.SecretGet(tlsCertSecret); err != nil {
		return "", err
	} else if ok {
		return "", ErrTLSExists
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	certPEM, certDER, err := selfSignedCert(priv, hosts, now)
	if err != nil {
		return "", err
	}

	if err := secrets.SecretPut(tlsKeySecret, keyPEM, now); err != nil {
		return "", err
	}
	if err := secrets.SecretPut(tlsCertSecret, certPEM, now); err != nil {
		_ = secrets.DB.Tx(func(t *db.Tx) error { return t.SecretDelete(tlsKeySecret) })
		return "", err
	}

	return FingerprintOf(certDER), nil
}

// LoadTLS loads the server TLS certificate and key from the secrets, populating
// Leaf on the returned tls.Certificate.
func LoadTLS(secrets SecretStore) (tls.Certificate, error) {
	certPEM, ok, err := secrets.SecretGet(tlsCertSecret)
	if err != nil {
		return tls.Certificate{}, err
	}
	if !ok {
		return tls.Certificate{}, errors.New("no server certificate")
	}
	keyPEM, ok, err := secrets.SecretGet(tlsKeySecret)
	if err != nil {
		return tls.Certificate{}, err
	}
	if !ok {
		return tls.Certificate{}, errors.New("no server key")
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, err
	}
	if len(cert.Certificate) > 0 {
		cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return tls.Certificate{}, err
		}
	}
	return cert, nil
}
