package doctor

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/isolate"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/serve"
)

// containerPodmanLookPath resolves the container runtime for the isolation
// row. It is a var so a test can pin it absent or present.
var containerPodmanLookPath = exec.LookPath

// containerImageExists probes whether the configured image is present. It is a
// var so the container row is testable without a runtime.
var containerImageExists = func(ctx context.Context, image string) error {
	out, err := exec.CommandContext(ctx, "podman", "image", "exists", image).CombinedOutput()
	if err != nil {
		return fmt.Errorf("podman image exists %s: %w (%s)", image, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// tenantEUID reports the effective uid the user-mode isolation row checks. It
// is a var so a test can pin root or a non-root euid.
var tenantEUID = os.Geteuid

// tenantLookup resolves a login name to its uid and primary gid. It is a var so
// the user-mode row is testable without the host's real account database.
var tenantLookup = func(name string) (uid, gid uint32, err error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}
	uid64, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return 0, 0, err
	}
	gid64, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return 0, 0, err
	}
	return uint32(uid64), uint32(gid64), nil
}

// tenantStat reads a path's owner, group and permission bits. It is a var so
// the owner-root check is testable without real chowns.
var tenantStat = func(path string) (uid, gid uint32, mode os.FileMode, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0, 0, err
	}
	uid, gid, ok := statOwner(fi)
	if !ok {
		return 0, 0, fi.Mode().Perm(), nil
	}
	return uid, gid, fi.Mode().Perm(), nil
}

// ServeChecks evaluates the health of a relevo serve installation. It runs
// when the machine database holds the serve.tls.key secret: the certificate
// and the clients come from that database, and the state check probes the
// root the running daemon's serve.daemon row names -- or serveRoot when
// there is none. Reading the root from the row is what makes the serve rows
// show on a box started with a non-default --state.
//
// isolation is the raw serve.isolation policy value; the isolation row parses
// it, so a hand-built unknown reads as a failure. image is the configured
// serve.isolation_image the container row probes. sharedLogins is the parsed
// serve.isolation_shared_logins value: the user-mode row warns when it is on.
func ServeChecks(env Env, d *db.DB, serveRoot string, now time.Time, isolation, image string, sharedLogins bool) []Check {
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
		serveIsolationCheck(d, root, isolation, image, sharedLogins),
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

	active, present, err := serveClientCount(d)
	switch {
	case err != nil && present:
		c.Severity, c.Detail, c.Fix = SevFail, "parse error", "fix the serve.clients row in the database"
		return c
	case err != nil:
		c.Severity, c.Detail = SevFail, err.Error()
		return c
	case !present:
		return noneEnrolled()
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

// doctorClient is the slice of an enrolled client the doctor reads: enough to
// count actives and to check a user-mode owner's declared user and owner root.
type doctorClient struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	RevokedAt time.Time `json:"revoked_at,omitempty"`
	UnixUser  string    `json:"unix_user,omitempty"`
}

// serveActiveClients reads the serve.clients registry and returns its active
// (non-revoked) entries, whether the row was present at all, and any read or
// parse error.
func serveActiveClients(d *db.DB) (clients []doctorClient, present bool, err error) {
	raw, ok, err := d.KVGet("serve.clients")
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	var list []doctorClient
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, true, err
	}
	for _, cl := range list {
		if cl.RevokedAt.IsZero() {
			clients = append(clients, cl)
		}
	}
	return clients, true, nil
}

// serveClientCount reports the number of active (non-revoked) clients, whether
// the row was present at all, and any read or parse error. A malformed row is
// an error, so a broken registry can never read as a clean zero.
func serveClientCount(d *db.DB) (active int, present bool, err error) {
	clients, present, err := serveActiveClients(d)
	if err != nil {
		return 0, present, err
	}
	return len(clients), present, nil
}

// serveIsolationCheck is the serve isolation row. It parses the raw policy
// value and reports:
//
//   - none with at most one active client: OK, Detail "none".
//   - none with two or more: Warn, naming the count and the shared unix user.
//   - user: Fail with the first unmet prerequisite -- not root, an active
//     client with no unix_user, a user missing on this host, or an owner root
//     with the wrong owner, group or mode -- and otherwise OK (or Warn when
//     shared logins are on), Detail "scopes=off (isolation=user)".
//   - container: Fail when podman is absent or the configured image is
//     missing, naming the exact fix, and otherwise OK, Detail
//     "isolation=container".
//   - an unknown value: Fail naming serve.isolation.
//   - an unreadable or unparseable serve.clients row: Fail, so a broken
//     registry can never read as a clean none.
func serveIsolationCheck(d *db.DB, root, raw, image string, sharedLogins bool) Check {
	c := Check{Group: "serve", Name: "isolation"}
	const fix = `relevo config set policy.serve '{"isolation":"none"}'`

	mode, err := isolate.Parse(raw)
	if err != nil {
		c.Severity, c.Detail, c.Fix = SevFail, err.Error(), fix
		return c
	}
	if err := mode.Available(); err != nil {
		c.Severity, c.Detail, c.Fix = SevFail, err.Error(), fix
		return c
	}

	clients, _, err := serveActiveClients(d)
	if err != nil {
		c.Severity, c.Detail, c.Fix = SevFail, "serve.clients unreadable", "fix the serve.clients row in the database"
		return c
	}

	if mode == isolate.ModeUser {
		return serveUserIsolationCheck(c, root, clients, sharedLogins, fix)
	}
	if mode == isolate.ModeContainer {
		return serveContainerIsolationCheck(c, image)
	}

	if len(clients) <= 1 {
		c.Severity, c.Detail = SevOK, "none"
		return c
	}
	c.Severity = SevWarn
	c.Detail = fmt.Sprintf("%d active clients share one unix user", len(clients))
	return c
}

// serveContainerIsolationCheck is the container-mode half of the isolation row.
// It fails with the exact fix when podman is absent or the configured image is
// missing, and otherwise reports the mode in force.
func serveContainerIsolationCheck(c Check, image string) Check {
	if _, err := containerPodmanLookPath("podman"); err != nil {
		c.Severity, c.Detail = SevFail, "podman not found on PATH"
		c.Fix = "install podman and ensure it is on PATH"
		return c
	}
	if err := containerImageExists(context.Background(), image); err != nil {
		c.Severity, c.Detail = SevFail, fmt.Sprintf("container image %q not found", image)
		c.Fix = "build or pull the image (see dist/Containerfile)"
		return c
	}
	c.Severity, c.Detail = SevOK, "isolation=container"
	return c
}

// serveUserIsolationCheck is the user-mode half of the isolation row. It fails
// with a fix on the first unmet prerequisite and otherwise reports the
// scopes-off detail, warning when shared logins are on.
func serveUserIsolationCheck(c Check, root string, clients []doctorClient, sharedLogins bool, fix string) Check {
	fail := func(detail, fixLine string) Check {
		c.Severity, c.Detail, c.Fix = SevFail, detail, fixLine
		return c
	}

	if err := isolate.CheckPrivilege(isolate.ModeUser, tenantEUID()); err != nil {
		return fail(err.Error(), fix)
	}
	for _, cl := range clients {
		if cl.UnixUser == "" {
			return fail(
				fmt.Sprintf("client %s has no unix user declared", cl.Label),
				fmt.Sprintf("relevo serve enroll --label %s --key <line> --user <user>", cl.Label))
		}
		uid, gid, err := tenantLookup(cl.UnixUser)
		if err != nil {
			return fail(
				fmt.Sprintf("unix user %q is not on this host", cl.UnixUser),
				fmt.Sprintf("useradd --create-home %s", cl.UnixUser))
		}
		if line, bad := ownerRootMismatch(root, cl.ID, uid, gid); bad {
			return fail(line, "")
		}
	}

	c.Severity = SevOK
	c.Detail = "scopes=off (isolation=user)"
	if sharedLogins {
		c.Severity = SevWarn
		c.Detail += "; shared logins on"
	}
	return c
}

// ownerRootMismatch checks one owner's bindings/<hex> directory against the
// approved layout -- root-owned, group the tenant's primary gid, mode 0710. A
// missing root is not a mismatch: the create has not run yet. bad is true with
// the exact chown/chmod line when the owner, group or mode is wrong.
func ownerRootMismatch(root, id string, uid, gid uint32) (string, bool) {
	dir, ok := remote.ClientID(id).Dir()
	if !ok {
		// A hand-written id the server would refuse; the create refuses it too.
		return "", false
	}
	path := filepath.Join(root, "bindings", dir)
	gotUID, gotGID, mode, err := tenantStat(path)
	if err != nil {
		return "", false
	}
	_ = uid
	if mode != 0o710 {
		return fmt.Sprintf("%s has mode %04o, want 0710: run `chmod 0710 %s`", path, mode, path), true
	}
	if gotGID != gid || gotUID != 0 {
		return fmt.Sprintf("%s is owned by %d:%d, want root:%d: run `chown root:%d %s`", path, gotUID, gotGID, gid, gid, path), true
	}
	return "", false
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
