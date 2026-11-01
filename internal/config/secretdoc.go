package config

// SecretDoc is the six secrets a fleet shares, one key per name, each value
// sealed under the sync key so the log carries ciphertext. It reads and writes
// the machine-local secret table and adds the allow-list, checked on the way out
// and on the way in.
//
// Every refusal names the secret and never its value or envelope: a refusal is
// read by whoever holds the log.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// Secrets is the name the secrets document travels under, as `doc:secrets` on the
// log. It is the sync package's name for the same document, which is where the
// exporter's stamping reads it from.
const Secrets = sync.SecretsDocName

// ErrNotSharedSecret marks an arriving name the document does not carry. It is the
// document's own refusal rather than the envelope's, so a caller can tell "no reader
// for that name" from "not sealed right"; both are refusals rather than failures.
var ErrNotSharedSecret = fmt.Errorf("config: this secret does not travel: %w", ErrInvalidValue)

// secretSupersededSource labels the revision an arriving secret lands under when it
// did not, followed by the sending installation: the entry was newer and still lost
// because this machine could not read it or was not allowed to take it.
const secretSupersededSource = "sync-superseded:secrets:"

// secretStampKey is the write-stamp key of one secret, owned by the sync package
// where the exporter reads stamps from.
func secretStampKey(name string) string { return sync.SecretStampKey(name) }

// SecretDoc is the secrets document as a synclog document: one key per shared secret
// name, one sealed value per name. A machine that holds no key holds no document:
// Items answers with an error rather than an empty map, because the exporter would
// read an absent key as a removal for every secret the fleet has.
type SecretDoc struct {
	store *Store
	now   func() time.Time
}

// NewSecretDoc binds the secrets document to one store. now overrides the store's own
// clock for a test that stamps at a chosen time.
func NewSecretDoc(s *Store, now func() time.Time) *SecretDoc {
	if now == nil {
		now = s.now
	}
	return &SecretDoc{store: s, now: now}
}

// SecretsDoc returns the secrets document of this store, for the sync runtime to
// register once it has one.
func (s *Store) SecretsDoc() *SecretDoc { return NewSecretDoc(s, nil) }

// Name is the document's name, the word an entry's table carries after `doc:`.
func (d *SecretDoc) Name() string { return Secrets }

// Items returns the document as one sealed value per shared secret this machine
// holds, filtered to the allow-list. A secret outside the list is never read here:
// the table holds this machine's identity, which an exporter walking it by name
// would hand to a peer.
//
// The client key is the one entry whose export is conditional. An unstamped key is
// this machine's own, and handing it to the fleet would silently replace the
// identity of every machine already holding one, so it travels only once a person
// has put it here on purpose.
//
// Every other allow-listed secret travels whether or not it was stamped: a joiner
// is owed them without the owner re-running enable, so such a value takes its
// stamp from the clock at export time.
func (d *SecretDoc) Items() (map[string]json.RawMessage, error) {
	ring, err := readKeyringFrom(d.store.db)
	if err != nil {
		return nil, err
	}
	names, err := d.store.SecretNames()
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	for _, name := range names {
		if !sync.AllowSecret(name) {
			continue
		}
		if name == SecretClientKey && !d.stamped(name) {
			continue
		}
		item, held, err := d.store.Secret(name)
		if err != nil {
			return nil, err
		}
		if !held {
			continue
		}
		sealed, err := sync.SealSecret(ring, name, item)
		if err != nil {
			return nil, err
		}
		out[name] = sealed
	}
	return out, nil
}

// stamped reports whether a person wrote this secret on this machine; a peer-applied,
// generated or re-marshalled value answers false.
func (d *SecretDoc) stamped(name string) bool {
	_, ok, err := d.ItemWriteStamp(name)
	return err == nil && ok
}

// ResealKeepsStamp marks this document as one whose re-seals must not outrank a
// peer's edit. A rekey re-seals every value under the newest epoch, so stamping
// that re-seal from the clock would overwrite an edit made while the key was
// rotating; the re-emission keeps the stamp the key already had, so the peer's
// newer edit wins on every reader including this one.
func (d *SecretDoc) ResealKeepsStamp() bool { return true }

// Apply writes one secret's value, or clears it when value is nil, in one
// transaction with nothing else. A caller with the importer's transaction uses
// ApplyIn instead.
func (d *SecretDoc) Apply(key string, value json.RawMessage) error {
	if refusal, err := d.check(key, value); refusal != nil || err != nil {
		return refusal
	}
	return d.store.db.Tx(func(t *db.Tx) error { return d.applyIn(t, key, value, "") })
}

// ApplyIn is Apply inside the importer's transaction, for a value that arrived
// from the installation labelled label. The arriving value is opened before
// anything is written, so an unreadable envelope never leaves the table changed;
// a refusal changes nothing and the importer records it once no pass can land it.
//
// The write is unstamped: a peer's value is not this machine's edit, and the
// importer's applied state already stops it echoing straight back.
func (d *SecretDoc) ApplyIn(t *db.Tx, key string, value json.RawMessage, label string) (refusal error, err error) {
	if refusal, err := d.check(key, value); refusal != nil || err != nil {
		return refusal, err
	}
	return nil, d.applyIn(t, key, value, label)
}

// check refuses everything an arriving secret may not be, before a byte is
// written: the name against the allow-list, the value against the envelope rules.
// Both are refusals rather than failures, so the importer retries and then records
// them as lost. A tombstone carries no value and is checked against the name alone.
func (d *SecretDoc) check(key string, value json.RawMessage) (refusal error, err error) {
	if !sync.AllowSecret(key) {
		return fmt.Errorf("config: %s: %w", key, ErrNotSharedSecret), nil
	}
	if value == nil {
		return nil, nil
	}
	// An arriving client key is opened here and not in applyIn, so an unreadable value
	// is refused before the client key path is touched: a malformed PEM would replace
	// the identity this machine is enrolled under on every server.
	ring, err := readKeyringFrom(d.store.db)
	if err != nil {
		return err, nil
	}
	plain, err := sync.OpenSecret(ring, key, value)
	if err != nil {
		return err, nil
	}
	if key == SecretClientKey {
		if _, perr := remote.ParsePrivate(plain); perr != nil {
			return fmt.Errorf("config: an arriving %s is not a private key: %w: %w",
				key, perr, ErrInvalidValue), nil
		}
	}
	return nil, nil
}

// applyIn is the write itself, inside the caller's transaction. A nil value clears
// the secret: the removal travels as an upsert whose value is null, so the stamp
// reaches the reader that removes the name.
func (d *SecretDoc) applyIn(t *db.Tx, key string, value json.RawMessage, label string) error {
	if value == nil {
		return d.store.deleteSecretApplied(t, key, label)
	}
	ring, err := readKeyringFrom(t)
	if err != nil {
		return err
	}
	plain, err := sync.OpenSecret(ring, key, value)
	if err != nil {
		return err
	}
	if err := d.noteClientKeyReplaced(t, key, plain); err != nil {
		return err
	}
	return d.store.putSecretApplied(t, key, plain, label)
}

// ClientKeyReplacedKey is where the machine-local row records that an arriving
// client key replaced the one held. It is a row rather than a revision because it
// is a fact about this machine's state now: `relevo doctor` reads it to say the
// identity changed and the servers need the new enrolment line. It is the sync
// package's name for the same row, so the two spellings cannot drift.
const ClientKeyReplacedKey = sync.KeyClientKeyReplaced

// ClientKeyReplaced is what the row holds: the identity this machine had before
// the arrival, and the one it has now. Both are client ids rather than keys, so
// the row can be read and printed by anyone who opens this machine's file.
type ClientKeyReplaced struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// noteClientKeyReplaced records the swap an arriving client key makes, and only
// the swap: an equal value is a redelivery, and another name is not an identity. The
// row is what makes the arrival reportable; without it a replaced machine would
// silently send requests the servers no longer accept.
func (d *SecretDoc) noteClientKeyReplaced(t *db.Tx, key string, plain []byte) error {
	if key != SecretClientKey {
		return nil
	}
	before, held, err := t.SecretGet(key)
	if err != nil {
		return err
	}
	if !held || bytes.Equal(bytes.TrimSpace(before), bytes.TrimSpace(plain)) {
		return nil
	}
	swapped, err := clientIDsOf(before, plain)
	if err != nil {
		return err
	}
	body, err := json.Marshal(swapped)
	if err != nil {
		return err
	}
	return t.ConfigPut(ClientKeyReplacedKey, body, d.now().UTC())
}

// clientIDsOf is the pair of identities two stored keys stand for, or an error when
// either will not parse; an unparsable key is refused because the row must name two
// actionable identities.
func clientIDsOf(before, after []byte) (ClientKeyReplaced, error) {
	oldPair, err := remote.ParsePrivate(before)
	if err != nil {
		return ClientKeyReplaced{}, fmt.Errorf("config: the stored %s is not a private key: %w",
			SecretClientKey, err)
	}
	newPair, err := remote.ParsePrivate(after)
	if err != nil {
		return ClientKeyReplaced{}, fmt.Errorf("config: an arriving %s is not a private key: %w", SecretClientKey, err)
	}
	return ClientKeyReplaced{
		From: string(remote.IDOf(oldPair.Public)),
		To:   string(remote.IDOf(newPair.Public)),
	}, nil
}

// ReadClientKeyReplaced is what this machine recorded when an arriving client key
// replaced the one held. A machine that never had one swapped answers false, which is
// the ordinary state and not a fault.
func (s *Store) ReadClientKeyReplaced() (ClientKeyReplaced, bool, error) {
	body, ok, err := s.db.LocalOrSelf().ConfigGet(ClientKeyReplacedKey)
	if err != nil || !ok {
		return ClientKeyReplaced{}, false, err
	}
	var swapped ClientKeyReplaced
	if err := json.Unmarshal(body, &swapped); err != nil {
		return ClientKeyReplaced{}, false, fmt.Errorf("config: read the %s row: %w: %w",
			ClientKeyReplacedKey, err, ErrInvalidValue)
	}
	return swapped, true, nil
}

// SupersedeIn records a value that did not land and changes nothing else. It records
// the name and the reason and never the value: neither a sealed envelope nor a
// plaintext belongs in a revision. The snapshot is the config document, in which
// every secret is absent, so rolling back here is a no-op on the secret table.
func (d *SecretDoc) SupersedeIn(t *db.Tx, key string, value json.RawMessage, label, why string) error {
	before, err := readSnapshot(t)
	if err != nil {
		return err
	}
	changes, err := json.Marshal([]Change{{Path: "secret." + key, Op: "superseded"}})
	if err != nil {
		return err
	}
	snapshot, err := EncodeDoc(before.doc)
	if err != nil {
		return err
	}
	_, err = t.RevisionInsert(db.RevisionRow{
		At:       d.now(),
		Source:   secretSupersededSource + label,
		Message:  why,
		Version:  before.version,
		Changes:  changes,
		Snapshot: snapshot,
	})
	return err
}

// ItemWriteStamp is the stamp recorded when key was last written on this machine.
// A peer-applied, generated or re-marshalled value records none, which keeps an
// unstamped client key off the log.
func (d *SecretDoc) ItemWriteStamp(key string) (synclog.Stamp, bool, error) {
	text, ok, err := d.store.db.ConfigWriteStamp(secretStampKey(key))
	if err != nil || !ok {
		return 0, false, err
	}
	stamp, err := synclog.ParseStamp(text)
	return stamp, err == nil, err
}

// ItemWriteStampIn is ItemWriteStamp inside the caller's transaction, which is
// how an arriving value is weighed against a write made here.
func (d *SecretDoc) ItemWriteStampIn(t *db.Tx, key string) (synclog.Stamp, bool, error) {
	text, ok, err := t.ConfigWriteStamp(secretStampKey(key))
	if err != nil || !ok {
		return 0, false, err
	}
	stamp, err := synclog.ParseStamp(text)
	return stamp, err == nil, err
}

// readKeyringFrom is the sync keyring as the surface that holds it reads it,
// for a caller that must not depend on the whole database. A machine with no
// key is refused with the error the document's own refusal names: a locked
// machine holds no document at all, and every caller answers with an error
// rather than an empty one.
func readKeyringFrom(secrets sync.SecretReader) (sync.Keyring, error) {
	ring, ok, err := sync.ReadKeyringFrom(secrets)
	if err != nil {
		return sync.Keyring{}, err
	}
	if !ok {
		return sync.Keyring{}, fmt.Errorf("config: the %s document needs the sync key: %w", Secrets, sync.ErrNoKey)
	}
	return ring, nil
}
