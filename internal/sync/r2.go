package sync

// The R2 credentials, machine-local beside the token. Four secrets rather than
// one setting because they are four independent things to rotate: an endpoint and
// a bucket name are not secret, but the key that signs a request to them is, and
// a machine should be able to be moved to another bucket without its access key
// being reissued.
//
// Nothing here decides anything about sync. It stores four values, reads them
// back, and says whether an enable may go ahead with what is on hand -- the
// worker builds the actual store from what hello carries, out of reach of this
// package.

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The names the four R2 credentials are stored under. They sit in the local
// secret table beside turso.token, so they are machine-local by the table they
// are in and a change set can never carry one.
const (
	// SecretR2Endpoint is the account-scoped S3 URL, which carries no bucket.
	SecretR2Endpoint = "r2.endpoint"
	// SecretR2Bucket is the bucket objects are stored under.
	SecretR2Bucket = "r2.bucket"
	// SecretR2KeyID is the access key that signs requests.
	SecretR2KeyID = "r2.key_id"
	// SecretR2Secret is that key's secret. It is never formatted into a refusal
	// or an error, on the same rule as the token: every path out of this
	// package ends up in a log line.
	SecretR2Secret = "r2.secret"
)

// ErrNoR2 reports an enable whose machine holds no R2 credentials at all. It
// names all four secrets and the flags that set them, and stops there: which
// ones were tried is not printed, because the attempt is as sensitive as the
// values.
var ErrNoR2 = errors.New("sync: no R2 credentials on this machine: pass " +
	"--r2-endpoint, --r2-bucket and --r2-key-id, and set " + EnvR2Secret +
	" or pass --r2-secret-stdin")

// EnvR2Secret is the environment variable the R2 secret is read from when
// --r2-secret-stdin is not given. Its name is the tool's own rather than a
// vendor's, because R2's secret is minted by the account and shared with this
// program, and a shell exporting one for another purpose should not be picked up
// by accident.
const EnvR2Secret = "RELEVO_R2_SECRET"

// R2Secrets is the four machine-local R2 credentials as one value, which is what
// the preflight reasons about and what the handshake is built from.
//
// It is a struct rather than four arguments because they are never used apart:
// a caller that had one of them and not the others has nothing it can do with
// the one.
type R2Secrets struct {
	Endpoint string
	Bucket   string
	KeyID    string
	Secret   string
}

// Complete reports whether every half is set. A partially filled set is not a
// usable credential, so it answers false the same way an empty one does rather
// than letting an enable proceed on half a configuration.
func (r R2Secrets) Complete() bool {
	return r.Endpoint != "" && r.Bucket != "" && r.KeyID != "" && r.Secret != ""
}

// Missing names the credentials this set does not carry, in the order they are
// stored. It is what the refusal lists, so a reader is told which flag to reach
// for instead of being sent to a documentation page.
func (r R2Secrets) Missing() []string {
	var out []string
	if r.Endpoint == "" {
		out = append(out, SecretR2Endpoint)
	}
	if r.Bucket == "" {
		out = append(out, SecretR2Bucket)
	}
	if r.KeyID == "" {
		out = append(out, SecretR2KeyID)
	}
	if r.Secret == "" {
		out = append(out, SecretR2Secret)
	}
	return out
}

// SetR2 stores the four credentials in the local file beside the shared
// database, in one transaction so a machine is never left with half a set.
//
// The values are never formatted into an error: a rejection names the secret and
// never what it was handed.
func SetR2(l Local, r R2Secrets, now time.Time) error {
	if !r.Complete() {
		return fmt.Errorf("sync: incomplete R2 credentials, missing %v: %w", r.Missing(), db.ErrInvalid)
	}
	return l.Tx(func(t *db.Tx) error {
		for _, s := range []struct {
			name  string
			value string
		}{
			{SecretR2Endpoint, r.Endpoint},
			{SecretR2Bucket, r.Bucket},
			{SecretR2KeyID, r.KeyID},
			{SecretR2Secret, r.Secret},
		} {
			if err := t.SecretPut(s.name, []byte(s.value), now.UTC()); err != nil {
				return fmt.Errorf("sync: set %s: %w", s.name, err)
			}
		}
		return nil
	})
}

// ReadR2 returns the four credentials this machine holds. A machine with none is
// a zero R2Secrets and no error: a machine that never configured R2 has nothing
// to report, which is the ordinary case until bodies are enabled. An individual
// value that will not decode is a failure rather than an absent one, for the
// same reason a join marker that will not parse is: a half-readable credential
// set must not read as a configured machine.
func ReadR2(l Local) (R2Secrets, error) {
	var out R2Secrets
	for _, s := range []struct {
		name string
		dst  *string
	}{
		{SecretR2Endpoint, &out.Endpoint},
		{SecretR2Bucket, &out.Bucket},
		{SecretR2KeyID, &out.KeyID},
		{SecretR2Secret, &out.Secret},
	} {
		value, ok, err := l.SecretGet(s.name)
		if err != nil {
			return R2Secrets{}, fmt.Errorf("sync: read %s: %w", s.name, err)
		}
		if !ok {
			continue
		}
		*s.dst = string(bytes.TrimSpace(value))
	}
	return out, nil
}

// DeleteR2 removes the four credentials. An absent one is a no-op rather than
// an error, so a machine that never configured R2 still turns sync off cleanly,
// and one secret left behind by an older build is still removed.
func DeleteR2(l Local) error {
	return l.Tx(func(t *db.Tx) error {
		for _, name := range []string{SecretR2Endpoint, SecretR2Bucket, SecretR2KeyID, SecretR2Secret} {
			if err := t.SecretDelete(name); err != nil {
				return fmt.Errorf("sync: delete %s: %w", name, err)
			}
		}
		return nil
	})
}
