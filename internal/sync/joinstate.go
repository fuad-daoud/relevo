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
	// secretDoc is the secrets document's machine-local bookkeeping: the
	// (origin, stamp) each of its keys was last read at, and the digest each
	// was last compared by. The document's apply path records both before the
	// rows are written, so a restore that put the rows back without it would
	// leave every entry the pre-pass applied reading as a redelivery and land
	// on nothing.
	secretDoc docSecretsBookkeeping
	// clientKeyReplaced is the machine-local row that says an arriving client
	// key took the place of one this machine already held.
	clientKeyReplaced heldRow
	join              heldRow
}

// docSecretsBookkeeping is one doc's machine-local bookkeeping, read as rows
// and put back as rows: what each key was last applied at, what digest it was
// last compared by, and whether the doc was signalled when it was read. The
// tables move together because they record the same arrival -- the export
// state is how the exporter sees what the applied state is.
type docSecretsBookkeeping struct {
	applied   []db.DocAppliedRow
	export    []db.DocExportStateRow
	signalled bool
}

// readDocSecretsBookkeeping reads the secrets document's bookkeeping off the
// machine-local file, the file the document's apply path wrote it to.
func readDocSecretsBookkeeping(l Local) (docSecretsBookkeeping, error) {
	var kept docSecretsBookkeeping
	err := l.Tx(func(t *db.Tx) error {
		var err error
		if kept.applied, err = t.DocAppliedRows(SecretsDocName); err != nil {
			return err
		}
		if kept.export, err = t.DocExportStateRows(SecretsDocName); err != nil {
			return err
		}
		if kept.signalled, err = t.DocSignalled(SecretsDocName); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return docSecretsBookkeeping{}, fmt.Errorf("sync: read the %s document's bookkeeping: %w", SecretsDocName, err)
	}
	return kept, nil
}

// restoreDocSecretsBookkeeping puts the secrets document's bookkeeping back the
// way readDocSecretsBookkeeping read it. A signal the document carried when it
// was read stands -- it is the echo of a write a caller this enable passed, not
// of the state that was just put back -- while a signal the clear itself
// planted is struck, because what it points at is exactly the state these rows
// stand for. It is the whole restore in a transaction of its own, so a caller
// that has just run the one clear the join makes can keep the document's
// bookkeeping over it.
func restoreDocSecretsBookkeeping(l Local, kept docSecretsBookkeeping) error {
	err := l.Tx(func(t *db.Tx) error {
		if err := restoreSecretsDocBookkeeping(t, kept); err != nil {
			return err
		}
		if !kept.signalled {
			return t.ClearDocSignals(SecretsDocName)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("sync: restore the %s document's bookkeeping: %w", SecretsDocName, err)
	}
	return nil
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
	if s.secretDoc, err = readDocSecretsBookkeeping(l); err != nil {
		return JoinSnapshot{}, err
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
		if err := restoreSecretsDocBookkeeping(t, s.secretDoc); err != nil {
			return err
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

// restoreSecretsDocBookkeeping puts the secrets document's applied and export
// rows back the way the snapshot read them, inside the caller's transaction.
// Every row recorded for the document since then is deleted and the snapshot's
// rows written back, so a retried join meets the state a refused first join met
// rather than its own leftovers.
func restoreSecretsDocBookkeeping(t *db.Tx, kept docSecretsBookkeeping) error {
	if err := t.DeleteDocApplied(SecretsDocName); err != nil {
		return fmt.Errorf("sync: restore the %s document's applied state: %w", SecretsDocName, err)
	}
	if err := t.DeleteDocExport(SecretsDocName); err != nil {
		return fmt.Errorf("sync: restore the %s document's export state: %w", SecretsDocName, err)
	}
	for _, row := range kept.applied {
		if err := t.SetDocApplied(SecretsDocName, row.Key, row.Origin, row.Stamp); err != nil {
			return fmt.Errorf("sync: restore the %s %s applied state: %w", SecretsDocName, row.Key, err)
		}
	}
	for _, row := range kept.export {
		if err := t.SetDocExported(SecretsDocName, row.Key, row.Origin, row.Stamp, row.Digest); err != nil {
			return fmt.Errorf("sync: restore the %s %s export state: %w", SecretsDocName, row.Key, err)
		}
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
