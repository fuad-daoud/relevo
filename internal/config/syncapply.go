package config

import (
	"encoding/json"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
)

// syncedSourcePrefix labels the revision an applied item lands in, followed
// by the writing installation's label.
const syncedSourcePrefix = "sync:"

// ApplyIn is Apply inside the caller's transaction, for an item arriving
// from the installation labelled label. The change lands through the same
// write path `relevo config set` uses (writeSection, then record), so the
// version moves and a running reader reloads without a restart; the revision
// it leaves reads sync:<label>.
//
// refusal is non-nil when the item cannot land now: a key this document has
// no shape for, or a value leaving the merged config invalid. Nothing is
// recorded and nothing changes, because an item depending on another still on
// its way refuses here and accepts a moment later; the caller records it with
// SupersedeIn once it knows it never will. Only a failing store is an error.
func (d *SharedDoc) ApplyIn(t *db.Tx, key string, value json.RawMessage, label string) (refusal error, err error) {
	sec, name, err := splitKey(key)
	if err == nil && !syncs(sec) {
		err = fmt.Errorf("config: the %s section never syncs: %w", sec, ErrInvalidValue)
	}
	if err != nil {
		return err, nil
	}
	return d.store.As(syncedSourcePrefix+label, "").applyKeyIn(t, sec, name, value)
}

// SupersedeIn records an item that lost to what this machine holds, and
// changes nothing.
func (d *SharedDoc) SupersedeIn(t *db.Tx, key string, value json.RawMessage, label, why string) error {
	before, err := readSnapshot(t)
	if err != nil {
		return err
	}
	return d.store.recordSuperseded(t, before, key, value, label, fmt.Errorf("%s", why))
}
