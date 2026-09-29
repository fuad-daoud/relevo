package doctor

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/serve"
)

// ServeChecks evaluates the health of a relevo serve installation. It runs
// when the machine database holds the serve.tls.key secret: the certificate
// and the clients come from that database, and the state check probes the
// root the running daemon's serve.daemon row names -- or serveRoot when
// there is none. Reading the root from the row is what makes the serve rows
// show on a box started with a non-default --state.
func ServeChecks(env Env, d *db.DB, serveRoot string, now time.Time) []Check {
	if d == nil {
		return nil
	}
	if _, ok, err := d.SecretGet("serve.tls.key"); err != nil || !ok {
		return nil
	}

	root := serveRoot
	if p, ok, err := serve.ReadDaemonPointer(d); err == nil && ok && p.Root != "" {
		root = p.Root
	}

	return []Check{
		serveCertificateCheck(d, now),
		serveClientsCheck(d),
		serveStateCheck(env, root),
	}
}

func serveCertificateCheck(d *db.DB, now time.Time) Check {
	c := Check{Group: "serve", Name: "certificate"}
	unreadable := func() Check {
		c.Severity, c.Detail, c.Fix = SevFail, "unreadable", "relevo serve init"
		return c
	}

	rawCrt, ok, err := d.SecretGet("serve.tls.cert")
	if err != nil || !ok {
		return unreadable()
	}
	block, _ := pem.Decode(rawCrt)
	if block == nil {
		return unreadable()
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return unreadable()
	}

	switch {
	case now.After(cert.NotAfter):
		c.Severity, c.Detail, c.Fix = SevFail, "expired", "relevo serve init"
	case cert.NotAfter.Before(now.Add(30 * 24 * time.Hour)):
		c.Severity, c.Detail = SevWarn, fmt.Sprintf("expires %s", cert.NotAfter.Format("2006-01-02"))
	default:
		c.Severity, c.Detail = SevOK, fmt.Sprintf("valid until %s", cert.NotAfter.Format("2006-01-02"))
	}
	return c
}

func serveClientsCheck(d *db.DB) Check {
	c := Check{Group: "serve", Name: "clients"}
	noneEnrolled := func() Check {
		c.Severity, c.Detail, c.Fix = SevWarn, "none enrolled", "relevo serve enroll --label <name> --key <line>"
		return c
	}

	rawClients, ok, err := d.KVGet("serve.clients")
	switch {
	case err != nil:
		c.Severity, c.Detail = SevFail, err.Error()
		return c
	case !ok:
		return noneEnrolled()
	}

	var list []struct {
		ID        string    `json:"id"`
		RevokedAt time.Time `json:"revoked_at,omitempty"`
	}
	if err := json.Unmarshal(rawClients, &list); err != nil {
		c.Severity, c.Detail, c.Fix = SevFail, "parse error", "fix the serve.clients row in the database"
		return c
	}

	active := 0
	for _, cl := range list {
		if cl.RevokedAt.IsZero() {
			active++
		}
	}
	switch active {
	case 0:
		return noneEnrolled()
	case 1:
		c.Severity, c.Detail = SevOK, "1 enrolled"
	default:
		c.Severity, c.Detail = SevOK, fmt.Sprintf("%d enrolled", active)
	}
	return c
}

func serveStateCheck(env Env, root string) Check {
	c := Check{Group: "serve", Name: "state"}
	if err := env.Stat(root); err != nil {
		c.Severity, c.Detail, c.Fix = SevFail, err.Error(), "relevo serve init"
		return c
	}
	if err := env.Probe(filepath.Join(root, "bindings")); err != nil {
		c.Severity, c.Detail = SevFail, fmt.Sprintf("not writable: %v", err)
		return c
	}
	c.Severity, c.Detail = SevOK, "bindings writable"
	return c
}
