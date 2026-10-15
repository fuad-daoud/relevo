package sync

import (
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// An enable --join has to store the remote and the token before it can open a
// worker at all: the opener reads them out of the machine-local file rather than
// taking them as arguments, because that is the only place the daemon's worker
// assembly lives. So the join stages those two rows, checks the passphrase
// against the remote, and -- if the check fails -- puts every row back exactly as
// it found it.
//
// Exactly is the word that does the work. A machine that asked to join with the
// wrong passphrase and was left pointed at somebody else's remote, or holding a
// token it could not use, would be a machine whose next enable does something the
// operator never asked for. Restoring means putting the bytes back, including the
// rows that were absent before, rather than writing a default over them.

// JoinSnapshot is the machine-local state an enable --join must be able to put
// back: the sync section, the token, the key, every secret the fleet's document
// may land on, the fact about the remote's key, the enabled marker, the fact
// that an arriving client key replaced a held one, and the join marker.
//
// The secret rows are the whole allow-list, not an R2 subset, because the
// document apply runs on the worker the passphrase check drives and every
// refusal after it has to leave the machine byte-identical. A join refused at
// the settle must not keep the fleet's shared values -- the typesafe key and a
// stamped client key that replaced the one this machine generated for itself.
//
// It records absence as well as value, because "there was no token here" and
// "the token here was this" are different machines and only the first one is what
// a refused join should leave behind.
type JoinSnapshot struct {
	section      []byte
	hasSection   bool
	token        []byte
	hasToken     bool
	key          []byte
	hasKey       bool
	remoteKDF    []byte
	hasRemoteKDF bool
	enabled      []byte
	hasEnabled   bool
	// secrets holds one row per allow-listed name, in allowedSecrets order.
	secrets []heldRow
	// clientKeyReplaced is the machine-local row that says an arriving client
	// key took the place of one this machine already held.
	clientKeyReplaced heldRow
	join              heldRow
}

// heldRow is one row's bytes and whether it was there at all.
type heldRow struct {
	value   []byte
	present bool
}

// KeyClientKeyReplaced is the machine-local row that says an arriving client key
// took the place of one this machine already held. It is spelled here rather
// than imported because the config package imports this one, and the restore is
// part of what a refused join owes the machine -- the same reason
// SecretClientKey and SecretTypesafe are spelled in this package.
const KeyClientKeyReplaced = "config/client_key_replaced"

// SnapshotJoin reads every row an enable --join stages, or the document apply it
// runs writes, on the machine-local file. A row that was absent stays absent on
// restore.
func SnapshotJoin(l Local) (JoinSnapshot, error) {
	var s JoinSnapshot
	var err error
	s.secrets = make([]heldRow, len(allowedSecrets))
	if s.section, s.hasSection, err = l.ConfigGet(SectionSettings); err != nil {
		return JoinSnapshot{}, fmt.Errorf("sync: snapshot the %s section: %w", SectionSettings, err)
	}
	if s.token, s.hasToken, err = l.SecretGet(SecretToken); err != nil {
		return JoinSnapshot{}, fmt.Errorf("sync: snapshot %s: %w", SecretToken, err)
	}
	if s.key, s.hasKey, err = l.SecretGet(SecretKey); err != nil {
		return JoinSnapshot{}, fmt.Errorf("sync: snapshot %s: %w", SecretKey, err)
	}
	if s.remoteKDF, s.hasRemoteKDF, err = l.KVGet(KeyRemoteKDF); err != nil {
		return JoinSnapshot{}, fmt.Errorf("sync: snapshot %s: %w", KeyRemoteKDF, err)
	}
	if s.enabled, s.hasEnabled, err = l.KVGet(KeyEnabled); err != nil {
		return JoinSnapshot{}, fmt.Errorf("sync: snapshot %s: %w", KeyEnabled, err)
	}
	for i, name := range allowedSecrets {
		if s.secrets[i].value, s.secrets[i].present, err = l.SecretGet(name); err != nil {
			return JoinSnapshot{}, fmt.Errorf("sync: snapshot %s: %w", name, err)
		}
	}
	if s.clientKeyReplaced.value, s.clientKeyReplaced.present, err = l.ConfigGet(KeyClientKeyReplaced); err != nil {
		return JoinSnapshot{}, fmt.Errorf("sync: snapshot %s: %w", KeyClientKeyReplaced, err)
	}
	if s.join.value, s.join.present, err = l.KVGet(KeyJoin); err != nil {
		return JoinSnapshot{}, fmt.Errorf("sync: snapshot %s: %w", KeyJoin, err)
	}
	return s, nil
}

// Restore puts back exactly what SnapshotJoin read.
//
// A row that was absent is deleted rather than written empty: an absent token is
// how a machine says it has none, and a section written with no remote in it is
// a different thing that the preflight would read as a machine pointed nowhere.
func (s JoinSnapshot) Restore(l Local, now time.Time) error {
	return l.Tx(func(t *db.Tx) error {
		if err := restoreConfig(t, SectionSettings, s.section, s.hasSection, now); err != nil {
			return err
		}
		if err := restoreSecret(t, SecretToken, s.token, s.hasToken); err != nil {
			return err
		}
		if err := restoreSecret(t, SecretKey, s.key, s.hasKey); err != nil {
			return err
		}
		if err := restoreMarker(t, KeyRemoteKDF, s.remoteKDF, s.hasRemoteKDF); err != nil {
			return err
		}
		for i, name := range allowedSecrets {
			if err := restoreSecret(t, name, s.secrets[i].value, s.secrets[i].present); err != nil {
				return err
			}
		}
		if err := restoreConfig(t, KeyClientKeyReplaced, s.clientKeyReplaced.value, s.clientKeyReplaced.present, now); err != nil {
			return err
		}
		if err := restoreMarker(t, KeyJoin, s.join.value, s.join.present); err != nil {
			return err
		}
		return restoreMarker(t, KeyEnabled, s.enabled, s.hasEnabled)
	})
}

// restoreConfig puts one machine-local config row back. The sync section and
// the replaced row both travel this way: an absent row is deleted rather than
// written empty, and a present one is put back with these bytes, so a reader
// cannot meet a row of its own default between the two.
func restoreConfig(t *db.Tx, key string, body []byte, present bool, now time.Time) error {
	if !present {
		if err := t.ConfigDelete(key); err != nil {
			return fmt.Errorf("sync: restore %s: %w", key, err)
		}
		return nil
	}
	if err := t.ConfigPut(key, body, now.UTC()); err != nil {
		return fmt.Errorf("sync: restore %s: %w", key, err)
	}
	return nil
}

func restoreSecret(t *db.Tx, name string, value []byte, present bool) error {
	if !present {
		if err := t.SecretDelete(name); err != nil {
			return fmt.Errorf("sync: restore %s: %w", name, err)
		}
		return nil
	}
	if err := t.SecretPut(name, value, time.Unix(0, 0).UTC()); err != nil {
		return fmt.Errorf("sync: restore %s: %w", name, err)
	}
	return nil
}

func restoreMarker(t *db.Tx, key string, value []byte, present bool) error {
	if !present {
		if err := t.KVDelete(key); err != nil {
			return fmt.Errorf("sync: restore %s: %w", key, err)
		}
		return nil
	}
	if err := t.KVPut(key, value); err != nil {
		return fmt.Errorf("sync: restore %s: %w", key, err)
	}
	return nil
}
