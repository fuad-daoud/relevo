package sync

import (
	"bytes"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// SecretToken is the name this installation's remote bearer token is stored
// under: one database-scoped token per installation, in the local secret table.
// Revoking it cuts off this machine alone.
const SecretToken = "turso.token"

// SetToken stores value as this installation's remote token, in the local file
// beside the shared database. The value is never formatted into a refusal or an
// error: a rejection names the secret and never what it was handed, because
// every path out of this function ends up in a log line.
func SetToken(l Local, value []byte, now time.Time) error {
	if len(bytes.TrimSpace(value)) == 0 {
		return fmt.Errorf("sync: %s is empty: %w", SecretToken, db.ErrInvalid)
	}
	return l.Tx(func(t *db.Tx) error {
		if err := t.SecretPut(SecretToken, value, now.UTC()); err != nil {
			return fmt.Errorf("sync: set %s: %w", SecretToken, err)
		}
		return nil
	})
}

// ReadToken returns this installation's remote token out of the local file; ok
// is false when the machine has none. The value is returned to the caller that
// builds a client with it and is never logged on the way out.
func ReadToken(l Local) (value []byte, ok bool, err error) {
	value, ok, err = l.SecretGet(SecretToken)
	if err != nil {
		return nil, false, fmt.Errorf("sync: read %s: %w", SecretToken, err)
	}
	return value, ok, nil
}

// DeleteToken removes this installation's remote token. An absent token is a
// no-op rather than an error, so a machine that never had one still turns sync
// off cleanly.
func DeleteToken(l Local) error {
	return l.Tx(func(t *db.Tx) error {
		if err := t.SecretDelete(SecretToken); err != nil {
			return fmt.Errorf("sync: delete %s: %w", SecretToken, err)
		}
		return nil
	})
}
