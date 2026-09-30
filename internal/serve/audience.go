package serve

import (
	"errors"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// ErrNoAudience is the refusal a server gives when neither a certificate nor a
// --public-host value gives it a set of audiences to accept.
var ErrNoAudience = errors.New("no audience: run relevo serve init or pass --public-host")

// Audiences returns the audience set a server accepts: the certificate
// fingerprint when secrets holds one, plus host:<h> for every public host. The
// public hosts are normalised exactly as a client normalises the host in its
// server URL, so a client's host: audience and a --public-host value meet on
// the same string.
//
// A missing certificate is not by itself a refusal: a server started
// --insecure-http has none, and a CA-mode server behind a TLS-terminating
// proxy needs none. With no certificate and no public hosts the set is empty
// and the call fails closed with ErrNoAudience.
func Audiences(secrets SecretStore, publicHosts []string) ([]string, error) {
	var out []string
	fp, err := Fingerprint(secrets)
	switch {
	case err == nil:
		out = append(out, fp)
	case errors.Is(err, ErrNoCertificate):
		// A server with no certificate -- plain HTTP, or CA mode behind a
		// TLS-terminating proxy -- leans on its public hosts alone.
	default:
		return nil, err
	}
	for _, h := range publicHosts {
		out = append(out, remote.HostAudience(h))
	}
	if len(out) == 0 {
		return nil, ErrNoAudience
	}
	return out, nil
}
