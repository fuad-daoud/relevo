package consent

import (
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The one place a consent write's stamp comes from.
//
// internal/db cannot name the hybrid clock: the clock is synclog's, and synclog
// imports internal/db. So the documents' package owns the clock and hands the db
// package a function to call inside the write's own transaction, which keeps the
// stamp in the transaction of the answer it stamps -- the property the whole
// arrangement exists to hold -- without the import cycle.
//
// txKV lets the clock keep its position through that transaction, so the
// position this machine has reached moves only if the answer commits. A stamp
// whose clock position advanced for a write that then rolled back would leave
// this machine issuing stamps above a position no write ever reached, and a
// redelivery would then be refused as older than an answer that never landed.
type txKV struct{ t *db.Tx }

func (k txKV) KVGet(key string) ([]byte, bool, error) { return k.t.KVGet(key) }
func (k txKV) KVPut(key string, v []byte) error       { return k.t.KVPut(key, v) }
func (k txKV) KVDelete(key string) error              { return k.t.KVDelete(key) }

// stampNow issues the next stamp for a write inside tx.
func stampNow(tx *db.Tx) (string, error) {
	stamp, err := synclog.NewHybridClockAt(txKV{tx}, time.Now).Now()
	if err != nil {
		return "", fmt.Errorf("consent: stamp: %w", err)
	}
	return stamp.String(), nil
}

func init() { db.SetStampIssuer(stampNow) }
