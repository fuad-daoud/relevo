package sync

import (
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// An enable --join stages remote and token before opening a worker: the opener
// reads them from the machine-local file, the daemon's only worker assembly.
// On a failed passphrase check every row goes back exactly as found -- bytes and
// absence alike, never a default -- so no next enable acts on an unasked remote
// or unusable token.

// JoinSnapshot is the machine-local state an enable --join must be able to put
// back: section, token, key, every allow-listed secret the document may land on,
// remote-key fact, enabled marker, client-key-replaced fact, and join marker.
// Secrets cover the whole allow-list: the apply runs on the passphrase check's
// worker, so every later refusal leaves the machine byte-identical. Absence records
// alongside value: no token here differs from this token here, and only the first
// is what a refused join leaves.
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

// docSecretsBookkeeping is one doc's machine-local bookkeeping, read and restored
// as rows: last applied at, last compared digest, and whether signalled. The tables
// move together, recording the same arrival.
type docSecretsBookkeeping struct {
	applied   []db.DocAppliedRow
	export    []db.DocExportStateRow
	signalled bool
}

// fromPeers is the bookkeeping a join keeps over its fresh start, from peers'
// entries only: own-origin export rows name a log the joined log never saw, so
// they drop and the document stays signalled for the next export.
func (k docSecretsBookkeeping) fromPeers(self string) docSecretsBookkeeping {
	out := k
	out.export = nil
	for _, row := range k.export {
		if row.Origin == self {
			out.signalled = true
			continue
		}
		out.export = append(out.export, row)
	}
	return out
}

// readDocSecretsBookkeeping reads the secrets document's bookkeeping off the
// machine-local file its apply path wrote to.
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

// restoreDocSecretsBookkeeping puts the secrets document's bookkeeping back as read.
// A signal carried at read time stands -- an echo of this enable's caller write,
// not the restored state -- while a signal the clear planted is struck, since it
// points at exactly the state these rows stand for. It runs in its own transaction,
// so a caller that just ran the join's clear keeps the bookkeeping over it.
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

// heldRow is one row's bytes and whether it was there.
type heldRow struct {
	value   []byte
	present bool
}

// KeyClientKeyReplaced is the machine-local row saying an arriving client key took
// a held one's place, spelled here not imported: the config package imports this
// one, and its restore is part of what a refused join owes the machine -- as
// SecretClientKey does.
const KeyClientKeyReplaced = "config/client_key_replaced"

// SnapshotJoin reads every row an enable --join stages or its document apply writes;
// absent stays absent on restore.
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

// Restore puts back exactly what SnapshotJoin read: absent deletes rather than
// writing empty, since an absent token says none and a remoteless section reads as
// pointed nowhere.
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

// restoreConfig puts one machine-local config row back: absent deletes, present
// restores these bytes, so no reader meets a default row between the two.
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

// restoreSecretsDocBookkeeping puts the secrets document's applied and export rows
// back as snapshotted, inside the caller's transaction: later rows delete, snapshot
// rows return, so a retried join meets the refused first join's state.
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
